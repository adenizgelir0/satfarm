package web

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

// JobView is the same struct you already use on the dashboard:
// type JobView struct {
//     ID         int64
//     Name       string
//     FileName   string
//     CreatedAt  string
//     MaxWorkers int
//     Status     string
// }

type JobResultView struct {
	HasResult  bool   // true if there is a row in job_results
	Kind       string // "SAT" or "UNSAT"
	Assignment string // SAT assignment as text, e.g. "{1,-3,10}"
	CreatedAt  string // when the result row was created
}

type ShardView struct {
	ID            int64
	Cube          string
	ShardStatus   string // from job_shards.status
	ResultKind    string // "", "SAT", "UNSAT"
	ResultStatus  string // "", "unverified", "verified"
	Assignment    string // per-shard assignment, if you use it
	DratFileLabel string // DRAT file id as string, if any
	CreatedAt     string
	UpdatedAt     string
}

type jobDetailData struct {
	Job    JobView
	Result JobResultView
	Shards []ShardView
}

// HandleJobDetail shows a single job and all of its shards/results for the current user.
func (s *Server) HandleJobDetail(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.getCurrentUserID(r)
	if !ok || userID == 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	jobIDStr := chi.URLParam(r, "jobID")
	jobID, err := strconv.ParseInt(jobIDStr, 10, 64)
	if err != nil || jobID <= 0 {
		http.Error(w, "invalid job id", http.StatusBadRequest)
		return
	}

	// --- Load job and its job_results row (if any) ---
	var (
		dbJobID    int64
		fileName   string
		status     string
		maxWorkers int
		createdAt  time.Time
		// job_results
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
			http.Error(w, "job not found", http.StatusNotFound)
			return
		}
		log.Printf("job detail: load job: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	jobView := JobView{
		ID:         dbJobID,
		Name:       fileName,
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

	// --- Load shards for this job, with their per-shard results (if any) ---
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
		log.Printf("job detail: load shards: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
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
			log.Printf("job detail: scan shard: %v", err)
			continue
		}

		// Determine per-shard result kind: SAT / UNSAT / ""
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
			Cube:          cubeText, // e.g. "{1,-3,10}"
			ShardStatus:   shStatus,
			ResultKind:    resultKind,
			ResultStatus:  resultStatus,
			Assignment:    assignment,
			DratFileLabel: dratLabel,
			CreatedAt:     shCreated.Format("2006-01-02 15:04"),
			UpdatedAt:     shUpdated.Format("2006-01-02 15:04"),
		})
	}

	data := jobDetailData{
		Job:    jobView,
		Result: resultView,
		Shards: shards,
	}

	s.render(w, "job_detail.html", data)
}
