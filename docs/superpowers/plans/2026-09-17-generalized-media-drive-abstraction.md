# Generalized Media/Drive Abstraction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the hardcoded assumption that all media is optical, burned via `wodim` against one fixed device — each media type now declares its own disk-ID prefix and write mechanism (optical burn, or plain filesystem copy for RDX/USB-style media), and the target device/mount path is supplied per burn and per retrieval instead of fixed at process startup.

**Architecture:** `media_types` gains `id_prefix`/`write_kind` columns, replacing `mediaPrefix`'s hardcoded Go switch entirely. `burn.Manager`/`retrieve.Manager` drop their constructor-time `device` field — the target path now travels with each job (`Options.TargetPath`) or each read call (`ReadDisk`/`ReadReconstructionDisc`'s new parameter) instead. `runDisc`'s write step dispatches on `WriteKind`, reusing the dry-run plan's `copyFile` helper and the existing `VerifyBurn` unchanged for the filesystem case.

**Tech Stack:** Go 1.26+ stdlib, `pgx/v5`, `html/template` + HTMX (all already in use — no new dependencies).

**Depends on:** `docs/superpowers/plans/2026-09-17-dry-run-burn-mode.md` — execute that plan first. Every code snippet below assumes its changes (`Options.DryRun`, `Manager.dryRunDir`, the `copyFile` helper, `ReadDisk`/`ReadReconstructionDisc`'s existing `IsDryRun` checks) are already in place.

---

### Task 1: DB layer — `media_types.id_prefix`/`write_kind`, `GetMediaType`

**Files:**
- Modify: `internal/db/schema.sql`
- Modify: `internal/db/media_types.go`
- Modify: `internal/db/files_test.go` (existing `TestGetMediaTypeCapacity` sits here; a new test for `GetMediaType` belongs alongside it)

- [ ] **Step 1: Write the failing tests**

Add to `internal/db/files_test.go`:

```go
func TestGetMediaType_ReturnsFullRow(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	if err := SeedMediaTypes(ctx, pool); err != nil {
		t.Fatalf("SeedMediaTypes: %v", err)
	}
	mt, err := GetMediaType(ctx, pool, "BD-R")
	if err != nil {
		t.Fatalf("GetMediaType: %v", err)
	}
	if mt.CapacityBytes != 25_025_314_816 || mt.IDPrefix != "BD" || mt.WriteKind != "optical" {
		t.Errorf("GetMediaType(BD-R) = %+v, want CapacityBytes=25025314816 IDPrefix=BD WriteKind=optical", mt)
	}
}

func TestAddMediaType_CarriesIDPrefixAndWriteKind(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	name := "TESTUSB-" + t.Name()
	if err := AddMediaType(ctx, pool, MediaType{Name: name, CapacityBytes: 1000, IDPrefix: "USB", WriteKind: "filesystem"}); err != nil {
		t.Fatalf("AddMediaType: %v", err)
	}
	mt, err := GetMediaType(ctx, pool, name)
	if err != nil {
		t.Fatalf("GetMediaType: %v", err)
	}
	if mt.IDPrefix != "USB" || mt.WriteKind != "filesystem" {
		t.Errorf("GetMediaType(%s) = %+v, want IDPrefix=USB WriteKind=filesystem", name, mt)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `docker compose up -d archive-db && DATABASE_URL='postgres://varis:varis@localhost:5432/varis?sslmode=disable' go test ./internal/db/... -run 'TestGetMediaType_ReturnsFullRow|TestAddMediaType_CarriesIDPrefixAndWriteKind' -v`
Expected: FAIL to compile — `MediaType` has no `IDPrefix`/`WriteKind` fields yet, and `GetMediaType` doesn't exist.

- [ ] **Step 3: Add the schema columns**

Append to `internal/db/schema.sql`:

```sql
ALTER TABLE media_types ADD COLUMN IF NOT EXISTS id_prefix TEXT NOT NULL DEFAULT 'DISC';
ALTER TABLE media_types ADD COLUMN IF NOT EXISTS write_kind TEXT NOT NULL DEFAULT 'optical';
```

- [ ] **Step 4: Update `internal/db/media_types.go`**

Replace its contents:

```go
package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type MediaType struct {
	Name          string
	CapacityBytes int64
	IDPrefix      string
	WriteKind     string // "optical" | "filesystem"
}

var defaultMediaTypes = []MediaType{
	{Name: "BD-R", CapacityBytes: 25_025_314_816, IDPrefix: "BD", WriteKind: "optical"},
	{Name: "BD-R DL", CapacityBytes: 50_050_629_632, IDPrefix: "BDDL", WriteKind: "optical"},
}

func SeedMediaTypes(ctx context.Context, pool *pgxpool.Pool) error {
	for _, mt := range defaultMediaTypes {
		if _, err := pool.Exec(ctx,
			`INSERT INTO media_types (name, capacity_bytes, id_prefix, write_kind) VALUES ($1, $2, $3, $4) ON CONFLICT (name) DO NOTHING`,
			mt.Name, mt.CapacityBytes, mt.IDPrefix, mt.WriteKind); err != nil {
			return err
		}
	}
	return nil
}

func ListMediaTypes(ctx context.Context, pool *pgxpool.Pool) ([]MediaType, error) {
	rows, err := pool.Query(ctx, `SELECT name, capacity_bytes, id_prefix, write_kind FROM media_types ORDER BY capacity_bytes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MediaType
	for rows.Next() {
		var mt MediaType
		if err := rows.Scan(&mt.Name, &mt.CapacityBytes, &mt.IDPrefix, &mt.WriteKind); err != nil {
			return nil, err
		}
		out = append(out, mt)
	}
	return out, rows.Err()
}

func AddMediaType(ctx context.Context, pool *pgxpool.Pool, mt MediaType) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO media_types (name, capacity_bytes, id_prefix, write_kind) VALUES ($1, $2, $3, $4)`,
		mt.Name, mt.CapacityBytes, mt.IDPrefix, mt.WriteKind)
	return err
}

// GetMediaTypeCapacity is kept separate from GetMediaType (below) because
// internal/retrieve's Catalog.MediaCapacity only ever needs the capacity,
// to resolve a cross-disc-parity group's fixed XOR length — it has no use
// for IDPrefix/WriteKind, both of which are burn-time-only concerns.
func GetMediaTypeCapacity(ctx context.Context, pool *pgxpool.Pool, name string) (int64, error) {
	var capacity int64
	err := pool.QueryRow(ctx,
		`SELECT capacity_bytes FROM media_types WHERE name = $1`, name).Scan(&capacity)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("media type %q not found", name)
	}
	return capacity, err
}

// GetMediaType returns the full row, for callers (the web layer's
// startBurn) that need IDPrefix/WriteKind alongside capacity.
func GetMediaType(ctx context.Context, pool *pgxpool.Pool, name string) (MediaType, error) {
	var mt MediaType
	err := pool.QueryRow(ctx,
		`SELECT name, capacity_bytes, id_prefix, write_kind FROM media_types WHERE name = $1`, name).
		Scan(&mt.Name, &mt.CapacityBytes, &mt.IDPrefix, &mt.WriteKind)
	if errors.Is(err, pgx.ErrNoRows) {
		return MediaType{}, fmt.Errorf("media type %q not found", name)
	}
	return mt, err
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `DATABASE_URL='postgres://varis:varis@localhost:5432/varis?sslmode=disable' go test ./internal/db/... -run 'TestGetMediaType_ReturnsFullRow|TestAddMediaType_CarriesIDPrefixAndWriteKind|TestGetMediaTypeCapacity' -v`
Expected: PASS

- [ ] **Step 6: Confirm the rest of the module still builds**

Run: `go build ./...`
Expected: builds clean — check `internal/web/config.go`'s `db.MediaType{Name: ..., CapacityBytes: ...}` literal (it's keyed, so adding two trailing fields doesn't break it — it will just leave `IDPrefix`/`WriteKind` at their zero value `""` until Task 2 updates that handler).

- [ ] **Step 7: Commit**

```bash
git add internal/db/schema.sql internal/db/media_types.go internal/db/files_test.go
git commit -m "$(cat <<'EOF'
Add media_types.id_prefix/write_kind and db.GetMediaType

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 2: Config UI — ID prefix and write-kind fields

**Files:**
- Modify: `internal/web/config.go`
- Modify: `internal/web/templates/config.html`

- [ ] **Step 1: Validate and persist the two new fields in `addMediaType`**

Replace `internal/web/config.go`'s contents:

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
	if err != nil || capacity <= 0 {
		http.Error(w, "invalid capacity_bytes", http.StatusBadRequest)
		return
	}
	writeKind := r.FormValue("write_kind")
	if writeKind != "optical" && writeKind != "filesystem" {
		http.Error(w, `write_kind must be "optical" or "filesystem"`, http.StatusBadRequest)
		return
	}
	idPrefix := r.FormValue("id_prefix")
	if idPrefix == "" {
		http.Error(w, "id_prefix is required", http.StatusBadRequest)
		return
	}
	mt := db.MediaType{
		Name:          r.FormValue("name"),
		CapacityBytes: capacity,
		IDPrefix:      idPrefix,
		WriteKind:     writeKind,
	}
	if err := db.AddMediaType(r.Context(), s.pool, mt); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/config", http.StatusSeeOther)
}
```

- [ ] **Step 2: Add the fields to the Config page**

Replace `internal/web/templates/config.html`'s contents:

```html
{{define "config"}}
<!doctype html>
<html>
<head>{{template "head"}}</head>
<body>
{{template "nav"}}
<h1>Media Types</h1>
<table>
  <tr><th>Name</th><th>Capacity (bytes)</th><th>ID prefix</th><th>Write kind</th></tr>
  {{range .}}<tr><td>{{.Name}}</td><td>{{.CapacityBytes}}</td><td>{{.IDPrefix}}</td><td>{{.WriteKind}}</td></tr>{{end}}
</table>
<form method="post" action="/config">
  <label>Name <input type="text" name="name" required></label>
  <label>Capacity bytes <input type="number" name="capacity_bytes" required></label>
  <label>ID prefix <input type="text" name="id_prefix" required placeholder="e.g. BD, RDX, USB"></label>
  <label>Write kind
    <select name="write_kind">
      <option value="optical">optical (burn via wodim)</option>
      <option value="filesystem">filesystem (plain copy)</option>
    </select>
  </label>
  <button type="submit">Add</button>
</form>
</body>
</html>
{{end}}
```

- [ ] **Step 3: Run tests to verify nothing broke**

Run: `go build ./... && go vet ./... && go test ./... -race`
Expected: PASS — `TestTemplates_ParseWithoutError` (from the staged-file-listing-and-metadata plan) catches any template syntax mistake in `config.html`.

- [ ] **Step 4: Commit**

```bash
git add internal/web/config.go internal/web/templates/config.html
git commit -m "$(cat <<'EOF'
Add ID prefix and write-kind fields to the Config page

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 3: Burn pipeline — `IDPrefix`/`WriteKind`/`TargetPath`, remove `mediaPrefix`

**Files:**
- Modify: `internal/burn/pipeline.go`
- Modify: `internal/burn/pipeline_test.go`
- Modify: `internal/web/handlers_test.go` (fixes its `burn.NewManager` call sites for the dropped `device` parameter)
- Modify: `internal/retrieve/integration_test.go` (fixes its `burn.NewManager` call site for the same reason)

- [ ] **Step 1: Write the failing tests**

Add to `internal/burn/pipeline_test.go`. This exercises the `write_kind =
"filesystem"` path end to end: `copyFile` is used instead of `BurnISO`,
`VerifyBurn` still runs (against the copied file, not a device), and a
`<diskID>.sha256` file is written alongside the copy.

```go
func TestManager_FilesystemWriteKind_CopiesAndVerifiesWithChecksumFile(t *testing.T) {
	staging := t.TempDir()
	writeStagingFile(t, staging, "a.bin", "hello")
	targetDir := t.TempDir()
	targetPath := filepath.Join(targetDir, "usb-target.iso")

	cat := newFakeCataloger()
	ex := fakeExecutorForHappyPath("") // devicePath unused for a filesystem-kind burn; wodim is asserted never called below

	mgr := NewManager(cat, ex, staging, t.TempDir(), t.TempDir())
	mgr.freeSpace = func(string) (uint64, error) { return 1 << 40, nil }

	opts := Options{
		MediaType:     "USB",
		CapacityBytes: 1000,
		ParityPercent: 10,
		IDPrefix:      "USB",
		WriteKind:     "filesystem",
		TargetPath:    targetPath,
	}
	if err := mgr.Start(context.Background(), opts); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := mgr.ContinueNextDisc(context.Background()); err != nil {
		t.Fatalf("ContinueNextDisc: %v", err)
	}

	job := mgr.Current()
	if job.State != StateDone {
		t.Fatalf("State = %s, want DONE (err=%v)", job.State, job.Err)
	}
	for _, call := range ex.Calls() {
		if call.Name == "wodim" {
			t.Fatal("expected wodim to never be called for a filesystem-write-kind burn")
		}
	}

	if len(cat.Disks) != 1 || cat.Disks[0].ID != "USB:0001" {
		t.Fatalf("Disks = %+v, want ID=USB:0001 (from IDPrefix)", cat.Disks)
	}

	isoBytes, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("reading copied target: %v", err)
	}
	if len(isoBytes) == 0 {
		t.Error("expected a non-empty copied ISO at targetPath")
	}

	sumBytes, err := os.ReadFile(targetPath + ".sha256")
	if err != nil {
		t.Fatalf("reading checksum file: %v", err)
	}
	wantHash, err := HashFile(targetPath)
	if err != nil {
		t.Fatalf("HashFile: %v", err)
	}
	wantLine := wantHash + "  usb-target.iso\n"
	if string(sumBytes) != wantLine {
		t.Errorf("checksum file = %q, want %q", sumBytes, wantLine)
	}
}

func TestPlanJob_UsesOptionsIDPrefixNotMediaTypeName(t *testing.T) {
	staging := t.TempDir()
	writeStagingFile(t, staging, "a.bin", "hello")
	cat := newFakeCataloger()

	opts := Options{MediaType: "Definitely Not A Known Media Type", CapacityBytes: 1000, ParityPercent: 10, IDPrefix: "RDX"}
	plans, err := planJob(context.Background(), staging, opts, cat)
	if err != nil {
		t.Fatalf("planJob: %v", err)
	}
	if len(plans) != 1 || plans[0].DiskID != "RDX:0001" {
		t.Fatalf("plans = %+v, want one plan with DiskID=RDX:0001 (from opts.IDPrefix, ignoring MediaType's name)", plans)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/burn/... -run 'TestManager_FilesystemWriteKind_CopiesAndVerifiesWithChecksumFile|TestPlanJob_UsesOptionsIDPrefixNotMediaTypeName' -v`
Expected: FAIL to compile — `Options.IDPrefix`/`WriteKind`/`TargetPath` don't exist yet, and `NewManager` still takes a `device` parameter this test doesn't pass.

- [ ] **Step 3: Add the new `Options` fields and delete `mediaPrefix`**

In `internal/burn/pipeline.go`, update `Options`:

```go
type Options struct {
	MediaType       string
	CapacityBytes   int64
	ParityPercent   int
	Compress        bool
	CrossDiscParity bool
	GroupSize       int
	DryRun          bool
	IDPrefix        string
	WriteKind       string // "optical" | "filesystem" — ignored when DryRun is set
	TargetPath      string // device path or filesystem target — ignored when DryRun is set
}
```

Delete the `mediaPrefix` function entirely. In `planJob`, replace:

```go
	prefix := mediaPrefix(opts.MediaType)
```

with:

```go
	prefix := opts.IDPrefix
```

- [ ] **Step 4: Remove `device` from `Manager`/`NewManager`**

Replace:

```go
type Manager struct {
	cat        Cataloger
	ex         execx.Executor
	stagingDir string
	spoolDir   string
	device     string
	dryRunDir  string
	freeSpace  func(path string) (uint64, error)

	mu  sync.Mutex
	job *Job
}

func NewManager(cat Cataloger, ex execx.Executor, stagingDir, spoolDir, device, dryRunDir string) *Manager {
	return &Manager{
		cat:        cat,
		ex:         ex,
		stagingDir: stagingDir,
		spoolDir:   spoolDir,
		device:     device,
		dryRunDir:  dryRunDir,
		freeSpace:  diskFreeBytes,
	}
}
```

with:

```go
type Manager struct {
	cat        Cataloger
	ex         execx.Executor
	stagingDir string
	spoolDir   string
	dryRunDir  string
	freeSpace  func(path string) (uint64, error)

	mu  sync.Mutex
	job *Job
}

func NewManager(cat Cataloger, ex execx.Executor, stagingDir, spoolDir, dryRunDir string) *Manager {
	return &Manager{
		cat:        cat,
		ex:         ex,
		stagingDir: stagingDir,
		spoolDir:   spoolDir,
		dryRunDir:  dryRunDir,
		freeSpace:  diskFreeBytes,
	}
}
```

- [ ] **Step 5: Dispatch `runDisc`'s write step on `WriteKind`, add the checksum file**

Replace:

```go
	m.setState(job, StateBurning)
	if job.Options.DryRun {
		if err := os.MkdirAll(m.dryRunDir, 0o755); err != nil {
			return fmt.Errorf("dry run dir: %w", err)
		}
		if err := copyFile(filepath.Join(m.dryRunDir, plan.DiskID+".iso"), isoPath); err != nil {
			return fmt.Errorf("dry run copy: %w", err)
		}
	} else {
		if err := BurnISO(ctx, m.ex, m.device, isoPath); err != nil {
			return fmt.Errorf("burn: %w", err)
		}

		m.setState(job, StateVerifying)
		if err := VerifyBurn(ctx, m.ex, m.device, isoHash, filepath.Join(isoDir, filepath.Base(tarPath))); err != nil {
			return fmt.Errorf("verify: %w", err)
		}
	}

	if err := m.commitDisc(ctx, job, plan, isoHash); err != nil {
		return fmt.Errorf("db commit: %w", err)
	}
```

with:

```go
	m.setState(job, StateBurning)
	switch {
	case job.Options.DryRun:
		if err := os.MkdirAll(m.dryRunDir, 0o755); err != nil {
			return fmt.Errorf("dry run dir: %w", err)
		}
		if err := copyFile(filepath.Join(m.dryRunDir, plan.DiskID+".iso"), isoPath); err != nil {
			return fmt.Errorf("dry run copy: %w", err)
		}
	case job.Options.WriteKind == "filesystem":
		if err := copyFile(job.Options.TargetPath, isoPath); err != nil {
			return fmt.Errorf("copy: %w", err)
		}
		m.setState(job, StateVerifying)
		if err := VerifyBurn(ctx, m.ex, job.Options.TargetPath, isoHash, filepath.Join(isoDir, filepath.Base(tarPath))); err != nil {
			return fmt.Errorf("verify: %w", err)
		}
		// Independently checkable without trusting the app: standard
		// sha256sum -c input, "<hash>  <filename>\n".
		checksumLine := isoHash + "  " + filepath.Base(job.Options.TargetPath) + "\n"
		if err := os.WriteFile(job.Options.TargetPath+".sha256", []byte(checksumLine), 0o644); err != nil {
			return fmt.Errorf("writing checksum file: %w", err)
		}
	default: // "optical"
		if err := BurnISO(ctx, m.ex, job.Options.TargetPath, isoPath); err != nil {
			return fmt.Errorf("burn: %w", err)
		}

		m.setState(job, StateVerifying)
		if err := VerifyBurn(ctx, m.ex, job.Options.TargetPath, isoHash, filepath.Join(isoDir, filepath.Base(tarPath))); err != nil {
			return fmt.Errorf("verify: %w", err)
		}
	}

	if err := m.commitDisc(ctx, job, plan, isoHash); err != nil {
		return fmt.Errorf("db commit: %w", err)
	}
```

- [ ] **Step 6: Fix every `burn.NewManager` call site across the whole module**

`NewManager` now takes 5 arguments instead of 6 (dropped `device`).
Update every call site:

- `internal/burn/pipeline_test.go` — every `NewManager(cat, ex, staging, spool_or_tempdir, device, dryRunDir_or_tempdir)` call (added by the dry-run plan) drops its `device` argument, e.g. `NewManager(cat, ex, staging, spool, device, dryRunDir)` becomes `NewManager(cat, ex, staging, spool, dryRunDir)`. The two new tests in Step 1 already show the target shape.
- `internal/web/handlers_test.go` — both `burn.NewManager(&stubCataloger{}, &execx.FakeExecutor{}, staging, t.TempDir(), t.TempDir()+"/device", t.TempDir())` calls (added by the dry-run plan) drop the `t.TempDir()+"/device"` argument.
- `internal/retrieve/integration_test.go` — the one `burn.NewManager(store, ex, staging, spool, device, t.TempDir())` call (added by the dry-run plan) drops `device`.

- [ ] **Step 7: Run tests to verify everything passes**

Run: `go build ./... && go vet ./... && go test ./internal/burn/... -race -v`
Expected: PASS — all existing burn tests plus both new tests from this
task. Also run `go vet ./internal/web/... ./internal/retrieve/...` to
confirm those two packages still at least compile (their own test
suites aren't fully exercised again until Tasks 4 and 5 touch them).

- [ ] **Step 8: Commit**

```bash
git add internal/burn/pipeline.go internal/burn/pipeline_test.go internal/web/handlers_test.go internal/retrieve/integration_test.go
git commit -m "$(cat <<'EOF'
Generalize the burn pipeline: per-burn TargetPath, WriteKind dispatch, remove mediaPrefix

mediaPrefix's hardcoded switch is replaced by Options.IDPrefix, resolved by the caller from media_types.id_prefix. Manager drops its fixed device field — burning now reads job.Options.TargetPath, and a WriteKind of "filesystem" reuses the dry-run plan's copyFile helper plus VerifyBurn unchanged (it already just hashes whatever path it's given), adding a standalone sha256sum-compatible checksum file alongside the copy.

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 4: Retrieve pipeline — remove `device`, add `targetPath` parameters

**Files:**
- Modify: `internal/retrieve/retrieve.go`
- Modify: `internal/retrieve/retrieve_test.go`
- Modify: `internal/retrieve/integration_test.go` (fixes its `retrieve.NewManager` call site)
- Modify: `internal/web/handlers_test.go` (fixes its `retrieve.NewManager` call sites)

- [ ] **Step 1: Write the failing test**

Add to `internal/retrieve/retrieve_test.go` (check the file for `fakeCatalog`/`srcAwareDiscExecutor`'s exact shapes first, already used by the dry-run plan's own tests in this file):

```go
func TestManager_ReadDisk_UsesCallerSuppliedTargetPath(t *testing.T) {
	retrievedDir := t.TempDir()
	scratchDir := t.TempDir()
	targetPath := filepath.Join(t.TempDir(), "usb-mount", "disc.iso")

	tocBytes, _ := toc.TOC{DiskID: "BD:0001", Compressed: false}.Marshal()
	tarPath := filepath.Join(t.TempDir(), "src.tar")
	writeTarFixture(t, tarPath, map[string]string{"photo.jpg": "bytes"})
	tarBytes, err := os.ReadFile(tarPath)
	if err != nil {
		t.Fatal(err)
	}

	cat := fakeCatalog{
		files: map[string]db.FileRecord{"file1": {ID: "file1", DiskID: "BD:0001", OriginalPath: "photo.jpg"}},
		disks: map[string]db.Disk{"BD:0001": {ID: "BD:0001", MediaType: "USB", Role: "data"}}, // not a dry run
	}
	ex := srcAwareDiscExecutor(targetPath, map[string][]byte{
		"/BD:0001.toc.json": tocBytes,
		"/BD:0001.tar":       tarBytes,
		"/BD:0001.tar.par2":  []byte("index"),
	})

	mgr := NewManager(cat, ex, retrievedDir, scratchDir, t.TempDir())
	if err := mgr.Start(context.Background(), "file1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := mgr.ReadDisk(context.Background(), targetPath); err != nil {
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
		t.Errorf("got %q, want %q", got, "bytes")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/retrieve/... -run TestManager_ReadDisk_UsesCallerSuppliedTargetPath -v`
Expected: FAIL to compile — `NewManager` still takes a `device` parameter this test doesn't pass, and `ReadDisk` doesn't take a `targetPath` argument yet.

- [ ] **Step 3: Remove `device` from `Manager`/`NewManager`, add `targetPath` to `ReadDisk`/`ReadReconstructionDisc`**

Replace:

```go
type Manager struct {
	cat          Catalog
	ex           execx.Executor
	retrievedDir string
	scratchDir   string
	device       string
	dryRunDir    string

	mu                     sync.Mutex
	job                    *Job
	reconstructor          *burn.Reconstructor
	reconstructionCapacity int64
}

func NewManager(cat Catalog, ex execx.Executor, retrievedDir, scratchDir, device, dryRunDir string) *Manager {
	return &Manager{cat: cat, ex: ex, retrievedDir: retrievedDir, scratchDir: scratchDir, device: device, dryRunDir: dryRunDir}
}

func (m *Manager) ReadDisk(ctx context.Context) error {
	m.mu.Lock()
	job := m.job
	if job == nil || job.State != StateAwaitingMedia {
		m.mu.Unlock()
		return fmt.Errorf("no retrieval awaiting media")
	}
	job.State = StateReadingDisc
	m.mu.Unlock()

	disk, err := m.cat.GetDisk(ctx, job.DiskID)
	if err != nil {
		return m.fail(job, err)
	}
	src := m.device
	if disk.IsDryRun {
		src = filepath.Join(m.dryRunDir, job.DiskID+".iso")
	}
	return m.readFrom(ctx, job, src)
}
```

with:

```go
type Manager struct {
	cat          Catalog
	ex           execx.Executor
	retrievedDir string
	scratchDir   string
	dryRunDir    string

	mu                     sync.Mutex
	job                    *Job
	reconstructor          *burn.Reconstructor
	reconstructionCapacity int64
}

func NewManager(cat Catalog, ex execx.Executor, retrievedDir, scratchDir, dryRunDir string) *Manager {
	return &Manager{cat: cat, ex: ex, retrievedDir: retrievedDir, scratchDir: scratchDir, dryRunDir: dryRunDir}
}

// ReadDisk is called once the user has entered the device/mount path for
// the disc named by the current job and clicked "Read Disk" — unless the
// disc is a dry-run disc, in which case targetPath is ignored: nothing
// needed inserting for a dry run, so there's nothing for the caller to
// have correctly supplied either.
func (m *Manager) ReadDisk(ctx context.Context, targetPath string) error {
	m.mu.Lock()
	job := m.job
	if job == nil || job.State != StateAwaitingMedia {
		m.mu.Unlock()
		return fmt.Errorf("no retrieval awaiting media")
	}
	job.State = StateReadingDisc
	m.mu.Unlock()

	disk, err := m.cat.GetDisk(ctx, job.DiskID)
	if err != nil {
		return m.fail(job, err)
	}
	src := targetPath
	if disk.IsDryRun {
		src = filepath.Join(m.dryRunDir, job.DiskID+".iso")
	}
	return m.readFrom(ctx, job, src)
}
```

Replace `ReadReconstructionDisc`'s signature and its `src` resolution
(the rest of the function — `foldReconstructionImage`, the `needsMore`
branch, the final `readFrom` call against the reconstructed image — is
unchanged):

```go
func (m *Manager) ReadReconstructionDisc(ctx context.Context, diskID string) error {
```

becomes:

```go
// ReadReconstructionDisc is called once the user has entered the
// device/mount path for the disc named by Remaining() and clicked
// "Read" — unless that member is a dry-run disc, in which case
// targetPath is ignored the same way ReadDisk ignores it.
func (m *Manager) ReadReconstructionDisc(ctx context.Context, diskID, targetPath string) error {
```

and its `src` resolution:

```go
	disk, err := m.cat.GetDisk(ctx, diskID)
	if err != nil {
		return m.revertToReconstructing(job, err)
	}
	src := m.device
	if disk.IsDryRun {
		src = filepath.Join(m.dryRunDir, diskID+".iso")
	}
```

becomes:

```go
	disk, err := m.cat.GetDisk(ctx, diskID)
	if err != nil {
		return m.revertToReconstructing(job, err)
	}
	src := targetPath
	if disk.IsDryRun {
		src = filepath.Join(m.dryRunDir, diskID+".iso")
	}
```

- [ ] **Step 4: Fix every `retrieve.NewManager` call site across the whole module, and every existing caller of `ReadDisk`/`ReadReconstructionDisc`**

- `internal/retrieve/retrieve_test.go` — every `NewManager(cat, ex, retrievedDir, scratchDir, device, dryRunDir)` call (added by the dry-run plan) drops `device`. Every existing call to `mgr.ReadDisk(context.Background())` becomes `mgr.ReadDisk(context.Background(), device)` where `device` is whatever local variable that test already declared for its device path (check each test — this project's existing tests consistently name it `device`, so the fix is purely additive: pass that same variable as the new second argument instead of removing it from the test). Every existing call to `mgr.ReadReconstructionDisc(context.Background(), diskID)` becomes `mgr.ReadReconstructionDisc(context.Background(), diskID, device)` the same way. (The two dry-run tests the previous plan added already ignore whatever's passed here when the disc `IsDryRun`, so passing the same `device` variable those tests already declare is correct and requires no new variables.)
- `internal/retrieve/integration_test.go` — the one `retrieve.NewManager(store, ex, retrievedDir, scratch, device, t.TempDir())` call drops `device`; its `retrieveMgr.ReadDisk(context.Background())` call becomes `retrieveMgr.ReadDisk(context.Background(), device)`; its `retrieveMgr.ReadReconstructionDisc(context.Background(), diskID)` calls (inside the loop over `remaining`) become `retrieveMgr.ReadReconstructionDisc(context.Background(), diskID, device)`.
- `internal/web/handlers_test.go` — both `retrieve.NewManager(stubCatalog{...}, ..., t.TempDir(), t.TempDir())` calls (added by the dry-run plan) drop their `device` argument the same way Task 3 already fixed their `burn.NewManager` counterparts.

- [ ] **Step 5: Run tests to verify everything passes**

Run: `go build ./... && go vet ./... && go test ./internal/retrieve/... -race -v && go test ./internal/web/... -race -v`
Expected: PASS — all existing retrieve and web tests plus the new test
from this task.

- [ ] **Step 6: Commit**

```bash
git add internal/retrieve/retrieve.go internal/retrieve/retrieve_test.go internal/retrieve/integration_test.go internal/web/handlers_test.go
git commit -m "$(cat <<'EOF'
Generalize retrieval: caller-supplied target path instead of a fixed device

ReadDisk and ReadReconstructionDisc each gain a targetPath parameter, used unless the disc in question is a dry run (that check, from the dry-run plan, still takes priority). Manager drops its fixed device field the same way burn.Manager did.

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 5: Web layer — target-path inputs, `startBurn`/`readDisk`/`readReconstructionDisc` wiring

**Files:**
- Modify: `internal/web/jobs.go`
- Modify: `internal/web/library.go`
- Modify: `internal/web/templates/dashboard.html`
- Modify: `internal/web/templates/library.html`

- [ ] **Step 1: Resolve `IDPrefix`/`WriteKind` and read `target_path` in `startBurn`**

In `internal/web/jobs.go`, replace `startBurn`'s media-type/capacity
resolution and `Options` construction:

```go
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
```

with:

```go
	mediaType := r.FormValue("media_type")
	mt, err := db.GetMediaType(r.Context(), s.pool, mediaType)
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
		CapacityBytes:   mt.CapacityBytes,
		ParityPercent:   parityPercent,
		Compress:        r.FormValue("compress") == "on",
		CrossDiscParity: r.FormValue("cross_disc_parity") == "on",
		GroupSize:       groupSize,
		DryRun:          r.FormValue("dry_run") == "on",
		IDPrefix:        mt.IDPrefix,
		WriteKind:       mt.WriteKind,
		TargetPath:      r.FormValue("target_path"),
	}
```

- [ ] **Step 2: Add the target-path input to the dashboard's Burn form**

In `internal/web/templates/dashboard.html`, add a target-path input to
the burn form (right before the "Dry run" checkbox the previous plan
added; the static default value matches `internal/config/config.go`'s
own `OPTICAL_DEVICE` fallback — a plain HTML default, always editable,
not threaded through any Go config value, since it's a UI convenience
only and every burn's actual target always comes from this field
regardless of what it starts out showing):

```html
  <label>Device / target path <input type="text" name="target_path" value="/dev/sr0"></label>
  <label><input type="checkbox" name="dry_run"> Dry run (produce an ISO only, don't burn)</label>
  <button type="submit">Burn</button>
```

(Replacing the existing two lines with these three.)

- [ ] **Step 3: Read `target_path` in `readDisk`/`readReconstructionDisc`**

In `internal/web/library.go`, replace:

```go
func (s *Server) readDisk(w http.ResponseWriter, r *http.Request) {
	// A NEEDS_RECONSTRUCTION (or FAILED) outcome is still a normal result
	// the status fragment below knows how to render, so an error here
	// doesn't fail the request — it's surfaced via job.Err in the fragment.
	_ = s.retrieveMgr.ReadDisk(r.Context())
	s.renderRetrieveStatus(w)
}
```

with:

```go
func (s *Server) readDisk(w http.ResponseWriter, r *http.Request) {
	// A NEEDS_RECONSTRUCTION (or FAILED) outcome is still a normal result
	// the status fragment below knows how to render, so an error here
	// doesn't fail the request — it's surfaced via job.Err in the fragment.
	_ = s.retrieveMgr.ReadDisk(r.Context(), r.FormValue("target_path"))
	s.renderRetrieveStatus(w)
}
```

Replace:

```go
func (s *Server) readReconstructionDisc(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	diskID := r.FormValue("disk_id")
	// Same reasoning as readDisk: the outcome (more discs needed, or a
	// failure) is rendered via job.Err in the status fragment, not as an
	// HTTP error.
	_ = s.retrieveMgr.ReadReconstructionDisc(r.Context(), diskID)
	s.renderRetrieveStatus(w)
}
```

with:

```go
func (s *Server) readReconstructionDisc(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	diskID := r.FormValue("disk_id")
	// Same reasoning as readDisk: the outcome (more discs needed, or a
	// failure) is rendered via job.Err in the status fragment, not as an
	// HTTP error.
	_ = s.retrieveMgr.ReadReconstructionDisc(r.Context(), diskID, r.FormValue("target_path"))
	s.renderRetrieveStatus(w)
}
```

- [ ] **Step 4: Add target-path inputs to the retrieval forms**

In `internal/web/templates/library.html`, replace the `retrieve-status`
fragment's two relevant forms:

```html
{{if eq .State "AWAITING_MEDIA"}}
  <form hx-post="/retrieve/read" hx-target="#retrieve-status"><button type="submit">Insert {{.DiskID}}, Read Disk</button></form>
{{end}}
```

with:

```html
{{if eq .State "AWAITING_MEDIA"}}
  <form hx-post="/retrieve/read" hx-target="#retrieve-status">
    <label>Device / target path <input type="text" name="target_path" value="/dev/sr0"></label>
    <button type="submit">Insert {{.DiskID}}, Read Disk</button>
  </form>
{{end}}
```

and:

```html
{{if eq .State "RECONSTRUCTING"}}
  {{range .Remaining}}
  <form hx-post="/retrieve/reconstruct/read" hx-target="#retrieve-status">
    <input type="hidden" name="disk_id" value="{{.}}">
    <button type="submit">Insert {{.}}, Read</button>
  </form>
  {{end}}
{{end}}
```

with:

```html
{{if eq .State "RECONSTRUCTING"}}
  {{range .Remaining}}
  <form hx-post="/retrieve/reconstruct/read" hx-target="#retrieve-status">
    <input type="hidden" name="disk_id" value="{{.}}">
    <label>Device / target path <input type="text" name="target_path" value="/dev/sr0"></label>
    <button type="submit">Insert {{.}}, Read</button>
  </form>
  {{end}}
{{end}}
```

- [ ] **Step 5: Run tests to verify everything passes**

Run: `go build ./... && go vet ./... && go test ./... -race`
Expected: PASS — `TestTemplates_ParseWithoutError` catches any template
syntax mistake, and `TestStagingFilesFragment_Renders`/`TestSearchRows_Renders`
continue to prove their own fragments still execute correctly (neither
touches the fragments this task modifies, so this is just a compile
-level check for those; no test in this project executes `retrieve-status`
specifically, matching the same established gap already noted for
`cover.html` — not something this task adds coverage for either).

- [ ] **Step 6: Commit**

```bash
git add internal/web/jobs.go internal/web/library.go internal/web/templates/dashboard.html internal/web/templates/library.html
git commit -m "$(cat <<'EOF'
Add target-path inputs to the burn and retrieval forms

startBurn resolves IDPrefix/WriteKind alongside capacity via the new db.GetMediaType, and reads the submitted target_path; readDisk/readReconstructionDisc do the same for retrieval.

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 6: Wire `main.go` — drop `device` from both manager constructions

**Files:**
- Modify: `cmd/archive-core/main.go`

- [ ] **Step 1: Update the manager construction calls**

Replace (the shape left by the dry-run plan):

```go
	burnMgr := burn.NewManager(burn.NewDBCataloger(pool), ex, cfg.StagingDir, cfg.SpoolDir, cfg.OpticalDevice, cfg.DryRunDir)
	retrieveMgr := retrieve.NewManager(retrieve.NewDBCatalog(pool), ex, cfg.RetrievedDir, scratchDir, cfg.OpticalDevice, cfg.DryRunDir)
```

with:

```go
	burnMgr := burn.NewManager(burn.NewDBCataloger(pool), ex, cfg.StagingDir, cfg.SpoolDir, cfg.DryRunDir)
	retrieveMgr := retrieve.NewManager(retrieve.NewDBCatalog(pool), ex, cfg.RetrievedDir, scratchDir, cfg.DryRunDir)
```

`cfg.OpticalDevice` is no longer read anywhere in `main.go` after this
change — leave the `OpticalDevice` field and `OPTICAL_DEVICE` env var in
`internal/config/config.go` as-is regardless (removing config surface
that a real deployment's Compose file/environment may already set is a
separate, riskier cleanup than this plan's scope; an unused struct field
is harmless).

- [ ] **Step 2: Confirm the module builds**

Run: `go build ./... && go vet ./...`
Expected: builds clean.

- [ ] **Step 3: Commit**

```bash
git add cmd/archive-core/main.go
git commit -m "$(cat <<'EOF'
Drop the fixed device argument from both manager constructions

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

## Plan Self-Review

**Spec coverage:**
- §2 (data model, `id_prefix`/`write_kind`): Task 1.
- §3 (`mediaPrefix` removal, `Options.IDPrefix`): Task 3.
- §4 (write-kind dispatch, per-burn `TargetPath`, checksum file, dashboard input): Task 3 (pipeline) + Task 5 (form/handler wiring).
- §5 (per-retrieval `TargetPath`, `IsDryRun` still taking priority, both `ReadDisk` and `ReadReconstructionDisc`): Task 4.
- §6 (Config page fields, dashboard input, Library retrieval inputs): Task 2 + Task 5.
- §7 (error handling falls out of `TargetPath` simply replacing `device` — no new error-handling code; `write_kind` validation at the Config page; test coverage for both the filesystem write path and caller-supplied target paths): Task 2 (validation) + Task 3 (filesystem-write test) + Task 4 (target-path test); the `NewManager`/caller sweep called out in §7 explicitly: Task 3 Step 6, Task 4 Step 4.
- §8 (non-goals): no tape/network mechanisms added; no per-disc target-path persistence added; reconstruction's XOR logic is untouched — only Task 4's `src` resolution changes, matching the dry-run plan's own precedent for the identical non-goal.

**Placeholder scan:** no TBD/TODO or vague instructions remain. Task 3 Step 6 and Task 4 Step 4 both describe a mechanical sweep across several files' existing test call sites rather than reproducing every single test function verbatim — as with the equivalent step in the dry-run plan, this isn't a hidden-design-work placeholder (the exact change — drop one argument, or add one argument threading an already-declared variable through — is fully specified, and Task 3/4's own Step 1 tests show the complete target shape), just a larger mechanical sweep than a single code block conveniently captures.

**Type consistency:** `Options.IDPrefix`/`WriteKind`/`TargetPath` (Task 3) are read consistently by `planJob` (Task 3), `runDisc` (Task 3), and constructed consistently by `startBurn` (Task 5) from `db.GetMediaType`'s `MediaType.IDPrefix`/`WriteKind` (Task 1). `retrieve.Manager.ReadDisk`/`ReadReconstructionDisc`'s new `targetPath` parameter (Task 4) is supplied consistently from `readDisk`/`readReconstructionDisc`'s `r.FormValue("target_path")` (Task 5) and from `library.html`'s new `target_path` form fields (Task 5). `burn.NewManager`/`retrieve.NewManager`'s dropped `device` parameter is removed consistently everywhere both constructors are called (Tasks 3, 4, 6), leaving no dangling references to a `device` field on either `Manager` struct.
