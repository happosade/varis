# Unified WebDAV Mount Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the three separate WebDAV mounts (staging, retrieved, dry-run) with one root mount exposing all three as subfolders, show the operator what URL to mount right on the Dashboard, and make the Dashboard's staged-files listing collapsible per folder.

**Architecture:** A new `CombinedFileSystem` in `internal/webdav` implements `golang.org/x/net/webdav.FileSystem` by dispatching on a request path's first segment (`staging`/`retrieved`/`dryrun`) to the matching real directory, synthesizing just enough of a root directory listing for a WebDAV client to see all three as subfolders. `cmd/archive-core/main.go` mounts this once instead of mounting three separate handlers. The Dashboard handler computes the mount URL from the incoming request's Host header and displays it; the existing staged-files template swaps `<fieldset>`/`<legend>` for native `<details>`/`<summary>`.

**Tech Stack:** Go 1.26+ stdlib, `golang.org/x/net/webdav` (already a dependency, used today via `internal/webdav.Handler`), `html/template` + HTMX (all already in use — no new dependencies).

**Depends on:** the existing full implementation (all prior plans). Additive/replacing throughout — no schema or DB changes.

---

### Task 1: `internal/webdav` — `CombinedFileSystem` and `CombinedHandler`

**Files:**
- Create: `internal/webdav/combined.go`
- Create: `internal/webdav/combined_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/webdav/combined_test.go`:

```go
package webdav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCombinedFileSystem_RootListsCategories(t *testing.T) {
	fs := NewCombinedFileSystem(map[string]string{
		"staging":   t.TempDir(),
		"retrieved": t.TempDir(),
		"dryrun":    t.TempDir(),
	})

	f, err := fs.OpenFile(context.Background(), "/", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile(/): %v", err)
	}
	defer f.Close()

	entries, err := f.Readdir(0)
	if err != nil {
		t.Fatalf("Readdir: %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			t.Errorf("entry %q: IsDir() = false, want true", e.Name())
		}
		names[e.Name()] = true
	}
	for _, want := range []string{"staging", "retrieved", "dryrun"} {
		if !names[want] {
			t.Errorf("root listing = %v, want it to include %q", names, want)
		}
	}
}

func TestCombinedHandler_PutGetDeleteRoundTripsWithinEachCategory(t *testing.T) {
	dirs := map[string]string{
		"staging":   t.TempDir(),
		"retrieved": t.TempDir(),
		"dryrun":    t.TempDir(),
	}
	h := CombinedHandler(dirs)

	for category, dir := range dirs {
		t.Run(category, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/"+category+"/hello.txt", strings.NewReader("hi"))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusCreated {
				t.Fatalf("PUT status = %d, want 201", w.Code)
			}

			req = httptest.NewRequest(http.MethodGet, "/"+category+"/hello.txt", nil)
			w = httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusOK || w.Body.String() != "hi" {
				t.Fatalf("GET = %d %q, want 200 \"hi\"", w.Code, w.Body.String())
			}

			if _, err := os.Stat(filepath.Join(dir, "hello.txt")); err != nil {
				t.Errorf("expected hello.txt on disk at %s: %v", dir, err)
			}

			req = httptest.NewRequest(http.MethodDelete, "/"+category+"/hello.txt", nil)
			w = httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusNoContent {
				t.Fatalf("DELETE status = %d, want 204", w.Code)
			}
		})
	}
}

func TestCombinedHandler_UnknownCategoryNotFoundOnGet(t *testing.T) {
	h := CombinedHandler(map[string]string{"staging": t.TempDir()})

	req := httptest.NewRequest(http.MethodGet, "/nope/x.txt", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /nope/x.txt status = %d, want 404", w.Code)
	}
}

func TestCombinedHandler_RejectsCreatingATopLevelCategory(t *testing.T) {
	h := CombinedHandler(map[string]string{"staging": t.TempDir()})

	req := httptest.NewRequest(http.MethodPut, "/newcategory", strings.NewReader("hi"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	// "newcategory" isn't a known category, so the underlying webdav.Handler
	// treats this like writing into a nonexistent parent directory: 409
	// Conflict, not 201 Created. The three categories are fixed; a client
	// can never create a fourth by PUTting at the root.
	if w.Code != http.StatusConflict {
		t.Errorf("PUT /newcategory status = %d, want 409", w.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/webdav/... -v`
Expected: FAIL to compile — `NewCombinedFileSystem` and `CombinedHandler` don't exist yet.

- [ ] **Step 3: Implement `CombinedFileSystem` and `CombinedHandler`**

Create `internal/webdav/combined.go`:

```go
package webdav

import (
	"context"
	"io"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/webdav"
)

// CombinedFileSystem composes several webdav.Dir filesystems, keyed by
// their top-level path segment, into a single virtual root — so a WebDAV
// client can mount one URL and see each category (staging, retrieved,
// dryrun) as a subfolder, instead of needing a separate mount per
// category.
type CombinedFileSystem map[string]webdav.Dir

// NewCombinedFileSystem builds a CombinedFileSystem from category name
// (the subfolder a client will see) to the real directory it serves.
func NewCombinedFileSystem(dirs map[string]string) CombinedFileSystem {
	fs := make(CombinedFileSystem, len(dirs))
	for name, dir := range dirs {
		fs[name] = webdav.Dir(dir)
	}
	return fs
}

// CombinedHandler serves several directories under one WebDAV root, each
// as a subfolder named by its key in dirs.
func CombinedHandler(dirs map[string]string) http.Handler {
	return &webdav.Handler{
		Prefix:     "/",
		FileSystem: NewCombinedFileSystem(dirs),
		LockSystem: webdav.NewMemLS(),
	}
}

// split breaks a WebDAV path into its category (first segment) and the
// remainder (always slash-prefixed — "/" if nothing follows).
func (fs CombinedFileSystem) split(name string) (category, rest string, isRoot bool) {
	trimmed := strings.TrimPrefix(path.Clean("/"+name), "/")
	if trimmed == "" {
		return "", "", true
	}
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) == 2 {
		return parts[0], "/" + parts[1], false
	}
	return parts[0], "/", false
}

func (fs CombinedFileSystem) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	category, rest, isRoot := fs.split(name)
	if isRoot {
		return os.ErrPermission
	}
	dir, ok := fs[category]
	if !ok {
		return os.ErrNotExist
	}
	return dir.Mkdir(ctx, rest, perm)
}

func (fs CombinedFileSystem) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	category, rest, isRoot := fs.split(name)
	if isRoot {
		// The three categories are fixed, so the root only ever supports a
		// read-only directory listing — no writing/creating at this level.
		if flag != os.O_RDONLY {
			return nil, os.ErrPermission
		}
		return fs.rootFile(), nil
	}
	dir, ok := fs[category]
	if !ok {
		return nil, os.ErrNotExist
	}
	return dir.OpenFile(ctx, rest, flag, perm)
}

func (fs CombinedFileSystem) RemoveAll(ctx context.Context, name string) error {
	category, rest, isRoot := fs.split(name)
	if isRoot {
		return os.ErrPermission
	}
	dir, ok := fs[category]
	if !ok {
		return os.ErrNotExist
	}
	return dir.RemoveAll(ctx, rest)
}

func (fs CombinedFileSystem) Rename(ctx context.Context, oldName, newName string) error {
	oldCategory, oldRest, oldIsRoot := fs.split(oldName)
	newCategory, newRest, newIsRoot := fs.split(newName)
	if oldIsRoot || newIsRoot {
		return os.ErrPermission
	}
	// Moving a file between categories would mean copying across separate,
	// independently-configured real directories rather than a single
	// os.Rename — not a supported operation.
	if oldCategory != newCategory {
		return os.ErrPermission
	}
	dir, ok := fs[oldCategory]
	if !ok {
		return os.ErrNotExist
	}
	return dir.Rename(ctx, oldRest, newRest)
}

func (fs CombinedFileSystem) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	category, rest, isRoot := fs.split(name)
	if isRoot {
		return dirInfo{name: "/"}, nil
	}
	dir, ok := fs[category]
	if !ok {
		return nil, os.ErrNotExist
	}
	return dir.Stat(ctx, rest)
}

// rootFile lists this CombinedFileSystem's categories as the root
// directory's children.
func (fs CombinedFileSystem) rootFile() webdav.File {
	names := make([]string, 0, len(fs))
	for name := range fs {
		names = append(names, name)
	}
	sort.Strings(names)
	return &rootDir{names: names}
}

// dirInfo is a synthetic os.FileInfo for a directory that doesn't exist
// on any real filesystem — the combined root itself, or one of its
// category subfolders as seen in the root's own listing.
type dirInfo struct{ name string }

func (d dirInfo) Name() string     { return d.name }
func (dirInfo) Size() int64        { return 0 }
func (dirInfo) Mode() os.FileMode  { return os.ModeDir | 0o555 }
func (dirInfo) ModTime() time.Time { return time.Time{} }
func (dirInfo) IsDir() bool        { return true }
func (dirInfo) Sys() any           { return nil }

// rootDir is the webdav.File handle for the synthetic root: it only
// supports listing its children and Stat-ing itself. Read/Write/Seek on
// a directory handle are invalid, mirroring how *os.File behaves for the
// same operations against a real directory.
type rootDir struct {
	names  []string
	offset int
}

func (r *rootDir) Close() error                  { return nil }
func (r *rootDir) Read([]byte) (int, error)       { return 0, os.ErrInvalid }
func (r *rootDir) Write([]byte) (int, error)      { return 0, os.ErrInvalid }
func (r *rootDir) Seek(int64, int) (int64, error) { return 0, os.ErrInvalid }
func (r *rootDir) Stat() (os.FileInfo, error)     { return dirInfo{name: "/"}, nil }

func (r *rootDir) Readdir(count int) ([]os.FileInfo, error) {
	if count <= 0 {
		infos := make([]os.FileInfo, len(r.names))
		for i, name := range r.names {
			infos[i] = dirInfo{name: name}
		}
		return infos, nil
	}
	if r.offset >= len(r.names) {
		return nil, io.EOF
	}
	end := r.offset + count
	if end > len(r.names) {
		end = len(r.names)
	}
	infos := make([]os.FileInfo, 0, end-r.offset)
	for _, name := range r.names[r.offset:end] {
		infos = append(infos, dirInfo{name: name})
	}
	r.offset = end
	return infos, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `gofmt -l internal/webdav && go build ./... && go vet ./... && go test ./internal/webdav/... -race -v`
Expected: `gofmt -l` prints nothing (already formatted); tests PASS — all four
new tests plus the existing `TestHandler_PutAndGet`/`TestDirSize`.

- [ ] **Step 5: Commit**

```bash
git add internal/webdav/combined.go internal/webdav/combined_test.go
git commit -m "$(cat <<'EOF'
Add CombinedFileSystem: serve staging/retrieved/dryrun under one WebDAV root

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 2: Wire `main.go` — one `/webdav/` mount instead of three

**Files:**
- Modify: `cmd/archive-core/main.go`

- [ ] **Step 1: Replace the three mounts with one**

Replace:

```go
	mux.Handle("/webdav/staging/", http.StripPrefix("/webdav/staging", webdav.Handler("/", cfg.StagingDir)))
	mux.Handle("/webdav/retrieved/", http.StripPrefix("/webdav/retrieved", webdav.Handler("/", cfg.RetrievedDir)))
	mux.Handle("/webdav/dryrun/", http.StripPrefix("/webdav/dryrun", webdav.Handler("/", cfg.DryRunDir)))
```

with:

```go
	mux.Handle("/webdav/", http.StripPrefix("/webdav", webdav.CombinedHandler(map[string]string{
		"staging":   cfg.StagingDir,
		"retrieved": cfg.RetrievedDir,
		"dryrun":    cfg.DryRunDir,
	})))
```

The three category names match today's URL segments exactly (`staging`,
`retrieved`, `dryrun`), so every existing link that points at e.g.
`/webdav/dryrun/{ID}.iso` (in `dryruns.html`) or mentions
`/webdav/retrieved/` (in `library.html`) keeps working unchanged — only
the single URL an operator *mounts* in a WebDAV client changes, from
three to one.

- [ ] **Step 2: Confirm the module builds**

Run: `go build ./...`
Expected: builds clean (there's no test file for `cmd/archive-core` —
this is the verification step for this task, same as prior plans'
`main.go`-only tasks).

- [ ] **Step 3: Manually smoke-test the unified mount (optional but recommended)**

If you have `docker compose` available:

```bash
docker compose up -d --build
curl -s -u '' --request PROPFIND -H "Depth: 1" http://localhost:8080/webdav/ | grep -o '<D:href>[^<]*' 
```

Expected: three `<D:href>` entries containing `/webdav/staging/`,
`/webdav/retrieved/`, `/webdav/dryrun/`. If docker isn't available in
this environment, skip this step — Task 1's tests already prove the
`CombinedFileSystem`'s behavior at the Go level, and `go build` proves
`main.go` wires it correctly.

- [ ] **Step 4: Commit**

```bash
git add cmd/archive-core/main.go
git commit -m "$(cat <<'EOF'
Mount staging/retrieved/dryrun under one /webdav/ root instead of three

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 3: Dashboard UI — mount info + collapsible staging folders

**Files:**
- Modify: `internal/web/dashboard.go`
- Modify: `internal/web/templates/dashboard.html`
- Create: `internal/web/dashboard_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/web/dashboard_test.go`:

```go
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
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/web/... -run TestDashboardTemplate_ShowsMountURL -v`
Expected: FAIL to compile — `dashboardData` has no `MountURL` field yet.

- [ ] **Step 3: Add `MountURL` to `dashboardData` and compute it in the handler**

In `internal/web/dashboard.go`, update `dashboardData` and `dashboard`:

```go
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
```

(`r.Host` reflects whatever host/port the browser actually used to reach
the server — `localhost:8080`, a LAN IP, whatever — so nothing needs to
be hardcoded or separately configured.)

- [ ] **Step 4: Add the mount blurb to `dashboard.html`**

In `internal/web/templates/dashboard.html`, replace:

```html
<h1>To-Archive</h1>
<p>Staged: {{.StagedBytes}} bytes.</p>
```

with:

```html
<h1>To-Archive</h1>
<p>Mount all archive files over WebDAV at <code>{{.MountURL}}</code> —
on macOS: Finder → <kbd>Cmd+K</kbd> → Connect to Server → paste that URL.
Inside, you'll find <code>staging/</code>, <code>retrieved/</code>, and
<code>dryrun/</code> folders.</p>
<p>Staged: {{.StagedBytes}} bytes.</p>
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/web/... -run TestDashboardTemplate_ShowsMountURL -v`
Expected: PASS

- [ ] **Step 6: Make each staged folder collapsible**

In `internal/web/templates/dashboard.html`, within `staging-files-fragment`,
replace:

```html
{{range .}}
  <fieldset>
    {{if .Name}}
    <legend><label><input type="checkbox" name="path" value="{{.Name}}/"> {{.Name}}/</label></legend>
    {{else}}
    <legend>(root)</legend>
    {{end}}
    <table>
```

with:

```html
{{range .}}
  <details open>
    {{if .Name}}
    <summary><label><input type="checkbox" name="path" value="{{.Name}}/"> {{.Name}}/</label></summary>
    {{else}}
    <summary>(root)</summary>
    {{end}}
    <table>
```

and its matching closing tag — replace the `</fieldset>` a few lines
below (right after the `</table>` inside the same `{{range}}` block) with
`</details>`. The rest of the fragment (the table headers/rows, the
tags/description inputs, the submit button after the `{{end}}`) is
unchanged.

`open` is set on every folder so today's always-visible behavior is
preserved by default — this is about letting the operator *collapse* a
large folder for readability, not hiding folders by default.

- [ ] **Step 7: Run tests to verify everything passes**

Run: `go build ./... && go vet ./... && go test ./internal/web/... -race -v`
Expected: PASS, including the existing `TestStagingFilesFragment_Renders`
(proves the `<details>`/`<summary>` swap didn't break rendering — it
already asserts `vacation/a.jpg` and `root.txt` appear in the output,
which doesn't depend on which wrapper element holds them) and the new
`TestDashboardTemplate_ShowsMountURL`.

- [ ] **Step 8: Commit**

```bash
git add internal/web/dashboard.go internal/web/dashboard_test.go internal/web/templates/dashboard.html
git commit -m "$(cat <<'EOF'
Show the WebDAV mount URL on the Dashboard; make staged folders collapsible

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```
