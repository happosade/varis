package web

import (
	"net/http"
	"sort"
	"strings"

	"varis/internal/binpack"
	"varis/internal/db"
)

// stagedFileRow is one staged file plus whatever metadata it currently
// carries in staged_metadata, for the dashboard listing.
type stagedFileRow struct {
	Path        string
	Size        int64
	Tags        []string
	Description string
}

// stagedFolderGroup is one top-level folder's worth of staged files (or,
// when Name is "", the files sitting directly in the staging root).
type stagedFolderGroup struct {
	Name  string
	Files []stagedFileRow
}

// groupStagedFiles groups files by their top-level folder (the first path
// segment), joining in tags/description from meta by path. The root group
// (files with no folder) always sorts first; folder groups after it sort
// alphabetically.
func groupStagedFiles(files []binpack.FileInfo, meta map[string]db.StagedMetadata) []stagedFolderGroup {
	groups := map[string]*stagedFolderGroup{}
	var order []string
	for _, f := range files {
		name := ""
		if idx := strings.IndexByte(f.Path, '/'); idx >= 0 {
			name = f.Path[:idx]
		}
		g, ok := groups[name]
		if !ok {
			g = &stagedFolderGroup{Name: name}
			groups[name] = g
			order = append(order, name)
		}
		m := meta[f.Path]
		g.Files = append(g.Files, stagedFileRow{Path: f.Path, Size: f.Size, Tags: m.Tags, Description: m.Description})
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i] == "" {
			return true
		}
		if order[j] == "" {
			return false
		}
		return order[i] < order[j]
	})
	out := make([]stagedFolderGroup, 0, len(order))
	for _, name := range order {
		out = append(out, *groups[name])
	}
	return out
}

// resolveSelectedPaths expands a form's checked selections into the exact
// set of staging-relative paths to apply metadata to. A selection ending
// in "/" is a folder header checkbox: it expands to every currently
// -staged file whose path starts with that prefix (a fresh ScanStaging, so
// a folder checked a moment ago before some of its files were burned just
// resolves to whatever's still there). Anything else is a single file's
// own path, taken as-is.
func resolveSelectedPaths(stagingDir string, selected []string) ([]string, error) {
	files, err := binpack.ScanStaging(stagingDir)
	if err != nil {
		return nil, err
	}
	var prefixes []string
	set := map[string]bool{}
	for _, sel := range selected {
		if strings.HasSuffix(sel, "/") {
			prefixes = append(prefixes, sel)
		} else {
			set[sel] = true
		}
	}
	for _, f := range files {
		for _, prefix := range prefixes {
			if strings.HasPrefix(f.Path, prefix) {
				set[f.Path] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	return out, nil
}

// stagedFileGroups scans staging and joins in current metadata, for both
// the dashboard's initial render and applyMetadata's re-rendered fragment.
func (s *Server) stagedFileGroups(r *http.Request) ([]stagedFolderGroup, error) {
	files, err := binpack.ScanStaging(s.stagingDir)
	if err != nil {
		return nil, err
	}
	meta, err := db.ListStagedMetadata(r.Context(), s.pool)
	if err != nil {
		return nil, err
	}
	return groupStagedFiles(files, meta), nil
}

func (s *Server) renderStagingFiles(w http.ResponseWriter, r *http.Request) {
	groups, err := s.stagedFileGroups(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "staging-files-fragment", groups)
}

// applyMetadata replaces tags/description for every selected staged file
// (individual paths and/or folder-prefix selections) with the submitted
// values, then re-renders the listing so the change is visible immediately.
func (s *Server) applyMetadata(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	paths, err := resolveSelectedPaths(s.stagingDir, r.Form["path"])
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tags := parseTags(r.FormValue("tags"))
	description := r.FormValue("description")
	for _, p := range paths {
		if err := db.UpsertStagedMetadata(r.Context(), s.pool, p, tags, description); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	s.renderStagingFiles(w, r)
}

// parseTags splits a comma-separated tags input into a trimmed,
// non-empty-entries-only slice — "family, ,2019" becomes [family 2019].
func parseTags(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
