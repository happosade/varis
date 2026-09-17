package web

import (
	"net/http"

	"varis/internal/db"
	"varis/internal/webdav"
)

type dashboardData struct {
	StagedBytes int64
	MediaTypes  []db.MediaType
	Job         *jobView
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	staged, err := webdav.DirSize(s.stagingDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	types, err := db.ListMediaTypes(r.Context(), s.pool)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := dashboardData{
		StagedBytes: staged,
		MediaTypes:  types,
		Job:         s.currentJobView(),
	}
	s.render(w, "dashboard", data)
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
