package web

import (
	"database/sql"
	"log"
	"net/http"
	"time"
)

type AuthPageData struct {
	Error string
}

func (s *Server) HandleGetSignup(w http.ResponseWriter, r *http.Request) {
	data := AuthPageData{}
	s.render(w, "signup.html", data)
}

func (s *Server) HandlePostSignup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	username := r.FormValue("username")
	email := r.FormValue("email")
	password := r.FormValue("password")
	confirm := r.FormValue("confirm_password")

	var errorMsg string

	switch {
	case username == "" || email == "" || password == "" || confirm == "":
		errorMsg = "Please fill out all fields."
	case password != confirm:
		errorMsg = "Passwords do not match."
	}

	if errorMsg != "" {
		s.jsonError(w, http.StatusBadRequest, errorMsg)
		return
	}

	hash, err := hashPassword(password)
	if err != nil {
		log.Println("hashPassword error:", err)
		s.jsonError(w, http.StatusInternalServerError, "Server error.")
		return
	}

	_, err = s.DB.Exec(
		`INSERT INTO users (username, email, password_hash, created_at)
         VALUES ($1, $2, $3, $4)`,
		username, email, hash, time.Now(),
	)
	if err != nil {
		log.Println("insert user error:", err)
		s.jsonError(w, http.StatusBadRequest, "Could not create account. Username or email may already be taken.")
		return
	}

	s.json(w, http.StatusOK, map[string]any{
		"ok":       true,
		"redirect": "/login",
	})
}

func (s *Server) HandleGetLogin(w http.ResponseWriter, r *http.Request) {
	s.render(w, "login.html", nil)
}

func (s *Server) HandlePostLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	if username == "" || password == "" {
		s.jsonError(w, http.StatusBadRequest, "Username and password are required.")
		return
	}

	var user User
	err := s.DB.QueryRow(
		`SELECT id, username, email, password_hash, created_at
         FROM users
         WHERE username = $1`,
		username,
	).Scan(&user.ID, &user.Username, &user.Email, &user.PasswordHash, &user.CreatedAt)

	if err != nil {
		if err == sql.ErrNoRows {
			s.jsonError(w, http.StatusUnauthorized, "Invalid username or password.")
			return
		}
		log.Println("login query error:", err)
		s.jsonError(w, http.StatusInternalServerError, "Server error.")
		return
	}

	if err := checkPasswordHash(user.PasswordHash, password); err != nil {
		s.jsonError(w, http.StatusUnauthorized, "Invalid username or password.")
		return
	}

	if err := s.setSession(w, user.ID); err != nil {
		log.Println("setSession error:", err)
		s.jsonError(w, http.StatusInternalServerError, "Server error.")
		return
	}

	s.json(w, http.StatusOK, map[string]any{
		"ok":       true,
		"redirect": "/dashboard",
	})
}

func (s *Server) HandleLogout(w http.ResponseWriter, r *http.Request) {
	s.clearSession(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
