package web

import (
	"net/http"
	"strconv"

	"varis/internal/db"
)

func (s *Server) configPage(w http.ResponseWriter, r *http.Request) {
	types, err := db.ListMediaTypes(r.Context(), s.pool)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "config", types)
}

func (s *Server) addMediaType(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	capacity, err := strconv.ParseInt(r.FormValue("capacity_bytes"), 10, 64)
	if err != nil || capacity <= 0 {
		http.Error(w, "invalid capacity_bytes", http.StatusBadRequest)
		return
	}
	mt := db.MediaType{Name: r.FormValue("name"), CapacityBytes: capacity}
	if err := db.AddMediaType(r.Context(), s.pool, mt); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/config", http.StatusSeeOther)
}
