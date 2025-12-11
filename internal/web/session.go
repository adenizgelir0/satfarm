package web

import (
	cryptoRand "crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
)

type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]int64
}

func NewSessionStore() *SessionStore {
	return &SessionStore{
		sessions: make(map[string]int64),
	}
}

func (s *SessionStore) Set(id string, userID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[id] = userID
}

func (s *SessionStore) Get(id string) (int64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	uid, ok := s.sessions[id]
	return uid, ok
}

func (s *SessionStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

func generateSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := cryptoRand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *Server) setSession(w http.ResponseWriter, userID int64) error {
	id, err := generateSessionID()
	if err != nil {
		return err
	}
	s.Sessions.Set(id, userID)

	http.SetCookie(w, &http.Cookie{
		Name:     "session_id",
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	return nil
}

func (s *Server) getCurrentUserID(r *http.Request) (int64, bool) {
	c, err := r.Cookie("session_id")
	if err != nil || c.Value == "" {
		return 0, false
	}
	return s.Sessions.Get(c.Value)
}

func (s *Server) clearSession(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("session_id")
	if err == nil && c.Value != "" {
		s.Sessions.Delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "session_id",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
