package web

import (
	"fmt"
	"net/http"
	"strconv"

	"varis/internal/burn"
	"varis/internal/db"
)

// jobView adapts *burn.Job to what jobs.html needs (1-based disc index,
// the current disc's ID for the "printable cover" link once DONE).
type jobView struct {
	State         burn.State
	Err           error
	CurrentIndex1 int
	TotalDiscs    int
	CurrentDiskID string
}

func (s *Server) currentJobView() *jobView {
	job := s.burnMgr.Current()
	if job == nil {
		return nil
	}
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
	}
}

func (s *Server) startBurn(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	mediaType := r.FormValue("media_type")
	capacity, err := db.GetMediaTypeCapacity(r.Context(), s.pool, mediaType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	parityPercent, _ := strconv.Atoi(r.FormValue("parity_percent"))
	groupSize, _ := strconv.Atoi(r.FormValue("group_size"))

	opts := burn.Options{
		MediaType:       mediaType,
		CapacityBytes:   capacity,
		ParityPercent:   parityPercent,
		Compress:        r.FormValue("compress") == "on",
		CrossDiscParity: r.FormValue("cross_disc_parity") == "on",
		GroupSize:       groupSize,
	}
	if err := s.burnMgr.Start(r.Context(), opts); err != nil {
		fmt.Fprintf(w, `<p class="error">%s</p>`, err)
		return
	}
	s.render(w, "jobs-fragment", s.currentJobView())
}

func (s *Server) jobsFragment(w http.ResponseWriter, r *http.Request) {
	s.render(w, "jobs-fragment", s.currentJobView())
}

func (s *Server) continueDisc(w http.ResponseWriter, r *http.Request) {
	if err := s.burnMgr.ContinueNextDisc(r.Context()); err != nil {
		fmt.Fprintf(w, `<p class="error">%s</p>`, err)
		return
	}
	s.render(w, "jobs-fragment", s.currentJobView())
}

func (s *Server) retryDisc(w http.ResponseWriter, r *http.Request) {
	if err := s.burnMgr.Retry(r.Context()); err != nil {
		fmt.Fprintf(w, `<p class="error">%s</p>`, err)
		return
	}
	s.render(w, "jobs-fragment", s.currentJobView())
}
