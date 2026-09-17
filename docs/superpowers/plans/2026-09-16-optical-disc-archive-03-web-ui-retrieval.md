# Optical Disc Archive — 03: Web UI & Retrieval Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire the dashboard/jobs/library/config HTMX pages to the `burn` package (plans 01–02), and implement the normal single-disc retrieval flow plus the group-reconstruction flow from plan 02, as a new `retrieve` package driven by the same "insert disc, click a button" UX as burning.

**Architecture:** `internal/retrieve.Manager` mirrors `burn.Manager`'s shape (one active job, mutex-protected) but consumes discs instead of producing them: it reads a disc's TOC/tar/parity index via `xorriso -indev` (works against both `/dev/sr0` and a plain ISO file, which reconstruction exploits), verifies with `par2verify`, and extracts the requested file with a small pure-Go tar reader. `internal/web` is a thin `net/http` + `html/template` layer with no business logic of its own — every handler just calls into `burn.Manager`/`retrieve.Manager`/`db`.

**Tech Stack:** Go 1.26+ stdlib `net/http` (method+wildcard patterns), `html/template` with `embed.FS`, HTMX (loaded from `cdnjs.cloudflare.com` — see Task 5), `github.com/skip2/go-qrcode` for the disk cover's QR code (no native/stdlib QR encoder exists, so this one small dependency is justified).

**Depends on:** plan 00 (`db`, `execx`, `config`, `webdav`), plan 01 (`burn.Manager`, `toc.TOC`), plan 02 (`burn.Reconstructor`, grouped `DiscPlan`s).

---

## Package Layout (this plan creates/modifies)

```
internal/toc/toc.go               (modified: add Compressed field)
internal/burn/pipeline.go          (modified: set TOC.Compressed)
internal/burn/pack.go              (modified: add ExtractFile)
internal/burn/iso.go               (modified: add ExtractFromDisc)
internal/burn/reconstruct.go       (modified: add ReadRawImage)
internal/db/files.go               (modified: add GetFile)
internal/db/media_types.go         (modified: add GetMediaTypeCapacity)
internal/retrieve/
  retrieve.go
  catalog.go
internal/web/
  server.go
  dashboard.go
  jobs.go
  library.go
  config.go
  cover.go
  templates/
    layout.html
    dashboard.html
    jobs.html
    library.html
    config.html
    cover.html
cmd/archive-core/main.go           (rewritten)
```

---

### Task 1: Extend TOC with a Compressed flag

Retrieval needs to know whether a disc's tar payload is gzipped, and
nothing currently records that.

**Files:**
- Modify: `internal/toc/toc.go`
- Modify: `internal/toc/toc_test.go`
- Modify: `internal/burn/pipeline.go`

- [ ] **Step 1: Update the failing assertion**

Add to `TestMarshalAndParse_RoundTrip` in `internal/toc/toc_test.go`, right
after constructing `orig`:

```go
	orig.Compressed = true
```

And after parsing, add:

```go
	if !got.Compressed {
		t.Error("Parse round-trip lost Compressed=true")
	}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/toc/... -v`
Expected: FAIL — `unknown field Compressed`

- [ ] **Step 3: Add the field**

In `internal/toc/toc.go`, add to the `TOC` struct (after `ParityPercent`):

```go
	Compressed    bool        `json:"compressed"`
```

- [ ] **Step 4: Set it when a disc is packed**

In `internal/burn/pipeline.go`'s `runDisc`, the `toc.TOC{...}` literal
gains one field:

```go
	t := toc.TOC{
		DiskID:        plan.DiskID,
		MediaType:     job.Options.MediaType,
		ParityPercent: job.Options.ParityPercent,
		Compressed:    job.Options.Compress,
		GroupID:       plan.GroupID,
		Role:          plan.Role,
		SlotIndex:     plan.SlotIndex,
		CreatedAt:     time.Now(),
	}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/toc/... ./internal/burn/... -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/toc internal/burn/pipeline.go
git commit -m "$(cat <<'EOF'
Record whether a disc's payload is gzipped, needed for correct retrieval

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 2: Disc-read primitives in the burn package

Three additions the `retrieve` package needs: pulling one named file out of
a disc/ISO image (`ExtractFromDisc`), reading a fixed number of raw bytes
off a device (`ReadRawImage`, for reconstruction), and pulling one member
out of an already-downloaded tar (`ExtractFile`).

**Files:**
- Modify: `internal/burn/iso.go`
- Modify: `internal/burn/reconstruct.go`
- Modify: `internal/burn/pack.go`
- Test: `internal/burn/iso_test.go` (new)
- Test: `internal/burn/reconstruct_test.go`
- Test: `internal/burn/pack_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// internal/burn/iso_test.go
package burn

import (
	"context"
	"testing"

	"varis/internal/execx"
)

func TestExtractFromDisc_InvokesXorrisoIndevExtract(t *testing.T) {
	fake := &execx.FakeExecutor{}
	err := ExtractFromDisc(context.Background(), fake, "/dev/sr0", "/BD:0001.toc.json", "/tmp/out.json")
	if err != nil {
		t.Fatalf("ExtractFromDisc: %v", err)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Name != "xorriso" {
		t.Fatalf("calls = %+v", calls)
	}
	want := []string{"-indev", "/dev/sr0", "-extract", "/BD:0001.toc.json", "/tmp/out.json"}
	if len(calls[0].Args) != len(want) {
		t.Fatalf("args = %v, want %v", calls[0].Args, want)
	}
	for i := range want {
		if calls[0].Args[i] != want[i] {
			t.Fatalf("args = %v, want %v", calls[0].Args, want)
		}
	}
}
```

```go
// add to internal/burn/reconstruct_test.go
func TestReadRawImage_CopiesExactlySizeBytes(t *testing.T) {
	src := filepath.Join(t.TempDir(), "device")
	if err := os.WriteFile(src, bytes.Repeat([]byte{0x42}, 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "out.img")

	if err := ReadRawImage(src, dest, 500); err != nil {
		t.Fatalf("ReadRawImage: %v", err)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 500 {
		t.Fatalf("len(data) = %d, want 500", len(data))
	}
}
```

Add `"os"` and `"path/filepath"` to `reconstruct_test.go`'s imports (`bytes`
is already there from Task 1 of plan 02).

```go
// add to internal/burn/pack_test.go
func TestExtractFile_PullsOneMemberOut(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}
	bucket := binpack.Bucket{Files: []binpack.FileInfo{
		{Path: "a.txt", Size: 5}, {Path: "b.txt", Size: 5},
	}}
	tarPath := filepath.Join(t.TempDir(), "out.tar")
	if err := WriteTar(root, bucket, tarPath, false); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "recovered.txt")
	if err := ExtractFile(tarPath, false, "b.txt", dest); err != nil {
		t.Fatalf("ExtractFile: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "world" {
		t.Errorf("content = %q, want world", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/burn/... -run 'TestExtractFromDisc|TestReadRawImage|TestExtractFile' -v`
Expected: FAIL — functions don't exist yet

- [ ] **Step 3: Implement ExtractFromDisc in iso.go**

```go
// ExtractFromDisc pulls one named entry (pathInISO, absolute within the
// image, e.g. "/BD:0001.tar") out of an ISO 9660 image at src — which may
// be a physical device (e.g. "/dev/sr0") or a plain .iso file, both of
// which xorriso's -indev accepts identically. Reconstruction (see
// reconstruct.go) exploits this to read a reconstructed image the same
// way ReadDisk reads a live disc.
func ExtractFromDisc(ctx context.Context, ex execx.Executor, src, pathInISO, destPath string) error {
	_, err := ex.Run(ctx, "xorriso", "-indev", src, "-extract", pathInISO, destPath)
	return err
}
```

- [ ] **Step 4: Implement ReadRawImage in reconstruct.go**

```go
// ReadRawImage copies exactly sizeBytes from device into destPath, giving
// a byte-for-byte image matching what buildParityPayload XORed at burn
// time (every group member's image is zero-padded to the media's full
// capacity — see xordisk.XOR).
func ReadRawImage(device, destPath string, sizeBytes int64) error {
	in, err := os.Open(device)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.CopyN(out, in, sizeBytes)
	if err != nil && err != io.EOF {
		return err
	}
	return nil
}
```

Add `"io"` and `"os"` to reconstruct.go's imports.

- [ ] **Step 5: Implement ExtractFile in pack.go**

```go
// ExtractFile pulls one member (relPath) out of a tar archive at tarPath
// (gzipped if compressed) and writes it to destPath.
func ExtractFile(tarPath string, compressed bool, relPath, destPath string) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()

	var r io.Reader = f
	if compressed {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		r = gz
	}

	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("file %s not found in %s", relPath, tarPath)
		}
		if err != nil {
			return err
		}
		if hdr.Name == relPath {
			out, err := os.Create(destPath)
			if err != nil {
				return err
			}
			defer out.Close()
			_, err = io.Copy(out, tr)
			return err
		}
	}
}
```

Add `"fmt"` to pack.go's imports (`archive/tar`, `compress/gzip`, `io`,
`os` are already imported there from plan 01).

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/burn/... -v`
Expected: PASS (all burn package tests)

- [ ] **Step 7: Commit**

```bash
git add internal/burn
git commit -m "$(cat <<'EOF'
Add disc-read primitives: ExtractFromDisc, ReadRawImage, ExtractFile

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 3: DB additions for retrieval

**Files:**
- Modify: `internal/db/files.go`
- Modify: `internal/db/media_types.go`
- Test: `internal/db/files_test.go` (new)

- [ ] **Step 1: Write the failing test**

```go
package db

import (
	"context"
	"testing"
)

func TestGetFile(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	if err := SeedMediaTypes(ctx, pool); err != nil {
		t.Fatalf("SeedMediaTypes: %v", err)
	}
	id, err := NextDiskID(ctx, pool, "BDGETFILE")
	if err != nil {
		t.Fatalf("NextDiskID: %v", err)
	}
	if err := InsertDisk(ctx, pool, Disk{ID: id, MediaType: "BD-R", ParityPercent: 10, Role: "data"}); err != nil {
		t.Fatalf("InsertDisk: %v", err)
	}
	if err := InsertFile(ctx, pool, FileRecord{DiskID: id, OriginalPath: "a/b.txt", SizeBytes: 5, FileHash: "x"}); err != nil {
		t.Fatalf("InsertFile: %v", err)
	}

	files, err := SearchFiles(ctx, pool, "b.txt")
	if err != nil || len(files) == 0 {
		t.Fatalf("SearchFiles: %v, %+v", err, files)
	}

	got, err := GetFile(ctx, pool, files[0].ID)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if got.DiskID != id || got.OriginalPath != "a/b.txt" {
		t.Errorf("GetFile = %+v", got)
	}
}

func TestGetMediaTypeCapacity(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	if err := SeedMediaTypes(ctx, pool); err != nil {
		t.Fatalf("SeedMediaTypes: %v", err)
	}
	capacity, err := GetMediaTypeCapacity(ctx, pool, "BD-R")
	if err != nil {
		t.Fatalf("GetMediaTypeCapacity: %v", err)
	}
	if capacity != 25_025_314_816 {
		t.Errorf("capacity = %d, want 25025314816", capacity)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/db/... -run 'TestGetFile|TestGetMediaTypeCapacity' -v`
Expected: FAIL (or SKIP without a live DB) — `undefined: GetFile`

- [ ] **Step 3: Implement GetFile in files.go**

```go
func GetFile(ctx context.Context, pool *pgxpool.Pool, id string) (FileRecord, error) {
	var f FileRecord
	err := pool.QueryRow(ctx,
		`SELECT id, disk_id, original_path, size_bytes, file_hash FROM files WHERE id = $1`, id).
		Scan(&f.ID, &f.DiskID, &f.OriginalPath, &f.SizeBytes, &f.FileHash)
	return f, err
}
```

- [ ] **Step 4: Implement GetMediaTypeCapacity in media_types.go**

```go
func GetMediaTypeCapacity(ctx context.Context, pool *pgxpool.Pool, name string) (int64, error) {
	var capacity int64
	err := pool.QueryRow(ctx,
		`SELECT capacity_bytes FROM media_types WHERE name = $1`, name).Scan(&capacity)
	return capacity, err
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/db/... -v`
Expected: PASS (or SKIP without a live DB)

- [ ] **Step 6: Commit**

```bash
git add internal/db
git commit -m "$(cat <<'EOF'
Add GetFile and GetMediaTypeCapacity for the retrieval and web layers

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 4: The retrieve package

**Files:**
- Create: `internal/retrieve/retrieve.go`
- Create: `internal/retrieve/catalog.go`
- Test: `internal/retrieve/retrieve_test.go`

- [ ] **Step 1: Write catalog.go (production DB adapter)**

```go
package retrieve

import (
	"context"

	"varis/internal/db"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Catalog is the subset of DB access the retrieve package needs.
type Catalog interface {
	GetFile(ctx context.Context, fileID string) (db.FileRecord, error)
	GetDisk(ctx context.Context, diskID string) (db.Disk, error)
	GroupMembers(ctx context.Context, groupID string) ([]db.Disk, error)
	MediaCapacity(ctx context.Context, mediaType string) (int64, error)
}

type dbCatalog struct{ pool *pgxpool.Pool }

func NewDBCatalog(pool *pgxpool.Pool) Catalog { return dbCatalog{pool: pool} }

func (c dbCatalog) GetFile(ctx context.Context, fileID string) (db.FileRecord, error) {
	return db.GetFile(ctx, c.pool, fileID)
}
func (c dbCatalog) GetDisk(ctx context.Context, diskID string) (db.Disk, error) {
	return db.GetDisk(ctx, c.pool, diskID)
}
func (c dbCatalog) GroupMembers(ctx context.Context, groupID string) ([]db.Disk, error) {
	return db.GroupMembers(ctx, c.pool, groupID)
}
func (c dbCatalog) MediaCapacity(ctx context.Context, mediaType string) (int64, error) {
	return db.GetMediaTypeCapacity(ctx, c.pool, mediaType)
}
```

- [ ] **Step 2: Write the failing test for the normal (single-disc) path**

```go
// internal/retrieve/retrieve_test.go
package retrieve

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"varis/internal/db"
	"varis/internal/execx"
	"varis/internal/toc"
)

type fakeCatalog struct {
	files map[string]db.FileRecord
	disks map[string]db.Disk
}

func (c fakeCatalog) GetFile(ctx context.Context, fileID string) (db.FileRecord, error) {
	return c.files[fileID], nil
}
func (c fakeCatalog) GetDisk(ctx context.Context, diskID string) (db.Disk, error) {
	return c.disks[diskID], nil
}
func (c fakeCatalog) GroupMembers(ctx context.Context, groupID string) ([]db.Disk, error) {
	var out []db.Disk
	for _, d := range c.disks {
		if d.GroupID != nil && *d.GroupID == groupID {
			out = append(out, d)
		}
	}
	return out, nil
}
func (c fakeCatalog) MediaCapacity(ctx context.Context, mediaType string) (int64, error) {
	return 1000, nil
}

// fakeDiscExecutor simulates xorriso -indev <src> -extract <path> <dest>
// by serving fixed file contents keyed by the requested in-image path,
// regardless of which "device" (src) is asked — good enough for testing
// one disc at a time.
func fakeDiscExecutor(files map[string][]byte) *execx.FakeExecutor {
	return &execx.FakeExecutor{
		Funcs: map[string]func(args []string) execx.Result{
			"xorriso": func(args []string) execx.Result {
				pathInISO := args[2] // ["-indev", src, "-extract", pathInISO, destPath]
				destPath := args[3]
				content, ok := files[pathInISO]
				if !ok {
					return execx.Result{Err: os.ErrNotExist}
				}
				os.WriteFile(destPath, content, 0o644)
				return execx.Result{}
			},
			"par2verify": func(args []string) execx.Result { return execx.Result{} },
		},
	}
}

func TestManager_ReadDisk_HappyPath(t *testing.T) {
	retrievedDir := t.TempDir()
	scratchDir := t.TempDir()
	device := filepath.Join(t.TempDir(), "device")

	tocBytes, _ := toc.TOC{DiskID: "BD:0001", Compressed: false}.Marshal()

	// A minimal real tar containing "photo.jpg" so ExtractFile can find it.
	tarPath := filepath.Join(t.TempDir(), "src.tar")
	writeTarFixture(t, tarPath, map[string]string{"photo.jpg": "bytes"})
	tarBytes, err := os.ReadFile(tarPath)
	if err != nil {
		t.Fatal(err)
	}

	ex := fakeDiscExecutor(map[string][]byte{
		"/BD:0001.toc.json": tocBytes,
		"/BD:0001.tar":       tarBytes,
		"/BD:0001.tar.par2":  []byte("index"),
	})

	cat := fakeCatalog{files: map[string]db.FileRecord{
		"file1": {ID: "file1", DiskID: "BD:0001", OriginalPath: "photo.jpg"},
	}}

	mgr := NewManager(cat, ex, retrievedDir, scratchDir, device)
	if err := mgr.Start(context.Background(), "file1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if mgr.Current().State != StateAwaitingMedia {
		t.Fatalf("State = %s, want AWAITING_MEDIA", mgr.Current().State)
	}

	if err := mgr.ReadDisk(context.Background()); err != nil {
		t.Fatalf("ReadDisk: %v", err)
	}
	if mgr.Current().State != StateDone {
		t.Fatalf("State = %s, want DONE (err=%v)", mgr.Current().State, mgr.Current().Err)
	}
	got, err := os.ReadFile(filepath.Join(retrievedDir, "photo.jpg"))
	if err != nil {
		t.Fatalf("reading retrieved file: %v", err)
	}
	if string(got) != "bytes" {
		t.Errorf("content = %q, want bytes", got)
	}
}

func TestManager_ReadDisk_WrongDiscRejected(t *testing.T) {
	retrievedDir := t.TempDir()
	scratchDir := t.TempDir()
	device := filepath.Join(t.TempDir(), "device")

	wrongTOC, _ := toc.TOC{DiskID: "BD:9999"}.Marshal()
	ex := fakeDiscExecutor(map[string][]byte{"/BD:0001.toc.json": wrongTOC})
	cat := fakeCatalog{files: map[string]db.FileRecord{
		"file1": {ID: "file1", DiskID: "BD:0001", OriginalPath: "photo.jpg"},
	}}

	mgr := NewManager(cat, ex, retrievedDir, scratchDir, device)
	if err := mgr.Start(context.Background(), "file1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := mgr.ReadDisk(context.Background()); err == nil {
		t.Fatal("expected wrong-disc error")
	}
	if mgr.Current().State != StateFailed {
		t.Fatalf("State = %s, want FAILED", mgr.Current().State)
	}
}

// writeTarFixture is a tiny local helper (not exported from the burn
// package on purpose — retrieve's tests shouldn't need to import burn just
// to build a fixture).
func writeTarFixture(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tw := tarNewWriter(f)
	for name, content := range files {
		hdr := tarHeader(name, int64(len(content)))
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
}
```

`tarNewWriter`/`tarHeader` above are thin wrappers to keep the test's
import list obvious — replace them directly with the standard library:

- [ ] **Step 3: Fix the test fixture helper to use archive/tar directly**

Replace `writeTarFixture`'s body with real `archive/tar` calls and drop the
placeholder `tarNewWriter`/`tarHeader` functions:

```go
func writeTarFixture(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	for name, content := range files {
		hdr := &tar.Header{Name: name, Size: int64(len(content)), Mode: 0o644}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
}
```

Add `"archive/tar"` to retrieve_test.go's imports.

- [ ] **Step 4: Run tests to verify they fail**

Run: `go test ./internal/retrieve/... -v`
Expected: FAIL — package doesn't exist yet

- [ ] **Step 5: Implement retrieve.go**

```go
package retrieve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"varis/internal/burn"
	"varis/internal/execx"
	"varis/internal/toc"
)

type State string

const (
	StateAwaitingMedia       State = "AWAITING_MEDIA"
	StateDone                State = "DONE"
	StateFailed              State = "FAILED"
	StateNeedsReconstruction State = "NEEDS_RECONSTRUCTION"
	StateReconstructing      State = "RECONSTRUCTING"
)

type Job struct {
	FileID       string
	DiskID       string
	OriginalPath string
	State        State
	Err          error
}

// Manager runs at most one retrieval at a time. Callers are responsible
// for not starting a retrieval while a burn.Manager job is in progress on
// the same drive (see web.Server, plan 03 Task 6).
type Manager struct {
	cat          Catalog
	ex           execx.Executor
	retrievedDir string
	scratchDir   string
	device       string

	mu                     sync.Mutex
	job                    *Job
	reconstructor          *burn.Reconstructor
	reconstructionCapacity int64
}

func NewManager(cat Catalog, ex execx.Executor, retrievedDir, scratchDir, device string) *Manager {
	return &Manager{cat: cat, ex: ex, retrievedDir: retrievedDir, scratchDir: scratchDir, device: device}
}

func (m *Manager) Current() *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.job
}

func (m *Manager) Start(ctx context.Context, fileID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job != nil && m.job.State != StateDone && m.job.State != StateFailed {
		return fmt.Errorf("a retrieval is already in progress (state %s)", m.job.State)
	}
	f, err := m.cat.GetFile(ctx, fileID)
	if err != nil {
		return err
	}
	m.job = &Job{FileID: fileID, DiskID: f.DiskID, OriginalPath: f.OriginalPath, State: StateAwaitingMedia}
	return nil
}

// ReadDisk is called once the user has inserted the disc named by the
// current job and clicked "Read Disk".
func (m *Manager) ReadDisk(ctx context.Context) error {
	m.mu.Lock()
	job := m.job
	m.mu.Unlock()
	if job == nil || job.State != StateAwaitingMedia {
		return fmt.Errorf("no retrieval awaiting media")
	}
	return m.readFrom(ctx, job, m.device)
}

// readFrom does the actual TOC-check + parity-verify + extract, reading
// from src (a device path or, from ReadReconstructionDisc, a reconstructed
// .iso file — both work identically via ExtractFromDisc's -indev).
// readFrom routes every failure to one of two outcomes: a disc that's
// unreadable or fails verification means the *physical media* may be
// damaged — exactly the case cross-disc parity exists for — so those go
// to StateNeedsReconstruction, not a dead-end StateFailed. Only "you
// inserted the wrong disc" is a hard failure, since reconstruction can't
// fix a user simply grabbing the wrong one off the shelf; the fix there
// is just inserting the right disc and calling ReadDisk again.
func (m *Manager) readFrom(ctx context.Context, job *Job, src string) error {
	workDir, err := os.MkdirTemp(m.scratchDir, "retrieve-")
	if err != nil {
		return m.fail(job, err)
	}
	defer os.RemoveAll(workDir)

	tocPath := filepath.Join(workDir, job.DiskID+".toc.json")
	if err := burn.ExtractFromDisc(ctx, m.ex, src, "/"+job.DiskID+".toc.json", tocPath); err != nil {
		return m.needsReconstruction(job, fmt.Errorf("reading TOC: %w", err))
	}
	tocBytes, err := os.ReadFile(tocPath)
	if err != nil {
		return m.needsReconstruction(job, err)
	}
	t, err := toc.Parse(tocBytes)
	if err != nil {
		return m.needsReconstruction(job, err)
	}
	if t.DiskID != job.DiskID {
		return m.fail(job, fmt.Errorf("wrong disc: expected %s, found %s", job.DiskID, t.DiskID))
	}

	tarPath := filepath.Join(workDir, job.DiskID+".tar")
	if err := burn.ExtractFromDisc(ctx, m.ex, src, "/"+job.DiskID+".tar", tarPath); err != nil {
		return m.needsReconstruction(job, fmt.Errorf("reading data: %w", err))
	}
	if err := burn.ExtractFromDisc(ctx, m.ex, src, "/"+job.DiskID+".tar.par2", tarPath+".par2"); err != nil {
		return m.needsReconstruction(job, fmt.Errorf("reading parity index: %w", err))
	}

	if err := burn.VerifyParity(ctx, m.ex, tarPath); err != nil {
		return m.needsReconstruction(job, err)
	}

	destPath := filepath.Join(m.retrievedDir, filepath.Base(job.OriginalPath))
	if err := burn.ExtractFile(tarPath, t.Compressed, job.OriginalPath, destPath); err != nil {
		return m.fail(job, fmt.Errorf("extracting file: %w", err))
	}

	m.mu.Lock()
	job.State = StateDone
	m.mu.Unlock()
	return nil
}

func (m *Manager) fail(job *Job, err error) error {
	m.mu.Lock()
	job.State = StateFailed
	job.Err = err
	m.mu.Unlock()
	return err
}

func (m *Manager) needsReconstruction(job *Job, err error) error {
	m.mu.Lock()
	job.State = StateNeedsReconstruction
	job.Err = err
	m.mu.Unlock()
	return err
}

// StartReconstruction begins the "insert every other disc in the group"
// flow after ReadDisk reports StateNeedsReconstruction. It resolves the
// group's fixed media capacity once here (all members share one media
// type — see plan 01's planJob) and passes it into NewReconstructor,
// since that's the same fixed length buildParityPayload used and is the
// only thing both sides can agree on without knowing the missing disc's
// real size (see plan 02's correctness fix note).
func (m *Manager) StartReconstruction(ctx context.Context) error {
	m.mu.Lock()
	job := m.job
	m.mu.Unlock()
	if job == nil || job.State != StateNeedsReconstruction {
		return fmt.Errorf("no disc needing reconstruction")
	}
	disk, err := m.cat.GetDisk(ctx, job.DiskID)
	if err != nil {
		return err
	}
	if disk.GroupID == nil {
		return fmt.Errorf("disc %s is not part of a cross-disc parity group", job.DiskID)
	}
	members, err := m.cat.GroupMembers(ctx, *disk.GroupID)
	if err != nil {
		return err
	}
	capacity, err := m.cat.MediaCapacity(ctx, disk.MediaType)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.reconstructor = burn.NewReconstructor(members, job.DiskID, capacity)
	m.reconstructionCapacity = capacity
	job.State = StateReconstructing
	m.mu.Unlock()
	return nil
}

// Remaining lists the disc IDs still needed for reconstruction.
func (m *Manager) Remaining() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reconstructor == nil {
		return nil
	}
	return m.reconstructor.Remaining()
}

// ReadReconstructionDisc is called once the user has inserted the next
// disc named by Remaining() and clicked "Read". Once every other member
// has been supplied, it reconstructs the missing disc's image and
// extracts the originally requested file straight out of it. It reuses
// the same capacity StartReconstruction resolved, rather than taking a
// media type from the caller — every member of a group shares one media
// type, so there's nothing for a caller to legitimately vary here.
func (m *Manager) ReadReconstructionDisc(ctx context.Context, diskID string) error {
	m.mu.Lock()
	job := m.job
	r := m.reconstructor
	capacity := m.reconstructionCapacity
	m.mu.Unlock()
	if job == nil || r == nil || job.State != StateReconstructing {
		return fmt.Errorf("no reconstruction in progress")
	}

	imagePath := filepath.Join(m.scratchDir, diskID+".img")
	if err := burn.ReadRawImage(m.device, imagePath, capacity); err != nil {
		return err
	}
	data, err := os.ReadFile(imagePath)
	if err != nil {
		return err
	}
	if err := r.SupplyDiscImage(diskID, data); err != nil {
		return err
	}
	if r.NeedsMore() {
		return nil
	}

	image, err := r.Reconstruct()
	if err != nil {
		return err
	}
	reconstructedPath := filepath.Join(m.scratchDir, job.DiskID+".reconstructed.iso")
	if err := os.WriteFile(reconstructedPath, image, 0o644); err != nil {
		return err
	}
	defer os.Remove(reconstructedPath)

	return m.readFrom(ctx, job, reconstructedPath)
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/retrieve/... -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/retrieve
git commit -m "$(cat <<'EOF'
Add retrieve package: normal disc reads plus group reconstruction

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 5: Web server skeleton and templates

**Files:**
- Create: `internal/web/server.go`
- Create: `internal/web/templates/layout.html`
- Create: `internal/web/templates/dashboard.html`
- Create: `internal/web/templates/jobs.html`
- Create: `internal/web/templates/library.html`
- Create: `internal/web/templates/config.html`

- [ ] **Step 1: Write server.go**

```go
package web

import (
	"embed"
	"html/template"
	"net/http"

	"varis/internal/burn"
	"varis/internal/retrieve"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed templates/*.html
var templatesFS embed.FS

type Server struct {
	pool       *pgxpool.Pool
	burnMgr    *burn.Manager
	retrieveMgr *retrieve.Manager
	tmpl       *template.Template
	stagingDir string
}

func NewServer(pool *pgxpool.Pool, burnMgr *burn.Manager, retrieveMgr *retrieve.Manager, stagingDir string) (*Server, error) {
	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{pool: pool, burnMgr: burnMgr, retrieveMgr: retrieveMgr, tmpl: tmpl, stagingDir: stagingDir}, nil
}

func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /", s.dashboard)
	mux.HandleFunc("POST /burn", s.startBurn)
	mux.HandleFunc("GET /jobs", s.jobsFragment)
	mux.HandleFunc("POST /jobs/continue", s.continueDisc)
	mux.HandleFunc("POST /jobs/retry", s.retryDisc)

	mux.HandleFunc("GET /library", s.library)
	mux.HandleFunc("GET /search", s.search)
	mux.HandleFunc("POST /retrieve", s.startRetrieve)
	mux.HandleFunc("POST /retrieve/read", s.readDisk)
	mux.HandleFunc("POST /retrieve/reconstruct/start", s.startReconstruction)
	mux.HandleFunc("POST /retrieve/reconstruct/read", s.readReconstructionDisc)

	mux.HandleFunc("GET /config", s.configPage)
	mux.HandleFunc("POST /config", s.addMediaType)

	mux.HandleFunc("GET /cover/{diskID}", s.cover)
}
```

**Important:** `template.ParseFS` merges every file matched by the glob
into *one* shared template set. If each page defined its own block named
`{{define "content"}}`, the last one parsed would silently overwrite all
the others — every page would render as whichever page happens to parse
last. To avoid that, `layout.html` below only defines two small, genuinely
shared fragments (`head`, `nav`); every page template is instead a
**uniquely-named**, complete `<html>` document (`"dashboard"`, `"library"`,
`"config"`, `"cover"`) that includes those two fragments by name. Handlers
in Task 6 render pages by that unique name, never by a shared `"content"`.

- [ ] **Step 2: Write layout.html**

```html
{{define "head"}}
<meta charset="utf-8">
<title>Varis Optical Archive</title>
<script src="https://cdnjs.cloudflare.com/ajax/libs/htmx/1.9.12/htmx.min.js"></script>
<style>
  body { font-family: system-ui, sans-serif; max-width: 720px; margin: 2rem auto; padding: 0 1rem; }
  nav a { margin-right: 1rem; }
  table { width: 100%; border-collapse: collapse; }
  td, th { text-align: left; padding: 0.25rem 0.5rem; border-bottom: 1px solid #ddd; }
  .error { color: #b00020; }
</style>
{{end}}

{{define "nav"}}
<nav>
  <a href="/">Dashboard</a>
  <a href="/library">Library</a>
  <a href="/config">Config</a>
</nav>
{{end}}
```

- [ ] **Step 3: Write dashboard.html**

```html
{{define "dashboard"}}
<!doctype html>
<html>
<head>{{template "head"}}</head>
<body>
{{template "nav"}}
<h1>To-Archive</h1>
<p>Staged: {{.StagedBytes}} bytes.</p>

<form hx-post="/burn" hx-target="#job-status" hx-swap="innerHTML">
  <label>Media type
    <select name="media_type">
      {{range .MediaTypes}}<option value="{{.Name}}">{{.Name}} ({{.CapacityBytes}} bytes)</option>{{end}}
    </select>
  </label>
  <label>Parity % <input type="number" name="parity_percent" value="10" min="5" max="50"></label>
  <label><input type="checkbox" name="compress"> Compress</label>
  <label><input type="checkbox" name="cross_disc_parity"> Add cross-disc recovery disc</label>
  <label>Group size <input type="number" name="group_size" value="10" min="2"></label>
  <button type="submit">Burn</button>
</form>

<div id="job-status" hx-get="/jobs" hx-trigger="load, every 2s">
  {{template "jobs-fragment" .Job}}
</div>
</body>
</html>
{{end}}
```

- [ ] **Step 4: Write jobs.html**

Only the reusable fragment lives here — it has no full-page wrapper of its
own since it's always rendered as a partial (embedded in "dashboard", or
returned directly by the `/jobs`, `/jobs/continue`, `/jobs/retry` handlers).

```html
{{define "jobs-fragment"}}
{{if .}}
<p>Disc {{.CurrentIndex1}}/{{.TotalDiscs}} — state: {{.State}}{{if .Err}} — error: {{.Err}}{{end}}</p>
{{if eq .State "AWAITING_DISC"}}
  <form hx-post="/jobs/continue" hx-target="#job-status" hx-swap="innerHTML">
    <button type="submit">Insert blank disc, continue</button>
  </form>
{{end}}
{{if eq .State "FAILED"}}
  <form hx-post="/jobs/retry" hx-target="#job-status" hx-swap="innerHTML">
    <button type="submit">Retry this disc</button>
  </form>
{{end}}
{{if eq .State "DONE"}}
  <p><a href="/cover/{{.CurrentDiskID}}">Printable cover</a></p>
{{end}}
{{else}}
<p>No burn in progress.</p>
{{end}}
{{end}}
```

- [ ] **Step 5: Write library.html**

```html
{{define "library"}}
<!doctype html>
<html>
<head>{{template "head"}}</head>
<body>
{{template "nav"}}
<h1>Library</h1>
<input type="search" name="q" placeholder="Search files…"
       hx-get="/search" hx-trigger="keyup changed delay:300ms" hx-target="#results">
<table id="results">
  <tr><th>File</th><th>Size</th><th>Disc</th><th></th></tr>
  {{template "search-rows" .}}
</table>
<div id="retrieve-status"></div>
</body>
</html>
{{end}}

{{define "search-rows"}}
{{range .}}
<tr>
  <td>{{.OriginalPath}}</td>
  <td>{{.SizeBytes}}</td>
  <td>{{.DiskID}}</td>
  <td>
    <form hx-post="/retrieve" hx-target="#retrieve-status" hx-swap="innerHTML">
      <input type="hidden" name="file_id" value="{{.ID}}">
      <button type="submit">Get</button>
    </form>
  </td>
</tr>
{{end}}
{{end}}
```

- [ ] **Step 6: Write config.html**

```html
{{define "config"}}
<!doctype html>
<html>
<head>{{template "head"}}</head>
<body>
{{template "nav"}}
<h1>Media Types</h1>
<table>
  <tr><th>Name</th><th>Capacity (bytes)</th></tr>
  {{range .}}<tr><td>{{.Name}}</td><td>{{.CapacityBytes}}</td></tr>{{end}}
</table>
<form method="post" action="/config">
  <label>Name <input type="text" name="name" required></label>
  <label>Capacity bytes <input type="number" name="capacity_bytes" required></label>
  <button type="submit">Add</button>
</form>
</body>
</html>
{{end}}
```

- [ ] **Step 7: Build**

Run: `go build ./...`
Expected: fails to compile until Task 6's handlers exist (`s.dashboard`
etc. are referenced but not yet defined) — that's expected at this point;
proceed to Task 6 before running tests.

- [ ] **Step 8: Commit**

```bash
git add internal/web/server.go internal/web/templates
git commit -m "$(cat <<'EOF'
Add web server skeleton and HTMX templates

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 6: HTTP handlers

**Files:**
- Create: `internal/web/dashboard.go`
- Create: `internal/web/jobs.go`
- Create: `internal/web/library.go`
- Create: `internal/web/config.go`
- Create: `internal/web/cover.go`
- Create: `internal/web/templates/cover.html`
- Test: `internal/web/handlers_test.go`

- [ ] **Step 1: Write dashboard.go**

```go
package web

import (
	"net/http"

	"varis/internal/db"
	"varis/internal/webdav"
)

type dashboardData struct {
	StagedBytes int64
	MediaTypes  []db.MediaType
	Job         *jobView
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	staged, err := webdav.DirSize(s.stagingDir)
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
		MediaTypes:  types,
		Job:         s.currentJobView(),
	}
	s.render(w, "dashboard", data)
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
```

- [ ] **Step 2: Write jobs.go**

```go
package web

import (
	"fmt"
	"net/http"
	"strconv"

	"varis/internal/burn"
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
	capacity, err := getMediaCapacity(r, s, mediaType)
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

func getMediaCapacity(r *http.Request, s *Server, mediaType string) (int64, error) {
	return dbGetMediaTypeCapacity(r, s, mediaType)
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
```

The `getMediaCapacity`/`dbGetMediaTypeCapacity` indirection above is
unnecessary — simplify directly to the db package call:

- [ ] **Step 3: Simplify startBurn's capacity lookup**

Replace `getMediaCapacity` and its `dbGetMediaTypeCapacity` call in
`startBurn` with:

```go
	capacity, err := db.GetMediaTypeCapacity(r.Context(), s.pool, mediaType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
```

Delete the `getMediaCapacity` function entirely and add `"varis/internal/db"`
to jobs.go's imports.

- [ ] **Step 4: Write library.go**

```go
package web

import (
	"net/http"

	"varis/internal/db"
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
	fileID := r.FormValue("file_id")
	if err := s.retrieveMgr.Start(r.Context(), fileID); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	s.renderRetrieveStatus(w)
}

func (s *Server) readDisk(w http.ResponseWriter, r *http.Request) {
	if err := s.retrieveMgr.ReadDisk(r.Context()); err != nil {
		// A NEEDS_RECONSTRUCTION state is still a normal outcome the
		// fragment below knows how to render, so don't fail the request.
	}
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
	if err := s.retrieveMgr.ReadReconstructionDisc(r.Context(), diskID); err != nil {
		// Rendered in the fragment, same as readDisk.
	}
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
```

Add `"varis/internal/retrieve"` to library.go's imports.

Add a `retrieve-status` template fragment — append to
`internal/web/templates/library.html`:

```html
{{define "retrieve-status"}}
{{if .}}
<p>File: {{.OriginalPath}} — disc: {{.DiskID}} — state: {{.State}}{{if .Err}} — {{.Err}}{{end}}</p>
{{if eq .State "AWAITING_MEDIA"}}
  <form hx-post="/retrieve/read" hx-target="#retrieve-status"><button type="submit">Insert {{.DiskID}}, Read Disk</button></form>
{{end}}
{{if eq .State "NEEDS_RECONSTRUCTION"}}
  <form hx-post="/retrieve/reconstruct/start" hx-target="#retrieve-status"><button type="submit">Reconstruct from group</button></form>
{{end}}
{{if eq .State "RECONSTRUCTING"}}
  {{range .Remaining}}
  <form hx-post="/retrieve/reconstruct/read" hx-target="#retrieve-status">
    <input type="hidden" name="disk_id" value="{{.}}">
    <button type="submit">Insert {{.}}, Read</button>
  </form>
  {{end}}
{{end}}
{{if eq .State "DONE"}}<p>Ready in WebDAV under /webdav/retrieved/</p>{{end}}
{{end}}
{{end}}
```

- [ ] **Step 5: Write config.go**

```go
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
	if err != nil {
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
```

- [ ] **Step 6: Write cover.go and cover.html**

```go
package web

import (
	"net/http"

	"varis/internal/db"

	qrcode "github.com/skip2/go-qrcode"
)

type coverData struct {
	Disk    db.Disk
	QRDataURI string
}

func (s *Server) cover(w http.ResponseWriter, r *http.Request) {
	diskID := r.PathValue("diskID")
	disk, err := db.GetDisk(r.Context(), s.pool, diskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	png, err := qrcode.Encode("archive://disk/"+diskID, qrcode.Medium, 256)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "cover", coverData{Disk: disk, QRDataURI: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)})
}
```

Add `"encoding/base64"` to cover.go's imports and run:
```bash
go get github.com/skip2/go-qrcode
```

```html
{{define "cover"}}
<!doctype html>
<html>
<head>{{template "head"}}</head>
<body>
<h1>{{.Disk.ID}}</h1>
<p>{{.Disk.MediaType}} — parity {{.Disk.ParityPercent}}% — burned {{.Disk.CreatedAt}}</p>
<img src="{{.QRDataURI}}" width="256" height="256" alt="QR code for {{.Disk.ID}}">
<p>Scan to look up this disc's contents at archive://disk/{{.Disk.ID}}</p>
</body>
</html>
{{end}}
```

(Save as `internal/web/templates/cover.html`. Print this page from the
browser — Cmd/Ctrl+P — to get a physical disc cover, per the spec's
decision to avoid a server-side PDF library.)

- [ ] **Step 7: Write a handler-level test**

```go
// internal/web/handlers_test.go
package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearch_EmptyQueryReturnsNoRows(t *testing.T) {
	// This only exercises the empty-query short-circuit, which needs no
	// database — everything else in this package is thin enough (a
	// one-line call into db/burn/retrieve, already tested in their own
	// packages) that handler-level tests would mostly re-test those
	// packages through an HTTP wrapper, which is not worth the added
	// httptest+template plumbing at this project's scale.
	s := &Server{}
	tmpl := mustParseTestTemplate(t, `{{define "search-rows"}}{{len .}} rows{{end}}`)
	s.tmpl = tmpl

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/search?q=", nil)
	s.search(w, r)

	if !strings.Contains(w.Body.String(), "0 rows") {
		t.Errorf("body = %q, want it to contain '0 rows'", w.Body.String())
	}
}

func mustParseTestTemplate(t *testing.T, src string) *templateT {
	t.Helper()
	tmpl, err := newTemplateFromString(src)
	if err != nil {
		t.Fatal(err)
	}
	return tmpl
}
```

`templateT`/`newTemplateFromString` aren't real — fix this test to use
`html/template` directly instead of inventing helper types:

- [ ] **Step 8: Fix the handler test to use html/template directly**

```go
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
```

- [ ] **Step 9: Build and run tests**

Run: `go build ./... && go test ./internal/web/... -v`
Expected: builds cleanly, test PASSes

- [ ] **Step 10: Commit**

```bash
git add internal/web go.mod go.sum
git commit -m "$(cat <<'EOF'
Wire dashboard, jobs, library, config, and cover HTTP handlers

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 7: Rewrite main.go

**Files:**
- Modify: `cmd/archive-core/main.go`

- [ ] **Step 1: Rewrite main.go**

```go
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"varis/internal/burn"
	"varis/internal/config"
	"varis/internal/db"
	"varis/internal/execx"
	"varis/internal/retrieve"
	"varis/internal/web"
	"varis/internal/webdav"
)

func main() {
	cfg := config.Load()
	ctx := context.Background()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db connect: %v", err)
	}
	defer pool.Close()

	if err := db.SeedMediaTypes(ctx, pool); err != nil {
		log.Fatalf("seed media types: %v", err)
	}

	ex := execx.RealExecutor{}
	scratchDir := os.TempDir()

	burnMgr := burn.NewManager(burn.NewDBCataloger(pool), ex, cfg.StagingDir, cfg.SpoolDir, cfg.OpticalDevice)
	retrieveMgr := retrieve.NewManager(retrieve.NewDBCatalog(pool), ex, cfg.RetrievedDir, scratchDir, cfg.OpticalDevice)

	webServer, err := web.NewServer(pool, burnMgr, retrieveMgr, cfg.StagingDir)
	if err != nil {
		log.Fatalf("web server: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.Handle("/webdav/staging/", http.StripPrefix("/webdav/staging", webdav.Handler("/", cfg.StagingDir)))
	mux.Handle("/webdav/retrieved/", http.StripPrefix("/webdav/retrieved", webdav.Handler("/", cfg.RetrievedDir)))
	webServer.Routes(mux)

	log.Printf("listening on %s", cfg.HTTPAddr)
	log.Fatal(http.ListenAndServe(cfg.HTTPAddr, mux))
}
```

- [ ] **Step 2: Build**

Run: `go build ./...`
Expected: builds with no errors

- [ ] **Step 3: Commit**

```bash
git add cmd
git commit -m "$(cat <<'EOF'
Wire burn.Manager, retrieve.Manager, and web.Server into main.go

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

- [ ] **Step 4: Manual smoke test**

```bash
docker compose up --build -d
curl http://localhost:8080/
curl http://localhost:8080/library
curl http://localhost:8080/config
```
Expected: three HTML pages render without error. Full click-through (drag
a file into `/webdav/staging/`, burn, search, retrieve) needs a real
optical drive and is a manual test on the Linux host, not automatable here.

---

## Plan Self-Review

**Spec coverage:** Dashboard/Jobs/Library/Config pages, normal retrieval
flow (search → Get → insert disc → Read Disk → WebDAV), reconstruction
wiring for a disc that fails verification, and the printable HTML+QR cover
(§6) are all covered. `burn.Manager` and `retrieve.Manager` are each
single-job and mutex-protected on their own, but nothing in this plan yet
stops a burn and a retrieval running "at once" against the one shared
physical drive — that gap is closed in plan 04 Task 1, once both
managers exist for a handler to check against each other.

**Placeholder scan:** Task 6 Step 2/3 (`getMediaCapacity` introduced then
removed) and Step 7/8 (the handler test's `templateT`/`newTemplateFromString`
introduced then replaced with real `html/template` calls) each contain a
deliberately-wrong intermediate snippet, corrected in the very next step
(mirroring the pattern already used in plan 01 Task 6) — not left-over
placeholders. No other TBD/TODO remain.

**Correctness fix found on review:** the first draft of `readFrom` only
routed a `par2verify` failure to `StateNeedsReconstruction`; any failure to
even *read* the TOC/tar/parity-index off the disc (the more likely shape
of "this disc is actually destroyed," which is the scenario cross-disc
parity exists to survive) fell through to a dead-end `StateFailed` with no
recovery path. Every failure except "wrong disc ID" (a user picking the
wrong disc off the shelf, which reconstruction can't and shouldn't paper
over) now goes to `StateNeedsReconstruction` via the new `needsReconstruction`
helper, alongside `fail`.

**Type consistency:** `jobView` (this plan) wraps `burn.Job`/`burn.State`
without renaming any of plan 01/02's fields. `retrieve.Job`/`retrieve.State`
mirror `burn.Job`/`burn.State`'s shape by convention but are a distinct
type, matching that retrieval and burning are separate managers. Handler
names (`startBurn`, `continueDisc`, `retryDisc`, `startRetrieve`,
`readDisk`, `startReconstruction`, `readReconstructionDisc`) match the
routes registered in `server.go` exactly.
