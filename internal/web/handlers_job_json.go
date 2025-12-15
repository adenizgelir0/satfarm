package web

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

type jobDetailJSON struct {
	OK     bool          `json:"ok"`
	Job    JobView       `json:"job"`
	Result JobResultView `json:"result"`
	Shards []ShardView   `json:"shards"`
}

func (s *Server) HandleJobDetailJSON(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.getCurrentUserID(r)
	if !ok || userID == 0 {
		s.jsonError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	jobIDStr := chi.URLParam(r, "jobID")
	jobID, err := strconv.ParseInt(jobIDStr, 10, 64)
	if err != nil || jobID <= 0 {
		s.jsonError(w, http.StatusBadRequest, "invalid job id")
		return
	}

	// --- Load job and optional job_results row ---
	var (
		dbJobID    int64
		fileName   string
		status     string
		maxWorkers int
		createdAt  time.Time

		isSat      sql.NullBool
		resAssign  sql.NullString
		resCreated sql.NullTime
	)

	err = s.DB.QueryRowContext(r.Context(), `
        SELECT
            j.id,
            cf.original_name,
            j.status,
            j.max_workers,
            j.created_at,
            jr.is_sat,
            jr.assignment::text,
            jr.created_at
        FROM jobs j
        JOIN cnf_files cf ON j.cnf_file_id = cf.id
        LEFT JOIN job_results jr ON jr.job_id = j.id
        WHERE j.id = $1 AND j.user_id = $2
    `, jobID, userID).Scan(
		&dbJobID,
		&fileName,
		&status,
		&maxWorkers,
		&createdAt,
		&isSat,
		&resAssign,
		&resCreated,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			s.jsonError(w, http.StatusNotFound, "job not found")
			return
		}
		log.Printf("job detail json: load job: %v", err)
		s.jsonError(w, http.StatusInternalServerError, "internal error")
		return
	}

	jobView := JobView{
		ID:         dbJobID,
		Name:       fileName, // keep consistent with your existing HTML handler behavior
		FileName:   fileName,
		CreatedAt:  createdAt.Format("2006-01-02 15:04"),
		MaxWorkers: maxWorkers,
		Status:     status,
	}

	var resultView JobResultView
	if isSat.Valid {
		kind := "UNSAT"
		if isSat.Bool {
			kind = "SAT"
		}
		resultView.HasResult = true
		resultView.Kind = kind

		if resAssign.Valid {
			resultView.Assignment = resAssign.String
		}
		if resCreated.Valid {
			resultView.CreatedAt = resCreated.Time.Format("2006-01-02 15:04")
		}
	} else {
		resultView.HasResult = false
	}

	// --- Load shards + optional shard_results row ---
	rows, err := s.DB.QueryContext(r.Context(), `
        SELECT
            js.id,
            js.status,
            js.cube_literals::text,
            js.created_at,
            js.updated_at,
            sr.is_sat,
            sr.status,
            sr.assignment::text,
            sr.drat_file_id
        FROM job_shards js
        LEFT JOIN shard_results sr
            ON sr.shard_id = js.id
        WHERE js.job_id = $1
        ORDER BY js.id
    `, jobID)
	if err != nil {
		log.Printf("job detail json: load shards: %v", err)
		s.jsonError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()

	var shards []ShardView
	for rows.Next() {
		var (
			id         int64
			shStatus   string
			cubeText   string
			shCreated  time.Time
			shUpdated  time.Time
			srIsSat    sql.NullBool
			srStatus   sql.NullString
			srAssign   sql.NullString
			dratFileID sql.NullInt64
		)

		if err := rows.Scan(
			&id,
			&shStatus,
			&cubeText,
			&shCreated,
			&shUpdated,
			&srIsSat,
			&srStatus,
			&srAssign,
			&dratFileID,
		); err != nil {
			log.Printf("job detail json: scan shard: %v", err)
			s.jsonError(w, http.StatusInternalServerError, "internal error")
			return
		}

		resultKind := ""
		if srIsSat.Valid {
			if srIsSat.Bool {
				resultKind = "SAT"
			} else {
				resultKind = "UNSAT"
			}
		}

		resultStatus := ""
		if srStatus.Valid {
			resultStatus = srStatus.String
		}

		assignment := ""
		if srAssign.Valid {
			assignment = srAssign.String
		}

		dratLabel := ""
		if dratFileID.Valid {
			dratLabel = fmt.Sprintf("%d", dratFileID.Int64)
		}

		shards = append(shards, ShardView{
			ID:            id,
			Cube:          cubeText,
			ShardStatus:   shStatus,
			ResultKind:    resultKind,
			ResultStatus:  resultStatus,
			Assignment:    assignment,
			DratFileLabel: dratLabel,
			CreatedAt:     shCreated.Format("2006-01-02 15:04"),
			UpdatedAt:     shUpdated.Format("2006-01-02 15:04"),
		})
	}

	if err := rows.Err(); err != nil {
		log.Printf("job detail json: rows err: %v", err)
		s.jsonError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Write JSON (same helpers you already use)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	_ = json.NewEncoder(w).Encode(jobDetailJSON{
		OK:     true,
		Job:    jobView,
		Result: resultView,
		Shards: shards,
	})
}
