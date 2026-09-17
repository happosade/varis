# Unified WebDAV Mount + Dashboard Mount Info + Collapsible Folders — Design Spec

## 1. Overview

Today the archive exposes three separate WebDAV mounts —
`/webdav/staging/`, `/webdav/retrieved/`, `/webdav/dryrun/` — each backed
by its own `webdav.Handler(dir)`. A WebDAV client (Finder, Explorer,
`cadaver`, etc.) connects to exactly one root URL per mount, so seeing
all three areas means connecting three times. This replaces the three
mounts with a single `/webdav/` root exposing `staging/`, `retrieved/`,
and `dryrun/` as subfolders, adds a Dashboard blurb telling the operator
what URL to mount and how, and makes the Dashboard's staged-files listing
collapsible per folder for readability.

## 2. Unified WebDAV Mount

A new `CombinedFileSystem` type in `internal/webdav` implements
`golang.org/x/net/webdav.FileSystem` (`Mkdir`, `OpenFile`, `RemoveAll`,
`Rename`, `Stat`) and holds three named `webdav.Dir` values keyed by
their top-level path segment: `staging`, `retrieved`, `dryrun` — chosen
to match today's existing URL segments exactly, so every existing link
(`/webdav/dryrun/{ID}.iso` in `dryruns.html`, the `/webdav/retrieved/`
mention in `library.html`) keeps working unchanged; only the single URL
an operator *mounts* in a WebDAV client changes, from three to one.

Every method splits the requested name into its first path segment (the
category) and the remainder, then delegates to that category's
`webdav.Dir` for anything below the root. The root path (`"/"`) is
handled specially, since it doesn't belong to any one category:

- `Stat(ctx, "/")` returns a synthetic directory `os.FileInfo` (name
  `"/"`, `IsDir() == true`, zero size, current time as mod time — nothing
  downstream depends on more than that for a container directory).
- `OpenFile(ctx, "/", os.O_RDONLY, ...)` (the only valid flag combination
  for the root — anything requesting write access to `/` itself, e.g. an
  attempted create/rename of a top-level entry, is rejected with
  `os.ErrPermission`, since the three categories are fixed) returns a
  small `rootDir` value implementing `webdav.File`: `Readdir` returns the
  three categories' synthetic directory `os.FileInfo`s, `Stat` returns
  the same info `Stat(ctx, "/")` does, and `Read`/`Write`/`Seek` return
  `os.ErrInvalid` (mirroring how a real directory handle behaves for a
  read/write attempt — `os.File` itself returns an error from `Read` on a
  directory).
- `Mkdir(ctx, "/", ...)`, `RemoveAll(ctx, "/", ...)`, and any `Rename`
  naming `/` as either side all return `os.ErrPermission` — the three
  categories are fixed, not user-manageable.
- `Rename` where the two names resolve to *different* categories also
  returns `os.ErrPermission` — moving a file between staging, retrieved,
  and dry-run isn't a supported operation (nothing before this needed
  it, and doing it well would mean copying across real, separately
  -configured directories rather than a single `os.Rename`, which is out
  of scope here).

For any other path, the first segment must be exactly one of the three
known categories (checked via a small `map[string]webdav.Dir` lookup);
an unrecognized category returns `os.ErrNotExist`.

`cmd/archive-core/main.go` replaces its three `mux.Handle("/webdav/...", ...)`
lines with one:

```go
combined := webdav.CombinedFileSystem(map[string]string{
	"staging":   cfg.StagingDir,
	"retrieved": cfg.RetrievedDir,
	"dryrun":    cfg.DryRunDir,
})
mux.Handle("/webdav/", http.StripPrefix("/webdav", &stdwebdav.Handler{
	Prefix:     "/",
	FileSystem: combined,
	LockSystem: stdwebdav.NewMemLS(),
}))
```

(Exact construction — a constructor function vs. a literal, and where
the `golang.org/x/net/webdav.Handler` gets built — is an implementation
detail for the plan; the shape above just illustrates that one handler
now serves all three categories.)

## 3. Dashboard Mount Info

`dashboardData` gains a `MountURL string` field, computed in the
`dashboard` handler as `"http://" + r.Host + "/webdav/"` — this reflects
whatever host/port the browser actually used to reach the server
(`localhost:8080`, a LAN IP, etc.), so nothing needs to be hardcoded or
separately configured.

Shown near the top of `dashboard.html`, above the staged-files listing:

```html
<p>Mount all archive files over WebDAV at <code>{{.MountURL}}</code> —
on macOS: Finder → <kbd>Cmd+K</kbd> → Connect to Server → paste that URL.
Inside, you'll find <code>staging/</code>, <code>retrieved/</code>, and
<code>dryrun/</code> folders.</p>
```

## 4. Collapsible Staging Folders

In `staging-files-fragment` (`dashboard.html`), each folder's
`<fieldset>`/`<legend>` pair becomes `<details>`/`<summary>` — a native
HTML disclosure widget, no JavaScript needed:

```html
{{range .}}
  <details open>
    {{if .Name}}
    <summary><label><input type="checkbox" name="path" value="{{.Name}}/"> {{.Name}}/</label></summary>
    {{else}}
    <summary>(root)</summary>
    {{end}}
    <table>
      ...unchanged...
    </table>
  </details>
{{end}}
```

`open` is set on every folder by default (matching today's always-visible
behavior) — the feature is about letting the operator *collapse* a large
folder for readability, not about hiding folders by default.

## 5. Error Handling & Testing

- `CombinedFileSystem`'s tests mirror the existing
  `TestHandler_PutAndGet` pattern in `internal/webdav/webdav_test.go`:
  root listing (via a `PROPFIND`-equivalent `Readdir` call, or by driving
  the handler with an actual `PROPFIND` request and checking the
  response body mentions all three category names) shows `staging`,
  `retrieved`, `dryrun`; a `PUT`/`GET`/`DELETE` sequence against
  `/staging/x.txt` (and the same against `/retrieved/` and `/dryrun/`)
  round-trips correctly; a request against `/nope/x.txt` 404s; a `PUT`
  attempted directly at `/newcategory` (i.e., creating a fourth
  top-level entry) is rejected.
- No new tests needed for the Dashboard blurb or the folder-collapse
  change beyond the project's existing whole-template-set
  parse-and-execute tests (`TestTemplates_ParseWithoutError` /
  `TestStagingFilesFragment_Renders`, per `internal/web/handlers_test.go`
  and `internal/web/staging_test.go`), which already catch a template
  syntax mistake in either change.

## 6. Non-Goals

- Any change to what's servable over WebDAV beyond regrouping the same
  three existing directories under one root — no new category, no
  auth, no per-category access control beyond what already existed
  (none).
- Cross-category move/rename (see Section 2).
- Collapsing anything in the Library search results — that listing has
  no folder concept today (files are grouped by disc, not directory);
  out of scope here per the user's confirmation that the staging listing
  is the one meant.
