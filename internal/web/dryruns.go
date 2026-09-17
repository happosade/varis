package web

import (
	"net/http"
	"os"
	"path/filepath"

	"varis/internal/db"
)

func (s *Server) dryRunsPage(w http.ResponseWriter, r *http.Request) {
	discs, err := db.ListDryRunDisks(r.Context(), s.pool)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "dryruns", discs)
}

// deleteDryRun removes the ISO file before the catalog rows, so a failure
// never leaves a stale catalog reference to a file that's already gone —
// the inverse ordering of db.DeleteDryRunDisc's own reasoning, since this
// handler's failure mode runs the other way: a stale row pointing at a
// missing file is more confusing than a leftover file with no row, and
// DeleteDryRunDisc is safe to call again either way.
func (s *Server) deleteDryRun(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	diskID := r.FormValue("disk_id")
	isoPath := filepath.Join(s.dryRunDir, diskID+".iso")
	if err := os.Remove(isoPath); err != nil && !os.IsNotExist(err) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := db.DeleteDryRunDisc(r.Context(), s.pool, diskID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/dryruns", http.StatusSeeOther)
}
