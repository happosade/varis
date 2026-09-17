package web

import (
	"errors"
	"fmt"
	"net/http"

	"varis/internal/burn"
	"varis/internal/db"
	"varis/internal/retrieve"
)

func (s *Server) library(w http.ResponseWriter, r *http.Request) {
	s.render(w, "library", []db.FileRecord{})
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		s.render(w, "search-rows", []db.FileRecord{})
		return
	}
	files, err := db.SearchFiles(r.Context(), s.pool, q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "search-rows", files)
}

func (s *Server) startRetrieve(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if bj := s.burnMgr.Current(); bj != nil && bj.State != burn.StateDone && bj.State != burn.StateFailed {
		http.Error(w, fmt.Sprintf("drive busy: a burn is in progress (%s)", bj.State), http.StatusConflict)
		return
	}
	fileID := r.FormValue("file_id")
	if err := s.retrieveMgr.Start(r.Context(), fileID); err != nil {
		status := http.StatusConflict
		if errors.Is(err, db.ErrNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	s.renderRetrieveStatus(w)
}

func (s *Server) readDisk(w http.ResponseWriter, r *http.Request) {
	// A NEEDS_RECONSTRUCTION (or FAILED) outcome is still a normal result
	// the status fragment below knows how to render, so an error here
	// doesn't fail the request — it's surfaced via job.Err in the fragment.
	_ = s.retrieveMgr.ReadDisk(r.Context(), r.FormValue("target_path"))
	s.renderRetrieveStatus(w)
}

func (s *Server) startReconstruction(w http.ResponseWriter, r *http.Request) {
	if err := s.retrieveMgr.StartReconstruction(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	s.renderRetrieveStatus(w)
}

func (s *Server) readReconstructionDisc(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	diskID := r.FormValue("disk_id")
	// Same reasoning as readDisk: the outcome (more discs needed, or a
	// failure) is rendered via job.Err in the status fragment, not as an
	// HTTP error.
	_ = s.retrieveMgr.ReadReconstructionDisc(r.Context(), diskID, r.FormValue("target_path"))
	s.renderRetrieveStatus(w)
}

// retrieveView adapts *retrieve.Job for the template, adding the list of
// disc IDs still needed when a reconstruction is in progress — retrieve.Job
// itself has no Remaining field since that list lives on the Manager's
// Reconstructor, not the Job.
type retrieveView struct {
	*retrieve.Job
	Remaining []string
}

func (s *Server) renderRetrieveStatus(w http.ResponseWriter) {
	job := s.retrieveMgr.Current()
	if job == nil {
		s.render(w, "retrieve-status", (*retrieveView)(nil))
		return
	}
	s.render(w, "retrieve-status", &retrieveView{Job: job, Remaining: s.retrieveMgr.Remaining()})
}
