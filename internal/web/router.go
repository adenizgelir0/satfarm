package web

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func FileServer(r chi.Router, path string, root http.FileSystem) {
	if strings.ContainsAny(path, "{}*") {
		panic("FileServer does not permit URL parameters.")
	}

	path = strings.TrimSuffix(path, "/")
	r.Get(path, http.RedirectHandler(path+"/", http.StatusMovedPermanently).ServeHTTP)
	r.Get(path+"/*", http.StripPrefix(path, http.FileServer(root)).ServeHTTP)
}

func NewRouter(s *Server) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	FileServer(r, "/static", http.Dir("./static"))

	r.Get("/", s.HandleLanding)

	r.Get("/login", s.HandleGetLogin)
	r.Post("/login", s.HandlePostLogin)

	r.Get("/signup", s.HandleGetSignup)
	r.Post("/signup", s.HandlePostSignup)

	r.Get("/logout", s.HandleLogout)

	r.Get("/dashboard", s.HandleDashboard)
	r.Post("/tokens/new", s.HandleCreateToken)
	r.Post("/tokens/revoke", s.HandleRevokeToken)
	r.Post("/deposit", s.HandleDeposit)
	r.Post("/withdraw", s.HandleWithdraw)

	r.Post("/jobs/new", s.HandleCreateJob)
	r.Get("/transactions", s.HandleTransactions)
	r.Get("/jobs/{jobID}", s.HandleJobDetail)

	r.Post("/worker/drat", s.HandleWorkerDratUpload)
	r.Get("/worker/cnf/{jobID}", s.HandleWorkerCnfDownload)
	return r
}
