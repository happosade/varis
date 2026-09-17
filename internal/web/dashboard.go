package web

import (
	"net/http"

	"varis/internal/db"
	"varis/internal/webdav"
)

type dashboardData struct {
	StagedBytes int64
	StagedFiles []stagedFolderGroup
	MediaTypes  []db.MediaType
	Job         *jobView
	MountURL    string
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	staged, err := webdav.DirSize(s.stagingDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stagedFiles, err := s.stagedFileGroups(r)
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
		StagedFiles: stagedFiles,
		MediaTypes:  types,
		Job:         s.currentJobView(),
		MountURL:    "http://" + r.Host + "/webdav/",
	}
	s.render(w, "dashboard", data)
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
