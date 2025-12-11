package web

import (
	"database/sql"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	satgrpc "satfarm/internal/grpc"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type Server struct {
	DB        *sql.DB
	Tpl       *template.Template
	Sessions  *SessionStore
	Scheduler satgrpc.JobScheduler // 👈 new
}

func NewServer(db *sql.DB, tpl *template.Template, sched satgrpc.JobScheduler) *Server {

	return &Server{
		DB:        db,
		Sessions:  NewSessionStore(),
		Tpl:       tpl,
		Scheduler: sched,
	}
}

type User struct {
	ID           int64
	Username     string
	Email        string
	PasswordHash string
	CreatedAt    time.Time
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.Tpl.ExecuteTemplate(w, name, data); err != nil {
		log.Println("template error:", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func currentYear() int {
	return time.Now().Year()
}

func hashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(b), err
}

func checkPasswordHash(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}

func (s *Server) json(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("json encode error: %v", err)
	}
}

func (s *Server) jsonError(w http.ResponseWriter, status int, msg string) {
	s.json(w, status, map[string]any{
		"ok":    false,
		"error": msg,
	})
}
