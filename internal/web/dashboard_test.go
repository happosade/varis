package web

import (
	"html/template"
	"strings"
	"testing"
)

// TestDashboardTemplate_ShowsMountURL executes the real embedded
// "dashboard" template with a minimal dashboardData, proving the mount
// blurb actually renders the URL the handler computes — not just that
// the template parses.
func TestDashboardTemplate_ShowsMountURL(t *testing.T) {
	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		t.Fatalf("parsing embedded templates: %v", err)
	}
	data := dashboardData{MountURL: "http://example.test:8080/webdav/"}
	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "dashboard", data); err != nil {
		t.Fatalf("executing dashboard: %v", err)
	}
	if !strings.Contains(buf.String(), "http://example.test:8080/webdav/") {
		t.Errorf("expected the mount URL in output, got:\n%s", buf.String())
	}
}
