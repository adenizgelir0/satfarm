package web

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"time"
)

// where CNF uploads live inside the container
const UploadBaseDir = "/app/uploads"

type TokenView struct {
	ID        int64  `json:"id"`
	Label     string `json:"label"`
	CreatedAt string `json:"created_at"`
	Revoked   bool   `json:"revoked"`
}

type JobView struct {
	ID         int64
	Name       string
	FileName   string
	CreatedAt  string
	MaxWorkers int
	Status     string
}

type TransactionView struct {
	CreatedAt string `json:"created_at"`
	AmountSat int64  `json:"amount_sat"`
	Reason    string `json:"reason"`
}

type dashboardData struct {
	UserName     string
	Balance      string // satoshi as string
	Tokens       []TokenView
	Transactions []TransactionView
	Jobs         []JobView
}

func (s *Server) HandleDashboard(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.getCurrentUserID(r)
	if !ok || userID == 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// ---- Load user info ----
	var (
		username    string
		balanceSatc int64
	)

	err := s.DB.QueryRow(`
        SELECT username, balance_satc
        FROM users
        WHERE id = $1
    `, userID).Scan(&username, &balanceSatc)
	if err != nil {
		log.Println("dashboard: invalid session or user:", err)
		s.clearSession(w, r)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Show raw integer satoshi
	balanceStr := fmt.Sprintf("%d", balanceSatc)

	// -------------------------------------------------------
	// Load tokens
	// -------------------------------------------------------
	rows, err := s.DB.Query(`
        SELECT id, label, created_at, revoked
        FROM api_tokens
        WHERE user_id = $1
        ORDER BY created_at DESC
        LIMIT 50
    `, userID)
	if err != nil && err != sql.ErrNoRows {
		log.Println("dashboard: load tokens:", err)
	}

	var tokens []TokenView
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var (
				id        int64
				label     string
				createdAt time.Time
				revoked   bool
			)
			if err := rows.Scan(&id, &label, &createdAt, &revoked); err != nil {
				log.Println("dashboard: scan token:", err)
				continue
			}

			tokens = append(tokens, TokenView{
				ID:        id,
				Label:     label,
				CreatedAt: createdAt.Format("2006-01-02 15:04"),
				Revoked:   revoked,
			})
		}
	}

	// -------------------------------------------------------
	// Load latest transactions
	// NOTE: DB column name is amount_satc, but JSON field is amount_sat
	// -------------------------------------------------------
	txRows, err := s.DB.Query(`
        SELECT amount_satc, reason, created_at
        FROM transactions
        WHERE user_id = $1
        ORDER BY created_at DESC
        LIMIT 20
    `, userID)
	if err != nil && err != sql.ErrNoRows {
		log.Println("dashboard: load transactions:", err)
	}

	var txViews []TransactionView
	if txRows != nil {
		defer txRows.Close()
		for txRows.Next() {
			var (
				amountSatc int64
				reason     string
				createdAt  time.Time
			)
			if err := txRows.Scan(&amountSatc, &reason, &createdAt); err != nil {
				log.Println("dashboard: scan tx:", err)
				continue
			}

			txViews = append(txViews, TransactionView{
				CreatedAt: createdAt.Format("2006-01-02 15:04"),
				AmountSat: amountSatc,
				Reason:    reason,
			})
		}
	}

	// -------------------------------------------------------
	// Load latest jobs for this user
	// -------------------------------------------------------
	jobRows, err := s.DB.Query(`
        SELECT j.id, cf.original_name, j.created_at, j.max_workers, j.status
        FROM jobs j
        JOIN cnf_files cf ON j.cnf_file_id = cf.id
        WHERE j.user_id = $1
        ORDER BY j.created_at DESC
        LIMIT 50
    `, userID)
	if err != nil && err != sql.ErrNoRows {
		log.Println("dashboard: load jobs:", err)
	}

	var jobViews []JobView
	if jobRows != nil {
		defer jobRows.Close()
		for jobRows.Next() {
			var (
				id         int64
				fileName   string
				createdAt  time.Time
				maxWorkers int
				status     string
			)

			if err := jobRows.Scan(&id, &fileName, &createdAt, &maxWorkers, &status); err != nil {
				log.Println("dashboard: scan job:", err)
				continue
			}

			jobViews = append(jobViews, JobView{
				ID:         id,
				FileName:   fileName,
				Name:       fileName,
				CreatedAt:  createdAt.Format("2006-01-02 15:04"),
				MaxWorkers: maxWorkers,
				Status:     status,
			})
		}
	}

	// -------------------------------------------------------
	// Render template
	// -------------------------------------------------------
	data := dashboardData{
		UserName:     username,
		Balance:      balanceStr,
		Tokens:       tokens,
		Transactions: txViews,
		Jobs:         jobViews,
	}

	s.render(w, "dashboard.html", data)
}
