package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// changeUserBalance atomically updates balance_satc and inserts a transaction row.
func (s *Server) changeUserBalance(ctx context.Context, userID int64, deltaSatc int64, reason string, meta any) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// debit with safety
	if deltaSatc < 0 {
		res, err := tx.ExecContext(ctx, `
            UPDATE users
            SET balance_satc = balance_satc + $1
            WHERE id = $2 AND balance_satc >= $3
        `, deltaSatc, userID, -deltaSatc)
		if err != nil {
			return fmt.Errorf("debit: %w", err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return fmt.Errorf("insufficient balance")
		}
	} else {
		// credit
		_, err := tx.ExecContext(ctx, `
            UPDATE users
            SET balance_satc = balance_satc + $1
            WHERE id = $2
        `, deltaSatc, userID)
		if err != nil {
			return fmt.Errorf("credit: %w", err)
		}
	}

	// meta stored as JSONB
	var metaJSON any
	if meta != nil {
		b, err := json.Marshal(meta)
		if err != nil {
			return fmt.Errorf("marshal meta: %w", err)
		}
		metaJSON = b
	}

	_, err = tx.ExecContext(ctx, `
        INSERT INTO transactions (user_id, amount_satc, reason, meta)
        VALUES ($1, $2, $3, $4)
    `, userID, deltaSatc, reason, metaJSON)
	if err != nil {
		return fmt.Errorf("insert txn: %w", err)
	}

	return tx.Commit()
}

func (s *Server) HandleDeposit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	userID, ok := s.getCurrentUserID(r)
	if !ok {
		s.jsonError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	delta := int64(1000) // example: +1000 satoshi

	if err := s.changeUserBalance(r.Context(), userID, delta, "deposit", nil); err != nil {
		s.jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	var newBalance int64
	if err := s.DB.QueryRowContext(r.Context(), `
        SELECT balance_satc
        FROM users
        WHERE id = $1
    `, userID).Scan(&newBalance); err != nil {
		s.jsonError(w, http.StatusInternalServerError, "failed to load balance")
		return
	}

	s.json(w, http.StatusOK, map[string]any{
		"ok":          true,
		"delta":       delta,
		"balance":     newBalance,
		"balance_str": fmt.Sprintf("%d", newBalance),
	})
}

func (s *Server) HandleWithdraw(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	userID, ok := s.getCurrentUserID(r)
	if !ok {
		s.jsonError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	delta := int64(-1000) // example: -1000 satoshi

	if err := s.changeUserBalance(r.Context(), userID, delta, "withdraw", nil); err != nil {
		s.jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	var newBalance int64
	if err := s.DB.QueryRowContext(r.Context(), `
        SELECT balance_satc
        FROM users
        WHERE id = $1
    `, userID).Scan(&newBalance); err != nil {
		s.jsonError(w, http.StatusInternalServerError, "failed to load balance")
		return
	}

	s.json(w, http.StatusOK, map[string]any{
		"ok":          true,
		"delta":       delta,
		"balance":     newBalance,
		"balance_str": fmt.Sprintf("%d", newBalance),
	})
}

// HandleTransactions returns the latest 10 transactions for the current user as JSON.
func (s *Server) HandleTransactions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	userID, ok := s.getCurrentUserID(r)
	if !ok {
		s.jsonError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	ctx := r.Context()

	// 🔹 Load last 10 transactions
	rows, err := s.DB.QueryContext(ctx, `
        SELECT amount_satc, reason, created_at
        FROM transactions
        WHERE user_id = $1
        ORDER BY created_at DESC
        LIMIT 10
    `, userID)
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, "failed to load transactions")
		return
	}
	defer rows.Close()

	var txs []TransactionView
	for rows.Next() {
		var (
			amountSatc int64
			reason     string
			createdAt  time.Time
		)
		if err := rows.Scan(&amountSatc, &reason, &createdAt); err != nil {
			s.jsonError(w, http.StatusInternalServerError, "failed to scan transaction")
			return
		}

		txs = append(txs, TransactionView{
			CreatedAt: createdAt.Format("2006-01-02 15:04"),
			AmountSat: amountSatc,
			Reason:    reason,
		})
	}
	if err := rows.Err(); err != nil {
		s.jsonError(w, http.StatusInternalServerError, "failed to load transactions")
		return
	}

	// 🔹 Load current balance
	var balanceSatc int64
	if err := s.DB.QueryRowContext(ctx,
		`SELECT balance_satc FROM users WHERE id = $1`,
		userID,
	).Scan(&balanceSatc); err != nil {
		s.jsonError(w, http.StatusInternalServerError, "failed to load balance")
		return
	}

	s.json(w, http.StatusOK, map[string]any{
		"ok":           true,
		"transactions": txs,
		"balance_satc": balanceSatc,
		"balance_str":  fmt.Sprintf("%d", balanceSatc),
	})
}
