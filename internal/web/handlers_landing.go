package web

import "net/http"

type LandingPageData struct {
	Year int
}

func (s *Server) HandleLanding(w http.ResponseWriter, r *http.Request) {
	data := LandingPageData{
		Year: currentYear(),
	}
	s.render(w, "landing.html", data)
}
