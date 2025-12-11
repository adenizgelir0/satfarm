package web

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"satfarm/internal/sat"
	"strconv"
	"strings"
	"time"
)

func (s *Server) HandleCreateJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	userID, ok := s.getCurrentUserID(r)
	if !ok || userID == 0 {
		s.json(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": "unauthorized",
		})
		return
	}

	// Parse multipart up to 64MB (adjust as needed)
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		log.Println("jobs/new: parse multipart:", err)
		s.json(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": "invalid upload",
		})
		return
	}

	file, header, err := r.FormFile("cnf")
	if err != nil {
		log.Println("jobs/new: form file:", err)
		s.json(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": "missing cnf file",
		})
		return
	}
	defer file.Close()

	maxWorkersStr := r.FormValue("max_workers")
	if maxWorkersStr == "" {
		s.json(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": "missing max_workers",
		})
		return
	}
	maxWorkers, err := strconv.Atoi(maxWorkersStr)
	if err != nil || maxWorkers < 1 {
		s.json(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": "invalid max_workers",
		})
		return
	}

	originalName := header.Filename
	if originalName == "" {
		originalName = "job.cnf"
	}

	// Ensure upload directory exists: /app/uploads/user-<id>/
	userDir := filepath.Join(UploadBaseDir, fmt.Sprintf("user-%d", userID))
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		log.Println("jobs/new: mkdir:", err)
		s.json(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": "server error (mkdir)",
		})
		return
	}

	// Build a unique filename
	filename := fmt.Sprintf("%d-%d-%s", userID, time.Now().UnixNano(), originalName)
	destPath := filepath.Join(userDir, filename)

	out, err := os.Create(destPath)
	if err != nil {
		log.Println("jobs/new: create file:", err)
		s.json(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": "server error (create file)",
		})
		return
	}
	defer out.Close()

	written, err := io.Copy(out, file)
	if err != nil {
		log.Println("jobs/new: copy file:", err)
		s.json(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": "server error (write file)",
		})
		return
	}

	// 🔹 Optional: normalize CNF on disk (strip anything after '%')
	cleanedPath, err := sat.PrepareCNFForSolving(destPath)
	if err != nil {
		log.Println("jobs/new: PrepareCNFForSolving:", err)

		// best-effort cleanup
		_ = os.Remove(destPath)

		s.json(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": "invalid CNF (preprocessing failed)",
		})
		return
	}

	// Replace original file with the cleaned one
	if err := os.Rename(cleanedPath, destPath); err != nil {
		log.Println("jobs/new: rename cleaned CNF:", err)

		_ = os.Remove(destPath)
		_ = os.Remove(cleanedPath)

		s.json(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": "server error (cnf normalize)",
		})
		return
	}

	// 🔹 Charge user for this upload
	cost := uploadCostSatc(written)
	meta := map[string]any{
		"type":       "upload_cnf",
		"filename":   originalName,
		"size_bytes": written,
	}

	if err := s.changeUserBalance(r.Context(), userID, -cost, "upload_cnf", meta); err != nil {
		log.Println("jobs/new: charge error:", err)

		// Clean up the uploaded file if we couldn't charge
		_ = os.Remove(destPath)

		if strings.Contains(err.Error(), "insufficient balance") {
			s.json(w, http.StatusOK, map[string]any{
				"ok":    false,
				"error": "insufficient balance",
			})
			return
		}

		s.json(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": "server error (billing)",
		})
		return
	}

	// Insert cnf_files + jobs in a transaction
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		log.Println("jobs/new: begin tx:", err)
		s.json(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": "server error (tx)",
		})
		return
	}
	defer tx.Rollback()

	var cnfFileID int64
	err = tx.QueryRow(`
        INSERT INTO cnf_files (user_id, original_name, storage_path, size_bytes)
        VALUES ($1, $2, $3, $4)
        RETURNING id
    `, userID, originalName, destPath, written).Scan(&cnfFileID)
	if err != nil {
		log.Println("jobs/new: insert cnf_files:", err)
		s.json(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": "server error (db cnf_files)",
		})
		return
	}

	var (
		jobID      int64
		jobCreated time.Time
	)
	err = tx.QueryRow(`
        INSERT INTO jobs (user_id, cnf_file_id, max_workers, status)
        VALUES ($1, $2, $3, 'pending')
        RETURNING id, created_at
    `, userID, cnfFileID, maxWorkers).Scan(&jobID, &jobCreated)
	if err != nil {
		log.Println("jobs/new: insert jobs:", err)
		s.json(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": "server error (db jobs)",
		})
		return
	}

	if err := tx.Commit(); err != nil {
		log.Println("jobs/new: commit:", err)
		s.json(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": "server error (commit)",
		})
		return
	}

	// 🔹 Enqueue job into scheduler
	if s.Scheduler != nil {
		go s.Scheduler.EnqueueJob(jobID, destPath, maxWorkers)
	} else {
		log.Printf("jobs/new: scheduler is nil, job %d not enqueued", jobID)
	}

	s.json(w, http.StatusOK, map[string]any{
		"ok": true,
		"job": map[string]any{
			"id":          jobID,
			"name":        "", // no custom name yet
			"file_name":   originalName,
			"created_at":  jobCreated.Format("2006-01-02 15:04"),
			"max_workers": maxWorkers,
			"status":      "pending",
			"cost_satc":   cost,
		},
	})
}

func uploadCostSatc(sizeBytes int64) int64 {
	if sizeBytes <= 0 {
		return 0
	}

	const perKB int64 = 1000 // 1000 sat per KB

	// match JS: ceil(sizeBytes * 1000 / 1024)
	// integer version: (sizeBytes*1000 + 1023) / 1024
	return (sizeBytes*perKB + 1023) / 1024
}
