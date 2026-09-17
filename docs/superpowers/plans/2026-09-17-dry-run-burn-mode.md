# Dry-Run Burn Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a burn run as a "dry run" — the finished ISO is copied to a dedicated, WebDAV-mounted directory instead of being written to a physical disc, while still being cataloged (searchable, retrievable) exactly like a real disc, flagged so it's never mistaken for one, and deletable (ISO + catalog rows together) on demand.

**Architecture:** One new `disks.is_dry_run` column threads through the existing burn/retrieve state machines as a branch at the one point each already touches physical media (`runDisc`'s burn/verify step; `retrieve.Manager`'s disc-reading paths) — planning, packing, parity, and cataloging are completely unaffected. A new small web page manages dry-run discs' lifecycle (list + delete).

**Tech Stack:** Go 1.26+ stdlib, `pgx/v5`, `html/template` + HTMX (all already in use — no new dependencies).

**Depends on:** the existing full implementation (plans 00-04, plus the staged-file-listing-and-metadata plan). Additive throughout.

---

### Task 1: DB layer — `is_dry_run`, `DeleteDryRunDisc`, `ListDryRunDisks`

**Files:**
- Modify: `internal/db/schema.sql`
- Modify: `internal/db/disks.go`
- Modify: `internal/db/disks_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/db/disks_test.go` (check the file first for its existing
`TestDiskLifecycle`-style setup — a fresh unique `NextDiskID` prefix per
test avoids the project's known shared-dev-DB pollution issue):

```go
func TestInsertDisk_CarriesIsDryRun(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	if err := SeedMediaTypes(ctx, pool); err != nil {
		t.Fatalf("SeedMediaTypes: %v", err)
	}
	id, err := NextDiskID(ctx, pool, "BDDRYTEST", 0)
	if err != nil {
		t.Fatalf("NextDiskID: %v", err)
	}
	if err := InsertDisk(ctx, pool, Disk{ID: id, MediaType: "BD-R", ParityPercent: 10, Role: "data", IsDryRun: true}); err != nil {
		t.Fatalf("InsertDisk: %v", err)
	}

	got, err := GetDisk(ctx, pool, id)
	if err != nil {
		t.Fatalf("GetDisk: %v", err)
	}
	if !got.IsDryRun {
		t.Errorf("GetDisk.IsDryRun = false, want true")
	}
}

func TestDeleteDryRunDisc_RemovesDiskAndFileRows(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	if err := SeedMediaTypes(ctx, pool); err != nil {
		t.Fatalf("SeedMediaTypes: %v", err)
	}
	id, err := NextDiskID(ctx, pool, "BDDRYDEL", 0)
	if err != nil {
		t.Fatalf("NextDiskID: %v", err)
	}
	if err := InsertDisk(ctx, pool, Disk{ID: id, MediaType: "BD-R", ParityPercent: 10, Role: "data", IsDryRun: true}); err != nil {
		t.Fatalf("InsertDisk: %v", err)
	}
	if err := InsertFile(ctx, pool, FileRecord{DiskID: id, OriginalPath: "a.jpg", SizeBytes: 1}); err != nil {
		t.Fatalf("InsertFile: %v", err)
	}

	if err := DeleteDryRunDisc(ctx, pool, id); err != nil {
		t.Fatalf("DeleteDryRunDisc: %v", err)
	}

	if _, err := GetDisk(ctx, pool, id); err == nil {
		t.Error("expected the disk row to be gone after DeleteDryRunDisc")
	}
	var fileCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM files WHERE disk_id = $1`, id).Scan(&fileCount); err != nil {
		t.Fatalf("counting files: %v", err)
	}
	if fileCount != 0 {
		t.Errorf("file rows for %s = %d, want 0", id, fileCount)
	}
}

func TestDeleteDryRunDisc_RefusesARealDisc(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	if err := SeedMediaTypes(ctx, pool); err != nil {
		t.Fatalf("SeedMediaTypes: %v", err)
	}
	id, err := NextDiskID(ctx, pool, "BDDRYREAL", 0)
	if err != nil {
		t.Fatalf("NextDiskID: %v", err)
	}
	// IsDryRun left false: a real disc.
	if err := InsertDisk(ctx, pool, Disk{ID: id, MediaType: "BD-R", ParityPercent: 10, Role: "data"}); err != nil {
		t.Fatalf("InsertDisk: %v", err)
	}
	if err := InsertFile(ctx, pool, FileRecord{DiskID: id, OriginalPath: "a.jpg", SizeBytes: 1}); err != nil {
		t.Fatalf("InsertFile: %v", err)
	}

	if err := DeleteDryRunDisc(ctx, pool, id); err != nil {
		t.Fatalf("DeleteDryRunDisc: %v", err)
	}

	if _, err := GetDisk(ctx, pool, id); err != nil {
		t.Errorf("expected the real disc's row to survive DeleteDryRunDisc, got: %v", err)
	}
	var fileCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM files WHERE disk_id = $1`, id).Scan(&fileCount); err != nil {
		t.Fatalf("counting files: %v", err)
	}
	if fileCount != 1 {
		t.Errorf("file rows for real disc %s = %d, want 1 (untouched)", id, fileCount)
	}
}

func TestListDryRunDisks_OnlyReturnsDryRunDiscs(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	if err := SeedMediaTypes(ctx, pool); err != nil {
		t.Fatalf("SeedMediaTypes: %v", err)
	}
	dryID, err := NextDiskID(ctx, pool, "BDLISTDRY", 0)
	if err != nil {
		t.Fatalf("NextDiskID: %v", err)
	}
	if err := InsertDisk(ctx, pool, Disk{ID: dryID, MediaType: "BD-R", ParityPercent: 10, Role: "data", IsDryRun: true}); err != nil {
		t.Fatalf("InsertDisk (dry run): %v", err)
	}
	realID, err := NextDiskID(ctx, pool, "BDLISTREAL", 0)
	if err != nil {
		t.Fatalf("NextDiskID: %v", err)
	}
	if err := InsertDisk(ctx, pool, Disk{ID: realID, MediaType: "BD-R", ParityPercent: 10, Role: "data"}); err != nil {
		t.Fatalf("InsertDisk (real): %v", err)
	}

	discs, err := ListDryRunDisks(ctx, pool)
	if err != nil {
		t.Fatalf("ListDryRunDisks: %v", err)
	}
	foundDry, foundReal := false, false
	for _, d := range discs {
		if d.ID == dryID {
			foundDry = true
		}
		if d.ID == realID {
			foundReal = true
		}
	}
	if !foundDry {
		t.Errorf("expected %s (dry run) in ListDryRunDisks", dryID)
	}
	if foundReal {
		t.Errorf("expected %s (real) NOT in ListDryRunDisks", realID)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `docker compose up -d archive-db && DATABASE_URL='postgres://varis:varis@localhost:5432/varis?sslmode=disable' go test ./internal/db/... -run 'TestInsertDisk_CarriesIsDryRun|TestDeleteDryRunDisc|TestListDryRunDisks' -v`
Expected: FAIL to compile — `Disk.IsDryRun`, `DeleteDryRunDisc`, `ListDryRunDisks` don't exist yet.

- [ ] **Step 3: Add the schema column**

Append to `internal/db/schema.sql`:

```sql
ALTER TABLE disks ADD COLUMN IF NOT EXISTS is_dry_run BOOLEAN NOT NULL DEFAULT false;
```

- [ ] **Step 4: Update `internal/db/disks.go`**

Add `IsDryRun bool` to the `Disk` struct:

```go
type Disk struct {
	ID            string
	MediaType     string
	ParityPercent int
	GroupID       *string
	Role          string // "data" | "parity"
	SlotIndex     *int
	ISOHash       string
	CreatedAt     time.Time
	IsDryRun      bool
}
```

Update `InsertDisk`:

```go
func InsertDisk(ctx context.Context, pool *pgxpool.Pool, d Disk) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO disks (id, media_type, parity_percent, group_id, role, slot_index, iso_hash, is_dry_run)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		d.ID, d.MediaType, d.ParityPercent, d.GroupID, d.Role, d.SlotIndex, d.ISOHash, d.IsDryRun)
	return err
}
```

Update `GetDisk`:

```go
func GetDisk(ctx context.Context, pool *pgxpool.Pool, id string) (Disk, error) {
	var d Disk
	err := pool.QueryRow(ctx,
		`SELECT id, media_type, parity_percent, group_id, role, slot_index, iso_hash, created_at, is_dry_run
		 FROM disks WHERE id = $1`, id).
		Scan(&d.ID, &d.MediaType, &d.ParityPercent, &d.GroupID, &d.Role, &d.SlotIndex, &d.ISOHash, &d.CreatedAt, &d.IsDryRun)
	if errors.Is(err, pgx.ErrNoRows) {
		return Disk{}, fmt.Errorf("disk %s not found", id)
	}
	return d, err
}
```

Update `GroupMembers`:

```go
func GroupMembers(ctx context.Context, pool *pgxpool.Pool, groupID string) ([]Disk, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, media_type, parity_percent, group_id, role, slot_index, iso_hash, created_at, is_dry_run
		 FROM disks WHERE group_id = $1 ORDER BY slot_index`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Disk
	for rows.Next() {
		var d Disk
		if err := rows.Scan(&d.ID, &d.MediaType, &d.ParityPercent, &d.GroupID, &d.Role, &d.SlotIndex, &d.ISOHash, &d.CreatedAt, &d.IsDryRun); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
```

Add `DeleteDryRunDisc` and `ListDryRunDisks` to the same file:

```go
// DeleteDryRunDisc removes a dry-run disc's file and disk rows together.
// Scoped to is_dry_run = true throughout, so calling it with a real disc's
// ID is a safe no-op, never data loss. Files are deleted before the disk
// row (not wrapped in a transaction — this project doesn't use them
// elsewhere): if the process dies between the two statements, the worst
// case is an orphaned disk row with no files, which a second call to
// DeleteDryRunDisc cleans up (its own file-delete is a no-op, and the
// disk row still matches is_dry_run = true).
func DeleteDryRunDisc(ctx context.Context, pool *pgxpool.Pool, diskID string) error {
	_, err := pool.Exec(ctx,
		`DELETE FROM files WHERE disk_id = $1 AND EXISTS (
		     SELECT 1 FROM disks WHERE id = $1 AND is_dry_run
		 )`, diskID)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `DELETE FROM disks WHERE id = $1 AND is_dry_run`, diskID)
	return err
}

// ListDryRunDisks returns every disc flagged is_dry_run, newest first, for
// the /dryruns management page.
func ListDryRunDisks(ctx context.Context, pool *pgxpool.Pool) ([]Disk, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, media_type, parity_percent, group_id, role, slot_index, iso_hash, created_at, is_dry_run
		 FROM disks WHERE is_dry_run ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Disk
	for rows.Next() {
		var d Disk
		if err := rows.Scan(&d.ID, &d.MediaType, &d.ParityPercent, &d.GroupID, &d.Role, &d.SlotIndex, &d.ISOHash, &d.CreatedAt, &d.IsDryRun); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `DATABASE_URL='postgres://varis:varis@localhost:5432/varis?sslmode=disable' go test ./internal/db/... -v`
Expected: PASS (all `internal/db` tests)

- [ ] **Step 6: Confirm the rest of the module still builds**

Run: `go build ./...`
Expected: builds clean — `Disk{...}` is constructed with keyed fields everywhere in this codebase (verified: `internal/burn/pipeline.go`, `internal/burn/reconstruct_test.go`, `internal/retrieve/retrieve_test.go`, `internal/retrieve/integration_test.go`, `internal/web/handlers_test.go` all use keyed literals), so adding a trailing field is compatible.

- [ ] **Step 7: Commit**

```bash
git add internal/db/schema.sql internal/db/disks.go internal/db/disks_test.go
git commit -m "$(cat <<'EOF'
Add disks.is_dry_run and DeleteDryRunDisc/ListDryRunDisks

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 2: Config — `DRYRUN_DIR`

**Files:**
- Modify: `internal/config/config.go`

- [ ] **Step 1: Add the new field and env var**

Replace the contents of `internal/config/config.go`:

```go
package config

import (
	"cmp"
	"os"
)

type Config struct {
	DatabaseURL   string
	StagingDir    string
	SpoolDir      string
	RetrievedDir  string
	DryRunDir     string
	HTTPAddr      string
	OpticalDevice string
}

func Load() Config {
	return Config{
		DatabaseURL:   cmp.Or(os.Getenv("DATABASE_URL"), "postgres://varis:varis@archive-db:5432/varis?sslmode=disable"),
		StagingDir:    cmp.Or(os.Getenv("STAGING_DIR"), "/data/staging"),
		SpoolDir:      cmp.Or(os.Getenv("SPOOL_DIR"), "/data/spool"),
		RetrievedDir:  cmp.Or(os.Getenv("RETRIEVED_DIR"), "/data/retrieved"),
		DryRunDir:     cmp.Or(os.Getenv("DRYRUN_DIR"), "/data/dryrun"),
		HTTPAddr:      cmp.Or(os.Getenv("HTTP_ADDR"), ":8080"),
		OpticalDevice: cmp.Or(os.Getenv("OPTICAL_DEVICE"), "/dev/sr0"),
	}
}
```

- [ ] **Step 2: Run tests to verify nothing broke**

Run: `go build ./... && go vet ./...`
Expected: builds clean. (Check `internal/config` for an existing test file — if `config_test.go` exists, run `go test ./internal/config/...` too and confirm it still passes; if it asserts the exact field list of `Config{}`, it may need `DryRunDir` added to its expectations.)

- [ ] **Step 3: Commit**

```bash
git add internal/config/config.go
git commit -m "$(cat <<'EOF'
Add DRYRUN_DIR configuration

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 3: Burn pipeline — `Options.DryRun`

**Files:**
- Modify: `internal/burn/pipeline.go`
- Modify: `internal/burn/pipeline_test.go`
- Modify: `internal/web/handlers_test.go` (calls `burn.NewManager` directly, twice)
- Modify: `internal/retrieve/integration_test.go` (calls `burn.NewManager` directly, once)

- [ ] **Step 1: Write the failing tests**

Add to `internal/burn/pipeline_test.go`. First check the file for its
`fakeExecutorForHappyPath(devicePath string) *execx.FakeExecutor` helper's
exact shape (it registers `Funcs` for `"par2create"`, `"xorriso"`,
`"wodim"`, `"par2verify"`) — a dry-run test needs a fake executor that has
NO `"wodim"` entry at all, so a call to `wodim` would return the
`FakeExecutor`'s zero-value `Result{}` (nil error) if it somehow still got
called, defeating the test's own purpose unless the test also asserts
`wodim` was never invoked via `Calls()`:

```go
func TestManager_DryRun_CopiesISOInsteadOfBurning(t *testing.T) {
	staging := t.TempDir()
	writeStagingFile(t, staging, "a.bin", "hello")
	dryRunDir := t.TempDir()

	cat := newFakeCataloger()
	ex := fakeExecutorForHappyPath(filepath.Join(t.TempDir(), "device")) // registers par2create/xorriso/par2verify; wodim would just no-op if called, so we separately assert it never is

	mgr := NewManager(cat, ex, staging, t.TempDir(), t.TempDir()+"/device")
	mgr.dryRunDir = dryRunDir
	mgr.freeSpace = func(string) (uint64, error) { return 1 << 40, nil }

	opts := Options{MediaType: "BD-R", CapacityBytes: 1000, ParityPercent: 10, DryRun: true}
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
			t.Fatal("expected wodim to never be called for a dry-run burn")
		}
	}

	if len(cat.Disks) != 1 {
		t.Fatalf("Disks = %+v, want exactly 1", cat.Disks)
	}
	if !cat.Disks[0].IsDryRun {
		t.Error("expected the committed disk to have IsDryRun = true")
	}

	isoPath := filepath.Join(dryRunDir, cat.Disks[0].ID+".iso")
	if _, err := os.Stat(isoPath); err != nil {
		t.Errorf("expected the dry-run ISO at %s: %v", isoPath, err)
	}

	if len(cat.Files) != 1 || cat.Files[0].OriginalPath != "a.bin" {
		t.Errorf("Files = %+v, want exactly a.bin committed", cat.Files)
	}
	if _, err := os.Stat(filepath.Join(staging, "a.bin")); !os.IsNotExist(err) {
		t.Error("expected a.bin to be removed from staging after a dry-run burn, same as a real one")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/burn/... -run TestManager_DryRun_CopiesISOInsteadOfBurning -v`
Expected: FAIL to compile — `Options.DryRun` and `Manager.dryRunDir` don't exist yet.

- [ ] **Step 3: Add `DryRun` to `Options` and `dryRunDir` to `Manager`**

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
}
```

Update `Manager` and `NewManager`:

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

(This changes `NewManager`'s signature — every call site needs updating;
Task 3 Step 6 below fixes the test call sites in this same file, and Task 5
fixes `cmd/archive-core/main.go`.)

- [ ] **Step 4: Add a small file-copy helper and branch `runDisc`**

Add `"io"` to `pipeline.go`'s import list. Add this helper near the other
small file-manipulation helpers (`removeStaleArtifacts`, `movePathsInto`):

```go
// copyFile copies srcPath's contents to destPath via io.Copy (not
// os.ReadFile/os.WriteFile) since a real ISO can be tens of gigabytes —
// loading the whole thing into memory would be wasteful at best.
func copyFile(destPath, srcPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		return err
	}
	return dst.Close()
}
```

In `runDisc`, replace:

```go
	m.setState(job, StateBurning)
	if err := BurnISO(ctx, m.ex, m.device, isoPath); err != nil {
		return fmt.Errorf("burn: %w", err)
	}

	m.setState(job, StateVerifying)
	if err := VerifyBurn(ctx, m.ex, m.device, isoHash, filepath.Join(isoDir, filepath.Base(tarPath))); err != nil {
		return fmt.Errorf("verify: %w", err)
	}

	if err := m.commitDisc(ctx, job, plan, isoHash); err != nil {
		return fmt.Errorf("db commit: %w", err)
	}
```

with:

```go
	m.setState(job, StateBurning)
	if job.Options.DryRun {
		// No physical media, so nothing to verify against — the file just
		// copied here is exactly what gets cataloged, with no read-back
		// round trip to introduce a discrepancy. StateVerifying is skipped
		// entirely rather than reused, since there's genuinely no
		// verification step happening.
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

- [ ] **Step 5: Fold `DryRun` into `commitDisc`'s `db.Disk`**

In `commitDisc`, add `IsDryRun: job.Options.DryRun,` to the `db.Disk{...}`
literal:

```go
	err := m.cat.InsertDisk(ctx, db.Disk{
		ID:            plan.DiskID,
		MediaType:     job.Options.MediaType,
		ParityPercent: job.Options.ParityPercent,
		GroupID:       groupID,
		Role:          plan.Role,
		SlotIndex:     slotIndex,
		ISOHash:       isoHash,
		IsDryRun:      job.Options.DryRun,
	})
```

- [ ] **Step 6: Fix every `burn.NewManager` call site across the whole module**

`NewManager` now takes a 6th argument (`dryRunDir string`). Three files
outside this task's own package call it directly and will fail to
compile otherwise — this is exactly the kind of cross-package breakage
this project's plans have hit before whenever a constructor signature
changes (see the `Cataloger` interface-widening precedent from an
earlier plan), so fix all of them in this same commit:

- `internal/burn/pipeline_test.go` — every `NewManager(cat, ex, staging, spool_or_tempdir, device)` call (about ten of them) needs a trailing dry-run directory added, e.g. `t.TempDir()`. The test just added in Step 1 already shows the target shape (`NewManager(cat, ex, staging, t.TempDir(), t.TempDir()+"/device", dryRunDir)` — once `NewManager` takes the parameter directly, remove that test's now-redundant `mgr.dryRunDir = dryRunDir` line and pass `dryRunDir` as the 6th constructor argument instead). None of the other existing tests in this file exercise dry-run behavior, so the exact directory value doesn't matter for them — just add one.
- `internal/web/handlers_test.go` — two calls: `burn.NewManager(&stubCataloger{}, &execx.FakeExecutor{}, staging, t.TempDir(), t.TempDir()+"/device")` appears twice (in `TestStartBurn_RejectedWhileRetrievalInProgress` and `TestStartRetrieve_RejectedWhileBurnInProgress`). Add a trailing `t.TempDir()` to both.
- `internal/retrieve/integration_test.go` — one call: `burn.NewManager(store, ex, staging, spool, device)` in `TestEndToEnd_BurnGroupLoseADiscReconstruct`. Add a trailing `t.TempDir()` (this test doesn't exercise dry-run, so any directory works).

- [ ] **Step 7: Run tests to verify everything passes**

Run: `go build ./... && go vet ./... && go test ./internal/burn/... -race -v`
Expected: PASS — all existing burn tests (updated `NewManager` call
sites) plus the new dry-run test. `go build ./...` covers the two other
packages' call sites too, though their own test suites aren't run again
until Tasks 4 and 8 touch those packages — run
`go vet ./internal/web/... ./internal/retrieve/...` here as well to
confirm they at least still compile.

- [ ] **Step 8: Commit**

```bash
git add internal/burn/pipeline.go internal/burn/pipeline_test.go internal/web/handlers_test.go internal/retrieve/integration_test.go
git commit -m "$(cat <<'EOF'
Add dry-run mode to the burn pipeline: copy the ISO instead of burning it

burn.NewManager's signature change is fixed up in every calling file in this same commit (internal/web/handlers_test.go, internal/retrieve/integration_test.go) so the module keeps compiling throughout.

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 4: Retrieve pipeline — read from the dry-run path

This closes two related gaps: normal retrieval (`ReadDisk`) of a file on a
dry-run disc, and — a case easy to miss, since the design spec's own
non-goals section originally assumed this needed no extra work —
reconstruction (`ReadReconstructionDisc`) needing to read a dry-run
disc's bytes when it's one of a group's *surviving* members, not the
disc being recovered. Both currently hardcode `m.device` as the only
possible source; both need the identical "resolve `src` by checking
`disk.IsDryRun`" treatment.

**Files:**
- Modify: `internal/retrieve/retrieve.go`
- Modify: `internal/retrieve/retrieve_test.go`
- Modify: `internal/retrieve/integration_test.go` (calls `retrieve.NewManager` directly, once, in addition to the `burn.NewManager` call Task 3 already fixed there)
- Modify: `internal/web/handlers_test.go` (calls `retrieve.NewManager` directly, twice, in addition to the `burn.NewManager` calls Task 3 already fixed there)

- [ ] **Step 1: Write the failing test for `ReadDisk`**

Check `internal/retrieve/retrieve_test.go` for `fakeCatalog`'s exact shape
and `srcAwareDiscExecutor`'s exact shape first (both already exist). Add:

```go
func TestManager_ReadDisk_DryRunDisc_ReadsFromDryRunPath(t *testing.T) {
	retrievedDir := t.TempDir()
	scratchDir := t.TempDir()
	dryRunDir := t.TempDir()
	device := filepath.Join(t.TempDir(), "device") // never touched in this test

	dryRunPath := filepath.Join(dryRunDir, "BD:0001.iso")
	tocBytes, _ := toc.TOC{DiskID: "BD:0001", Compressed: false}.Marshal()
	tarPath := filepath.Join(t.TempDir(), "src.tar")
	writeTarFixture(t, tarPath, map[string]string{"photo.jpg": "bytes"})
	tarBytes, err := os.ReadFile(tarPath)
	if err != nil {
		t.Fatal(err)
	}

	cat := fakeCatalog{
		files: map[string]db.FileRecord{"file1": {ID: "file1", DiskID: "BD:0001", OriginalPath: "photo.jpg"}},
		disks: map[string]db.Disk{"BD:0001": {ID: "BD:0001", MediaType: "BD-R", Role: "data", IsDryRun: true}},
	}
	ex := srcAwareDiscExecutor(dryRunPath, map[string][]byte{
		"/BD:0001.toc.json": tocBytes,
		"/BD:0001.tar":       tarBytes,
		"/BD:0001.tar.par2":  []byte("index"),
	})

	mgr := NewManager(cat, ex, retrievedDir, scratchDir, device, dryRunDir)
	if err := mgr.Start(context.Background(), "file1"); err != nil {
		t.Fatalf("Start: %v", err)
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
		t.Errorf("got %q, want %q", got, "bytes")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/retrieve/... -run TestManager_ReadDisk_DryRunDisc -v`
Expected: FAIL to compile — `NewManager` doesn't take a `dryRunDir`
parameter yet, and `db.Disk` has no `IsDryRun` field the test can set
(it does, from Task 1 — but `NewManager`'s signature is what breaks
compilation here).

- [ ] **Step 3: Add `dryRunDir` to `retrieve.Manager` and branch `ReadDisk`**

In `internal/retrieve/retrieve.go`, update `Manager` and `NewManager`:

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
```

Replace `ReadDisk`:

```go
// ReadDisk is called once the user has inserted the disc named by the
// current job and clicked "Read Disk" — or, for a dry-run disc, is called
// with nothing to insert at all; its bytes are read straight from
// dryRunDir instead of the physical device.
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
		// Unlike readFrom's own failure paths, there was no read attempt
		// here to blame on damaged media — the catalog lookup itself
		// failed — so this is a hard failure, not something reconstruction
		// could ever fix.
		return m.fail(job, err)
	}
	src := m.device
	if disk.IsDryRun {
		src = filepath.Join(m.dryRunDir, job.DiskID+".iso")
	}
	return m.readFrom(ctx, job, src)
}
```

- [ ] **Step 4: Run the `ReadDisk` test to verify it passes**

Run: `go test ./internal/retrieve/... -run TestManager_ReadDisk_DryRunDisc -v`
Expected: PASS

- [ ] **Step 5: Write the failing test for `ReadReconstructionDisc`**

Add to `internal/retrieve/retrieve_test.go`. This mirrors
`TestManager_ReconstructionFlow_HappyPath` (check that test for its exact
group/fixture setup), but the surviving parity disc is flagged
`IsDryRun: true` and its bytes are placed at the dry-run path instead of
being written to `device`:

```go
func TestManager_ReadReconstructionDisc_DryRunMemberReadsFromDryRunPath(t *testing.T) {
	retrievedDir := t.TempDir()
	scratchDir := t.TempDir()
	dryRunDir := t.TempDir()
	device := filepath.Join(t.TempDir(), "device")

	groupID := "g1"
	cat := fakeCatalog{
		files: map[string]db.FileRecord{
			"file1": {ID: "file1", DiskID: "BD:0001", OriginalPath: "photo.jpg"},
		},
		disks: map[string]db.Disk{
			"BD:0001": {ID: "BD:0001", MediaType: "BD-R", Role: "data", GroupID: &groupID, SlotIndex: intPtr(0)},
			"BD:0002": {ID: "BD:0002", MediaType: "BD-R", Role: "parity", GroupID: &groupID, SlotIndex: intPtr(1), IsDryRun: true},
		},
	}

	tocBytes, _ := toc.TOC{DiskID: "BD:0001", Compressed: false}.Marshal()
	tarPath := filepath.Join(t.TempDir(), "src.tar")
	writeTarFixture(t, tarPath, map[string]string{"photo.jpg": "bytes"})
	tarBytes, err := os.ReadFile(tarPath)
	if err != nil {
		t.Fatal(err)
	}

	reconstructedPath := filepath.Join(scratchDir, "BD:0001.reconstructed.iso")
	dryRunParityPath := filepath.Join(dryRunDir, "BD:0002.iso")
	ex := srcAwareDiscExecutor(reconstructedPath, map[string][]byte{
		"/BD:0001.toc.json": tocBytes,
		"/BD:0001.tar":       tarBytes,
		"/BD:0001.tar.par2":  []byte("index"),
	})

	mgr := NewManager(cat, ex, retrievedDir, scratchDir, device, dryRunDir)
	if err := mgr.Start(context.Background(), "file1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := os.WriteFile(device, []byte("garbage, not the real disc"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mgr.ReadDisk(context.Background()); err == nil {
		t.Fatal("expected ReadDisk against garbage to fail")
	}
	if mgr.Current().State != StateNeedsReconstruction {
		t.Fatalf("State = %s, want NEEDS_RECONSTRUCTION", mgr.Current().State)
	}
	if err := mgr.StartReconstruction(context.Background()); err != nil {
		t.Fatalf("StartReconstruction: %v", err)
	}

	// BD:0002 (the parity disc) is the only remaining member, and it's a
	// dry-run disc: its bytes live at dryRunParityPath, not at device.
	if err := os.WriteFile(dryRunParityPath, []byte("whatever bytes the srcAwareDiscExecutor passthrough will read"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mgr.ReadReconstructionDisc(context.Background(), "BD:0002"); err != nil {
		t.Fatalf("ReadReconstructionDisc: %v", err)
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

- [ ] **Step 6: Run it to verify it fails**

Run: `go test ./internal/retrieve/... -run TestManager_ReadReconstructionDisc_DryRunMember -v`
Expected: FAIL — `ReadReconstructionDisc` still hardcodes `m.device` as
the only source, so it tries to read `BD:0002`'s bytes from `device`
(never written for this disc in this test) instead of `dryRunParityPath`.

- [ ] **Step 7: Branch `ReadReconstructionDisc`'s source resolution**

In `ReadReconstructionDisc`, right after the existing
`disk, err := m.cat.GetDisk(ctx, diskID)` call (already present — it's
already used to check `disk.Role`), resolve `src` the same way `ReadDisk`
now does, and use `src` instead of the hardcoded `m.device` in both
branches below it:

```go
	disk, err := m.cat.GetDisk(ctx, diskID)
	if err != nil {
		return m.revertToReconstructing(job, err)
	}
	src := m.device
	if disk.IsDryRun {
		src = filepath.Join(m.dryRunDir, diskID+".iso")
	}

	imagePath := filepath.Join(m.scratchDir, diskID+".img")
	if disk.Role == "parity" {
		if err := burn.ExtractFromDisc(ctx, m.ex, src, "/"+diskID+".tar", imagePath); err != nil {
			return m.revertToReconstructing(job, err)
		}
	} else if err := burn.ReadRawImage(src, imagePath, capacity); err != nil {
		return m.revertToReconstructing(job, err)
	}
```

(`burn.ReadRawImage`'s first argument was already just a plain file path
under the hood — nothing about it assumes a real device node — so this
works identically for a dry-run ISO file.)

- [ ] **Step 8: Fix every `retrieve.NewManager` call site across the whole module**

Same situation as Task 3 Step 6, for `retrieve.NewManager`'s new 6th
argument:

- `internal/retrieve/retrieve_test.go` — five `NewManager(cat, ex, retrievedDir, scratchDir, device)` calls; add a trailing `t.TempDir()` to each (none of the other existing tests in this file exercise dry-run behavior).
- `internal/retrieve/integration_test.go` — one call, `NewManager(store, ex, retrievedDir, scratch, device)` in `TestEndToEnd_BurnGroupLoseADiscReconstruct`; add a trailing `t.TempDir()`.
- `internal/web/handlers_test.go` — two `retrieve.NewManager(stubCatalog{...}, ...)` calls (in the same two tests Task 3 Step 6 already touched for their `burn.NewManager` calls); add a trailing `t.TempDir()` to each.

- [ ] **Step 9: Run tests to verify everything passes**

Run: `go build ./... && go vet ./... && go test ./internal/retrieve/... -race -v && go test ./internal/web/... -race -v`
Expected: PASS — all existing retrieve and web tests (both packages'
`NewManager` call sites now fixed) plus both new dry-run tests from this
task.

- [ ] **Step 10: Commit**

```bash
git add internal/retrieve/retrieve.go internal/retrieve/retrieve_test.go internal/retrieve/integration_test.go internal/web/handlers_test.go
git commit -m "$(cat <<'EOF'
Read dry-run discs from their file path instead of the physical device

Covers both normal retrieval (ReadDisk) and reconstruction reads of a dry-run disc that's a surviving group member (ReadReconstructionDisc) — the latter is easy to miss, since it's a different code path from the disc actually being recovered. retrieve.NewManager's signature change is fixed up in every calling file in this same commit so the module keeps compiling throughout.

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 5: Wire `main.go` — WebDAV mount + `dryRunDir` threading

**Files:**
- Modify: `cmd/archive-core/main.go`

- [ ] **Step 1: Update the manager construction and add the WebDAV mount**

Replace:

```go
	burnMgr := burn.NewManager(burn.NewDBCataloger(pool), ex, cfg.StagingDir, cfg.SpoolDir, cfg.OpticalDevice)
	retrieveMgr := retrieve.NewManager(retrieve.NewDBCatalog(pool), ex, cfg.RetrievedDir, scratchDir, cfg.OpticalDevice)
```

with:

```go
	burnMgr := burn.NewManager(burn.NewDBCataloger(pool), ex, cfg.StagingDir, cfg.SpoolDir, cfg.OpticalDevice, cfg.DryRunDir)
	retrieveMgr := retrieve.NewManager(retrieve.NewDBCatalog(pool), ex, cfg.RetrievedDir, scratchDir, cfg.OpticalDevice, cfg.DryRunDir)
```

Replace:

```go
	mux.Handle("/webdav/staging/", http.StripPrefix("/webdav/staging", webdav.Handler("/", cfg.StagingDir)))
	mux.Handle("/webdav/retrieved/", http.StripPrefix("/webdav/retrieved", webdav.Handler("/", cfg.RetrievedDir)))
```

with:

```go
	mux.Handle("/webdav/staging/", http.StripPrefix("/webdav/staging", webdav.Handler("/", cfg.StagingDir)))
	mux.Handle("/webdav/retrieved/", http.StripPrefix("/webdav/retrieved", webdav.Handler("/", cfg.RetrievedDir)))
	mux.Handle("/webdav/dryrun/", http.StripPrefix("/webdav/dryrun", webdav.Handler("/", cfg.DryRunDir)))
```

- [ ] **Step 2: Confirm the module builds**

Run: `go build ./...`
Expected: builds clean (there's no test file for `cmd/archive-core`, so
this is the verification step for this task).

- [ ] **Step 3: Commit**

```bash
git add cmd/archive-core/main.go
git commit -m "$(cat <<'EOF'
Mount /webdav/dryrun/ and thread DryRunDir into both managers

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 6: Dashboard + Jobs UI — dry-run checkbox, badge, wording

**Files:**
- Modify: `internal/web/jobs.go`
- Modify: `internal/web/jobs_test.go`
- Modify: `internal/web/templates/dashboard.html`
- Modify: `internal/web/templates/jobs.html`

- [ ] **Step 1: Write the failing test for `jobViewFromJob`**

Add to `internal/web/jobs_test.go`, alongside the existing
`TestJobViewFromJob_IndexClamp` table test:

```go
func TestJobViewFromJob_ExposesDryRun(t *testing.T) {
	job := &burn.Job{
		Options:      burn.Options{DryRun: true},
		Plans:        []burn.DiscPlan{{DiskID: "BD:0001"}},
		CurrentIndex: 0,
		State:        burn.StateAwaitingDisc,
	}
	got := jobViewFromJob(job)
	if !got.DryRun {
		t.Error("expected jobViewFromJob to expose DryRun = true from job.Options.DryRun")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/web/... -run TestJobViewFromJob_ExposesDryRun -v`
Expected: FAIL to compile — `jobView` has no `DryRun` field yet.

- [ ] **Step 3: Add `DryRun` to `jobView` and `startBurn`'s `Options`**

In `internal/web/jobs.go`, update `jobView` and `jobViewFromJob`:

```go
type jobView struct {
	State         burn.State
	Err           error
	CurrentIndex1 int
	TotalDiscs    int
	CurrentDiskID string
	DryRun        bool
}

func jobViewFromJob(job *burn.Job) *jobView {
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
		DryRun:        job.Options.DryRun,
	}
}
```

Add `DryRun: r.FormValue("dry_run") == "on",` to `startBurn`'s
`burn.Options{...}` literal:

```go
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

- [ ] **Step 4: Add the checkbox, badge, and reworded button**

In `internal/web/templates/dashboard.html`, add a dry-run checkbox next
to the existing ones in the burn form:

```html
  <label><input type="checkbox" name="cross_disc_parity"> Add cross-disc recovery disc</label>
  <label>Group size <input type="number" name="group_size" value="10" min="2"></label>
  <label><input type="checkbox" name="dry_run"> Dry run (produce an ISO only, don't burn)</label>
  <button type="submit">Burn</button>
```

(That's the existing two lines plus one new one inserted before the
`Burn` button — replace the existing block accordingly.)

In `internal/web/templates/jobs.html`, replace:

```html
{{define "jobs-fragment"}}
{{if .}}
<p>Disc {{.CurrentIndex1}}/{{.TotalDiscs}} — state: {{.State}}{{if .Err}} — error: {{.Err}}{{end}}</p>
{{if eq .State "AWAITING_DISC"}}
  <form hx-post="/jobs/continue" hx-target="#job-status" hx-swap="innerHTML">
    <button type="submit">Insert blank disc, continue</button>
  </form>
{{end}}
```

with:

```html
{{define "jobs-fragment"}}
{{if .}}
<p>{{if .DryRun}}<strong>[DRY RUN]</strong> {{end}}Disc {{.CurrentIndex1}}/{{.TotalDiscs}} — state: {{.State}}{{if .Err}} — error: {{.Err}}{{end}}</p>
{{if eq .State "AWAITING_DISC"}}
  <form hx-post="/jobs/continue" hx-target="#job-status" hx-swap="innerHTML">
    <button type="submit">{{if .DryRun}}Continue (dry run — no disc needed){{else}}Insert blank disc, continue{{end}}</button>
  </form>
{{end}}
```

(The rest of `jobs.html` — the `FAILED`/`DONE`/final-else branches — is
unchanged.)

- [ ] **Step 5: Run tests to verify everything passes**

Run: `go build ./... && go vet ./... && go test ./internal/web/... -race -v`
Expected: PASS, including the new `TestJobViewFromJob_ExposesDryRun` and
`TestTemplates_ParseWithoutError`/`TestStagingFilesFragment_Renders`
(these parse/execute the whole embedded template set including
`dashboard.html` and `jobs.html`, so a syntax mistake here is caught).

- [ ] **Step 6: Commit**

```bash
git add internal/web/jobs.go internal/web/jobs_test.go internal/web/templates/dashboard.html internal/web/templates/jobs.html
git commit -m "$(cat <<'EOF'
Add a dry-run checkbox to the dashboard and a DRY RUN badge to the Jobs page

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 7: Library + Cover UI — dry-run indicator

`cover.go` already fetches the full `db.Disk` via `db.GetDisk` and passes
it to `cover.html` as `.Disk` — `cover.html` can reference `.Disk.IsDryRun`
directly, no data-flow change needed there at all. The Library's search
results are the one place that genuinely needs new plumbing:
`db.FileRecord` carries a `DiskID` but nothing about *that disc*, so
`SearchFiles` needs to join against `disks` to expose it.

**Files:**
- Modify: `internal/db/files.go`
- Modify: `internal/db/files_test.go`
- Modify: `internal/web/templates/library.html`
- Modify: `internal/web/templates/cover.html`

- [ ] **Step 1: Write the failing test for `SearchFiles`**

Add to `internal/db/files_test.go`:

```go
func TestSearchFiles_ExposesDiskIsDryRun(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	if err := SeedMediaTypes(ctx, pool); err != nil {
		t.Fatalf("SeedMediaTypes: %v", err)
	}
	id, err := NextDiskID(ctx, pool, "BDDRYSEARCH", 0)
	if err != nil {
		t.Fatalf("NextDiskID: %v", err)
	}
	if err := InsertDisk(ctx, pool, Disk{ID: id, MediaType: "BD-R", ParityPercent: 10, Role: "data", IsDryRun: true}); err != nil {
		t.Fatalf("InsertDisk: %v", err)
	}
	term := "dryrunsearch-" + id
	if err := InsertFile(ctx, pool, FileRecord{DiskID: id, OriginalPath: term + ".jpg", SizeBytes: 1}); err != nil {
		t.Fatalf("InsertFile: %v", err)
	}

	results, err := SearchFiles(ctx, pool, term)
	if err != nil {
		t.Fatalf("SearchFiles: %v", err)
	}
	found := false
	for _, f := range results {
		if f.OriginalPath == term+".jpg" {
			found = true
			if !f.DiskIsDryRun {
				t.Errorf("DiskIsDryRun = false, want true for a file on a dry-run disc")
			}
		}
	}
	if !found {
		t.Fatal("expected to find the inserted file in search results")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `DATABASE_URL='postgres://varis:varis@localhost:5432/varis?sslmode=disable' go test ./internal/db/... -run TestSearchFiles_ExposesDiskIsDryRun -v`
Expected: FAIL to compile — `FileRecord` has no `DiskIsDryRun` field yet.

- [ ] **Step 3: Add `DiskIsDryRun` and join `disks` in `SearchFiles`**

In `internal/db/files.go`, add the field to `FileRecord`:

```go
type FileRecord struct {
	ID           string
	DiskID       string
	OriginalPath string
	SizeBytes    int64
	FileHash     string
	Tags         []string
	Description  string
	DiskIsDryRun bool
}
```

Replace `SearchFiles`:

```go
// SearchFiles matches original_path, description, and tags. This is a
// deliberate full scan, not an indexed lookup: idx_files_tags is a GIN
// array_ops index, which serves containment queries (tags @> ARRAY[...])
// but can't accelerate a substring match against tag text, so tags are
// matched via a plain ILIKE against the joined tag text instead of the
// index — also bypassing the trigram index original_path alone used to
// get. Acceptable at this project's single-user, home-archive scale
// (tens of thousands of files, not millions); revisit if that changes.
func SearchFiles(ctx context.Context, pool *pgxpool.Pool, query string) ([]FileRecord, error) {
	rows, err := pool.Query(ctx,
		`SELECT f.id, f.disk_id, f.original_path, f.size_bytes, f.file_hash, f.tags, f.description, d.is_dry_run
		 FROM files f JOIN disks d ON d.id = f.disk_id
		 WHERE f.original_path ILIKE '%' || $1 || '%'
		    OR f.description ILIKE '%' || $1 || '%'
		    OR array_to_string(f.tags, ' ') ILIKE '%' || $1 || '%'
		 ORDER BY GREATEST(
		     similarity(f.original_path, $1),
		     similarity(f.description, $1),
		     similarity(array_to_string(f.tags, ' '), $1)
		 ) DESC LIMIT 50`, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileRecord
	for rows.Next() {
		var f FileRecord
		if err := rows.Scan(&f.ID, &f.DiskID, &f.OriginalPath, &f.SizeBytes, &f.FileHash, &f.Tags, &f.Description, &f.DiskIsDryRun); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
```

(`GetFile` and `InsertFile` are unchanged — this join is specific to
`SearchFiles`' display needs; nothing else needs `DiskIsDryRun`.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `DATABASE_URL='postgres://varis:varis@localhost:5432/varis?sslmode=disable' go test ./internal/db/... -run 'TestSearchFiles' -v`
Expected: PASS

- [ ] **Step 5: Show the flag in `library.html` and `cover.html`**

In `internal/web/templates/library.html`, update the `search-rows` row to
show a dry-run marker next to the disc ID:

```html
{{define "search-rows"}}
{{range .}}
<tr>
  <td>{{.OriginalPath}}</td>
  <td>{{.SizeBytes}}</td>
  <td>{{.DiskID}}{{if .DiskIsDryRun}} <strong>[DRY RUN]</strong>{{end}}</td>
  <td>{{range .Tags}}<span class="tag">{{.}}</span>{{end}}{{if .Description}} <em>{{.Description}}</em>{{end}}</td>
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

In `internal/web/templates/cover.html`, add the flag next to the existing
disc summary line:

```html
<p>{{.Disk.MediaType}} — parity {{.Disk.ParityPercent}}% — burned {{.Disk.CreatedAt}}{{if .Disk.IsDryRun}} — <strong>DRY RUN (not a real disc)</strong>{{end}}</p>
```

(Replacing the current single `<p>...</p>` line in that file.)

- [ ] **Step 6: Run tests to verify everything passes**

Run: `go build ./... && go vet ./... && go test ./... -race`
Expected: PASS — in particular, `TestTemplates_ParseWithoutError` (from
the staged-file-listing-and-metadata plan) catches any template syntax
mistake in `cover.html`/`library.html`. Note: no test in this project
currently *executes* `cover.html` specifically (only `dashboard.html`'s
fragments and `search-rows`, per prior plans' `TestStagingFilesFragment_Renders`/
`TestSearchRows_Renders`) — this task doesn't add one for `cover.html`
either, matching this project's existing scope for what gets execution
-level template coverage versus parse-only coverage; a future task could
add one following the same pattern if that gap matters later.

- [ ] **Step 7: Commit**

```bash
git add internal/db/files.go internal/db/files_test.go internal/web/templates/library.html internal/web/templates/cover.html
git commit -m "$(cat <<'EOF'
Show the dry-run flag in Library search results and on the disc cover page

SearchFiles joins disks to expose DiskIsDryRun on FileRecord — cover.html needed no new plumbing since cover.go already passes the full db.Disk through.

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 8: New `/dryruns` management page

**Files:**
- Create: `internal/web/dryruns.go`
- Create: `internal/web/templates/dryruns.html`
- Modify: `internal/web/server.go`
- Modify: `internal/web/templates/layout.html`

- [ ] **Step 1: Implement the handlers**

```go
// internal/web/dryruns.go
package web

import (
	"net/http"

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

func (s *Server) deleteDryRun(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	diskID := r.FormValue("disk_id")
	if err := db.DeleteDryRunDisc(r.Context(), s.pool, diskID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/dryruns", http.StatusSeeOther)
}
```

Note: `deleteDryRun` deletes the catalog rows but not the ISO file itself
in this step — Step 2 below adds that, since it needs the configured
dry-run directory, which `Server` doesn't currently have access to.

- [ ] **Step 2: Give `Server` access to the dry-run directory, and delete the ISO too**

In `internal/web/server.go`, add a `dryRunDir` field and constructor
parameter:

```go
type Server struct {
	pool        *pgxpool.Pool
	burnMgr     *burn.Manager
	retrieveMgr *retrieve.Manager
	tmpl        *template.Template
	stagingDir  string
	dryRunDir   string
}

func NewServer(pool *pgxpool.Pool, burnMgr *burn.Manager, retrieveMgr *retrieve.Manager, stagingDir, dryRunDir string) (*Server, error) {
	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{pool: pool, burnMgr: burnMgr, retrieveMgr: retrieveMgr, tmpl: tmpl, stagingDir: stagingDir, dryRunDir: dryRunDir}, nil
}
```

(This changes `NewServer`'s signature; update its one call site in
`cmd/archive-core/main.go`:)

```go
	webServer, err := web.NewServer(pool, burnMgr, retrieveMgr, cfg.StagingDir, cfg.DryRunDir)
```

Back in `internal/web/dryruns.go`, update `deleteDryRun` to also remove
the ISO file — deleting the file first, so a failure there never leaves
an orphaned catalog reference to a file that's already gone (the inverse
ordering of the DB-only helper's own reasoning, since this handler's
failure mode runs the other way: a stale catalog row pointing at a
missing file is more confusing than a leftover file with no row, and
`DeleteDryRunDisc` is safe to call again either way):

```go
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
```

Add `"os"` and `"path/filepath"` to `internal/web/dryruns.go`'s imports.

- [ ] **Step 3: Register the routes**

In `internal/web/server.go`'s `Routes`, add:

```go
	mux.HandleFunc("GET /dryruns", s.dryRunsPage)
	mux.HandleFunc("POST /dryruns/delete", s.deleteDryRun)
```

- [ ] **Step 4: Add the template**

```html
{{/* internal/web/templates/dryruns.html */}}
{{define "dryruns"}}
<!doctype html>
<html>
<head>{{template "head"}}</head>
<body>
{{template "nav"}}
<h1>Dry Runs</h1>
<p>These are test ISOs, not physical discs. Inspect them at
<code>/webdav/dryrun/</code>, or delete one below once you're done with it
— that removes both the ISO file and its catalog entry.</p>
<table>
  <thead><tr><th>Disk ID</th><th>Media type</th><th>Created</th><th>ISO</th><th></th></tr></thead>
  <tbody>
  {{range .}}
  <tr>
    <td>{{.ID}}</td>
    <td>{{.MediaType}}</td>
    <td>{{.CreatedAt}}</td>
    <td><a href="/webdav/dryrun/{{.ID}}.iso">{{.ID}}.iso</a></td>
    <td>
      <form method="post" action="/dryruns/delete">
        <input type="hidden" name="disk_id" value="{{.ID}}">
        <button type="submit">Delete</button>
      </form>
    </td>
  </tr>
  {{end}}
  </tbody>
</table>
</body>
</html>
{{end}}
```

In `internal/web/templates/layout.html`, add a nav link:

```html
{{define "nav"}}
<nav>
  <a href="/">Dashboard</a>
  <a href="/library">Library</a>
  <a href="/dryruns">Dry Runs</a>
  <a href="/config">Config</a>
</nav>
{{end}}
```

- [ ] **Step 5: Run tests to verify everything passes**

Run: `go build ./... && go vet ./... && go test ./... -race`
Expected: PASS. `TestTemplates_ParseWithoutError` picks up
`dryruns.html`'s inclusion automatically (it globs `templates/*.html`),
confirming it parses; no handler-level test is added for
`dryRunsPage`/`deleteDryRun` themselves, matching this project's
established convention (see the comment on `TestSearch_EmptyQueryReturnsNoRows`
in `internal/web/handlers_test.go`) that pool-touching web handlers with
no non-trivial pure logic of their own aren't unit-tested at this level —
`ListDryRunDisks`/`DeleteDryRunDisc` already have their own DB-backed
tests from Task 1.

- [ ] **Step 6: Commit**

```bash
git add internal/web/dryruns.go internal/web/templates/dryruns.html internal/web/server.go internal/web/templates/layout.html cmd/archive-core/main.go
git commit -m "$(cat <<'EOF'
Add a /dryruns page to list and delete dry-run discs

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

## Plan Self-Review

**Spec coverage:**
- §2 (data model, `is_dry_run`): Task 1.
- §3 (burn pipeline branch, cross-disc-parity discs unaffected): Task 3 — planning/packing/parity/ISO steps are never touched, only the burn/verify block.
- §4 (retrieval reads from the dry-run path): Task 4 — covers both `ReadDisk` (the disc actually being retrieved) and `ReadReconstructionDisc` (a dry-run disc as a *surviving* group member), the latter being a real gap the original spec's phrasing ("no new reconstruction-specific behavior needed") didn't fully anticipate — `ReadReconstructionDisc` already branches per-disc on `Role` and needed the identical `IsDryRun`-based source resolution added alongside it, not left out.
- §5 (WebDAV mount, `/dryruns` page, delete-everything-together): Task 5 (mount) + Task 8 (page, delete handler).
- §6 (dashboard checkbox, Jobs badge/wording, Library/cover indicators): Task 6 (dashboard/jobs) + Task 7 (library/cover).
- §7 (error handling matches existing burn-failure conventions; testing conventions): addressed throughout — dry-run copy failures use the same `StateFailed`/retry path as a real burn failure (Task 3's `runDisc` change returns an error the same way `BurnISO` failing already does, so the existing `ContinueNextDisc`/`Retry` machinery needs no changes at all); DB-backed tests skip gracefully without `DATABASE_URL`; fake-executor/fake-catalog patterns reused throughout.
- §8 (non-goals): the plan doesn't touch the media/drive abstraction, doesn't add "convert dry-run to real" tooling, and doesn't change reconstruction's *logic* — only where its per-member reads pull bytes from, which is squarely within scope (§4), not a change to reconstruction's non-goal.
- §9 (future work: media/drive abstraction) is explicitly out of this plan, per the user's own direction to plan it separately.

**Placeholder scan:** no TBD/TODO or vague instructions remain. Task 3 Step 6 and Task 4 Step 8 both say "go through the file and update every `NewManager(...)` call site the same way" rather than reproducing every single test function — this is not a placeholder in the disallowed sense (it doesn't hide missing design work; the exact mechanical change is fully specified — add one trailing `t.TempDir()` argument — and Step 1 of each task shows a complete worked example of the pattern), but it is a larger mechanical sweep than most steps in this project's prior plans, worth calling out explicitly to whoever executes it: this is intentional given how many pre-existing tests construct these managers, not an oversight.

**Type consistency:** `burn.Options.DryRun` (Task 3) is read by `jobViewFromJob` (Task 6) and `commitDisc` (Task 3) consistently. `db.Disk.IsDryRun` (Task 1) flows unchanged through `burn.Cataloger.InsertDisk`/`retrieve.Catalog.GetDisk` (no interface changes needed, confirmed) into `runDisc`/`ReadDisk`/`ReadReconstructionDisc` (Tasks 3-4) and `cover.html`/`db.FileRecord.DiskIsDryRun` (Task 7). `burn.NewManager`/`retrieve.NewManager`/`web.NewServer` all gain their new trailing parameter in the same names (`dryRunDir`) used consistently from `cmd/archive-core/main.go` (Task 5, Task 8) down to each package's own tests.
