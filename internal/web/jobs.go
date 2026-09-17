package web

import (
	"fmt"
	"net/http"
	"strconv"

	"varis/internal/burn"
	"varis/internal/db"
	"varis/internal/retrieve"
)

// jobView adapts *burn.Job to what jobs.html needs (1-based disc index,
// the current disc's ID for the "printable cover" link once DONE).
type jobView struct {
	State         burn.State
	Err           error
	CurrentIndex1 int
	TotalDiscs    int
	CurrentDiskID string
	DryRun        bool
}

func (s *Server) currentJobView() *jobView {
	job := s.burnMgr.Current()
	if job == nil {
		return nil
	}
	return jobViewFromJob(job)
}

// jobViewFromJob is split out from currentJobView so the index-clamping
// logic (the one non-trivial bit of this adapter) can be unit-tested
// directly against a *burn.Job, without needing a live *burn.Manager.
func jobViewFromJob(job *burn.Job) *jobView {
	idx := job.CurrentIndex
	if idx >= len(job.Plans) {
		idx = len(job.Plans) - 1
	}
	return &jobView{
		State:         job.State,
		Err:           job.Err,
		CurrentIndex1: job.CurrentIndex + 1,
		TotalDiscs:    len(job.Plans),
		CurrentDiskID: job.Plans[idx].DiskID,
		DryRun:        job.Options.DryRun,
	}
}

func (s *Server) startBurn(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if rj := s.retrieveMgr.Current(); rj != nil && rj.State != retrieve.StateDone && rj.State != retrieve.StateFailed {
		fmt.Fprintf(w, `<p class="error">drive busy: a retrieval is in progress (%s)</p>`, rj.State)
		return
	}
	mediaType := r.FormValue("media_type")
	capacity, err := db.GetMediaTypeCapacity(r.Context(), s.pool, mediaType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	parityPercent, err := strconv.Atoi(r.FormValue("parity_percent"))
	if err != nil || parityPercent < 5 || parityPercent > 50 {
		http.Error(w, "parity_percent must be an integer between 5 and 50", http.StatusBadRequest)
		return
	}
	groupSize, _ := strconv.Atoi(r.FormValue("group_size"))

	opts := burn.Options{
		MediaType:       mediaType,
		CapacityBytes:   capacity,
		ParityPercent:   parityPercent,
		Compress:        r.FormValue("compress") == "on",
		CrossDiscParity: r.FormValue("cross_disc_parity") == "on",
		GroupSize:       groupSize,
		DryRun:          r.FormValue("dry_run") == "on",
	}
	if err := s.burnMgr.Start(r.Context(), opts); err != nil {
		s.render(w, "error-fragment", err.Error())
		return
	}
	s.render(w, "jobs-fragment", s.currentJobView())
}

func (s *Server) jobsFragment(w http.ResponseWriter, r *http.Request) {
	s.render(w, "jobs-fragment", s.currentJobView())
}

func (s *Server) continueDisc(w http.ResponseWriter, r *http.Request) {
	if err := s.burnMgr.ContinueNextDisc(r.Context()); err != nil {
		s.render(w, "error-fragment", err.Error())
		return
	}
	s.render(w, "jobs-fragment", s.currentJobView())
}

func (s *Server) retryDisc(w http.ResponseWriter, r *http.Request) {
	if err := s.burnMgr.Retry(r.Context()); err != nil {
		s.render(w, "error-fragment", err.Error())
		return
	}
	s.render(w, "jobs-fragment", s.currentJobView())
}
