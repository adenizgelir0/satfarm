package web

import (
	"log"
	"net/http"
	"strconv"

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

// Minimal template payload: only job id.
// The template will fetch everything via /api/job/{jobID}.
type jobDetailPageData struct {
	JobID int64
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

	// Ensure the job exists and belongs to the current user.
	var exists bool
	if err := s.DB.QueryRowContext(r.Context(), `
		SELECT EXISTS(
			SELECT 1 FROM jobs WHERE id = $1 AND user_id = $2
		)
	`, jobID, userID).Scan(&exists); err != nil {
		log.Printf("job detail: exists check failed: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !exists {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}

	s.render(w, "job_detail.html", jobDetailPageData{JobID: jobID})
}
