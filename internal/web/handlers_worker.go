package web

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// authenticateWorkerFromBearer extracts "Bearer <token>" from the Authorization header
// and resolves api_tokens.secret -> users.id (user_id).
func (s *Server) authenticateWorkerFromBearer(r *http.Request) (userID int64, ok bool) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return 0, false
	}

	parts := strings.Fields(auth)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return 0, false
	}
	secret := parts[1]

	var id int64
	err := s.DB.QueryRow(`
        SELECT user_id
        FROM api_tokens
        WHERE secret = $1 AND revoked = FALSE
    `, secret).Scan(&id)
	if err != nil {
		if err != sql.ErrNoRows {
			log.Printf("authenticateWorkerFromBearer: db error: %v", err)
		}
		return 0, false
	}
	return id, true
}

// HandleWorkerDratUpload handles:
//
//	POST /worker/drat
//
// Headers:
//
//	Authorization: Bearer <api_token>
//
// Body (multipart/form-data):
//
//	field "proof" : the DRAT file bytes
//
// Behavior:
//   - Authenticates worker via api_tokens.
//   - Saves DRAT file under UploadBaseDir + "/drat/user-<workerID>/".
//   - Inserts row into drat_files.
//   - Returns JSON { ok: true, drat_file_id: <id> }.
//
// This endpoint does *not* run drat-trim or decide validity. It only stores
// the file and returns its database ID. The gRPC JobResult handling code is
// responsible for verifying the proof against the CNF.
func (s *Server) HandleWorkerDratUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	workerID, ok := s.authenticateWorkerFromBearer(r)
	if !ok {
		s.jsonError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Parse multipart up to 64MB (adjust if needed).
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		log.Printf("HandleWorkerDratUpload: parse multipart: %v", err)
		s.jsonError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}

	file, header, err := r.FormFile("proof")
	if err != nil {
		log.Printf("HandleWorkerDratUpload: missing proof file: %v", err)
		s.jsonError(w, http.StatusBadRequest, "missing proof file")
		return
	}
	defer file.Close()

	originalName := header.Filename
	if originalName == "" {
		originalName = "proof.drat"
	}

	// Store DRAT files under: <UploadBaseDir>/drat/user-<id>/
	baseDir := filepath.Join(UploadBaseDir, "drat")
	userDir := filepath.Join(baseDir, fmt.Sprintf("user-%d", workerID))
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		log.Printf("HandleWorkerDratUpload: mkdir: %v", err)
		s.jsonError(w, http.StatusInternalServerError, "internal error")
		return
	}

	ts := time.Now().UnixNano()
	filename := fmt.Sprintf("%d-%d-%s", workerID, ts, originalName)
	storagePath := filepath.Join(userDir, filename)

	out, err := os.Create(storagePath)
	if err != nil {
		log.Printf("HandleWorkerDratUpload: create file: %v", err)
		s.jsonError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer out.Close()

	n, err := io.Copy(out, file)
	if err != nil {
		log.Printf("HandleWorkerDratUpload: copy file: %v", err)
		s.jsonError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Insert drat_files row; ownership is by worker's user_id.
	var dratFileID int64
	err = s.DB.QueryRow(`
        INSERT INTO drat_files (
            user_id,
            original_name,
            storage_path,
            size_bytes
        )
        VALUES ($1, $2, $3, $4)
        RETURNING id
    `, workerID, originalName, storagePath, n).Scan(&dratFileID)
	if err != nil {
		log.Printf("HandleWorkerDratUpload: insert drat_files: %v", err)
		// best-effort cleanup
		_ = os.Remove(storagePath)
		s.jsonError(w, http.StatusInternalServerError, "internal error")
		return
	}

	s.json(w, http.StatusOK, map[string]any{
		"ok":           true,
		"drat_file_id": dratFileID,
	})
}

// HandleWorkerCnfDownload handles:
//
//	GET /worker/cnf/{jobID}
//
// Headers:
//
//	Authorization: Bearer <api_token>
//
// Behavior:
//   - Authenticates worker via api_tokens.
//   - Looks up the CNF file path for jobs.id = {jobID}.
//   - Streams the CNF file contents back as text/plain.
func (s *Server) HandleWorkerCnfDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	_, ok := s.authenticateWorkerFromBearer(r)
	if !ok {
		s.jsonError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Using chi URL params: /worker/cnf/{jobID}
	jobIDStr := chi.URLParam(r, "jobID")
	if jobIDStr == "" {
		s.jsonError(w, http.StatusBadRequest, "missing jobID")
		return
	}

	jobID, err := strconv.ParseInt(jobIDStr, 10, 64)
	if err != nil {
		s.jsonError(w, http.StatusBadRequest, "invalid jobID")
		return
	}

	// Lookup CNF path & original name for this job.
	var storagePath, originalName string
	err = s.DB.QueryRow(`
        SELECT cf.storage_path, cf.original_name
        FROM jobs j
        JOIN cnf_files cf ON j.cnf_file_id = cf.id
        WHERE j.id = $1
    `, jobID).Scan(&storagePath, &originalName)
	if err != nil {
		if err == sql.ErrNoRows {
			s.jsonError(w, http.StatusNotFound, "job or cnf not found")
			return
		}
		log.Printf("HandleWorkerCnfDownload: db error: %v", err)
		s.jsonError(w, http.StatusInternalServerError, "internal error")
		return
	}

	f, err := os.Open(storagePath)
	if err != nil {
		log.Printf("HandleWorkerCnfDownload: open file: %v", err)
		s.jsonError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer f.Close()

	if originalName == "" {
		originalName = filepath.Base(storagePath)
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, originalName))

	if _, err := io.Copy(w, f); err != nil {
		log.Printf("HandleWorkerCnfDownload: copy: %v", err)
	}
}
