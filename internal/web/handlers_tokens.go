package web

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"strconv"
	"time"
)

// generateTokenSecret returns a 32-byte random hex string.
func generateTokenSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (s *Server) HandleCreateToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	userID, ok := s.getCurrentUserID(r)
	if !ok || userID == 0 {
		s.jsonError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if err := r.ParseForm(); err != nil {
		s.jsonError(w, http.StatusBadRequest, "invalid form")
		return
	}

	label := r.Form.Get("label")
	if label == "" {
		s.jsonError(w, http.StatusBadRequest, "invalid label")
		return
	}

	secret, err := generateTokenSecret()
	if err != nil {
		log.Println("create token: generate secret:", err)
		s.jsonError(w, http.StatusInternalServerError, "internal error")
		return
	}

	var (
		tokenID   int64
		createdAt time.Time
	)

	err = s.DB.QueryRow(`
        INSERT INTO api_tokens (user_id, label, secret)
        VALUES ($1, $2, $3)
        RETURNING id, created_at
    `, userID, label, secret).Scan(&tokenID, &createdAt)
	if err != nil {
		log.Println("create token: insert:", err)
		s.jsonError(w, http.StatusInternalServerError, "could not create token")
		return
	}

	s.json(w, http.StatusOK, map[string]any{
		"ok": true,
		"token": map[string]any{
			"id":         tokenID,
			"label":      label,
			"created_at": createdAt.Format("2006-01-02 15:04"),
			"revoked":    false,
		},
		"secret": secret,
	})
}

func (s *Server) HandleRevokeToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	userID, ok := s.getCurrentUserID(r)
	if !ok || userID == 0 {
		s.jsonError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if err := r.ParseForm(); err != nil {
		s.jsonError(w, http.StatusBadRequest, "invalid form")
		return
	}

	idStr := r.Form.Get("token_id")
	if idStr == "" {
		s.jsonError(w, http.StatusBadRequest, "missing token_id")
		return
	}

	tokenID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		s.jsonError(w, http.StatusBadRequest, "invalid token_id")
		return
	}

	res, err := s.DB.Exec(`
        UPDATE api_tokens
        SET revoked = TRUE
        WHERE id = $1 AND user_id = $2
    `, tokenID, userID)
	if err != nil {
		log.Println("revoke token: update:", err)
		s.jsonError(w, http.StatusInternalServerError, "could not revoke token")
		return
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		s.jsonError(w, http.StatusNotFound, "token not found")
		return
	}

	s.json(w, http.StatusOK, map[string]any{
		"ok":       true,
		"token_id": tokenID,
	})
}
