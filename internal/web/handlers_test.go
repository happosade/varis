package web

import (
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearch_EmptyQueryReturnsNoRows(t *testing.T) {
	// This only exercises the empty-query short-circuit, which needs no
	// database — everything else in this package is thin enough (a
	// one-line call into db/burn/retrieve, already tested in their own
	// packages) that handler-level tests would mostly re-test those
	// packages through an HTTP wrapper, which isn't worth the added
	// httptest+template plumbing at this project's scale.
	tmpl := template.Must(template.New("search-rows").Parse(`{{define "search-rows"}}{{len .}} rows{{end}}`))
	s := &Server{tmpl: tmpl}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/search?q=", nil)
	s.search(w, r)

	if !strings.Contains(w.Body.String(), "0 rows") {
		t.Errorf("body = %q, want it to contain '0 rows'", w.Body.String())
	}
}
