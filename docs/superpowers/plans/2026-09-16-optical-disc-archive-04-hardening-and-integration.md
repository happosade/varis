# Optical Disc Archive — 04: Hardening, Integration Testing & Docs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the one structural gap flagged by plan 03's self-review (burning and retrieving aren't mutually exclusive on the single shared drive), add one end-to-end test that exercises a full burn → simulated disc loss → reconstruction cycle across the `burn` and `retrieve` packages together, and write the README this project has been missing since plan 00.

**Architecture:** No new packages. This plan only adds guard checks to two existing handlers, one integration test that composes `burn.Manager` and `retrieve.Manager` with a shared fake "shelf" standing in for physical media, and project documentation.

**Tech Stack:** Go 1.23 stdlib only.

**Depends on:** plans 00–03 (uses every package).

---

### Task 1: Shared drive lock between burning and retrieval

Right now `burn.Manager` and `retrieve.Manager` each separately guarantee
only one of *their own* jobs runs at a time, but nothing stops a burn and a
retrieval from both being "in progress" simultaneously — which would mean
two logical operations fighting over the one physical drive. Fix this at
the `web` layer, where both managers are already visible to each handler.

**Files:**
- Modify: `internal/web/jobs.go` (`startBurn`)
- Modify: `internal/web/library.go` (`startRetrieve`)
- Test: `internal/web/handlers_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// add to internal/web/handlers_test.go
package web

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"varis/internal/burn"
	"varis/internal/db"
	"varis/internal/execx"
	"varis/internal/retrieve"
)

// stubCataloger/stubCatalog below are minimal Cataloger/Catalog
// implementations that always succeed, just enough to get a Manager into
// a non-DONE/FAILED state for these lock tests — they don't need to
// exercise real burn/retrieve behavior, which is already covered in
// internal/burn and internal/retrieve.

type stubCataloger struct{ n int }

func (c *stubCataloger) NextDiskID(ctx context.Context, prefix string) (string, error) {
	c.n++
	return prefix + ":stub", nil
}
func (c *stubCataloger) NewBurnJobID(ctx context.Context) (string, error) { return "job", nil }
func (c *stubCataloger) NextGroupID(ctx context.Context, burnJobID string, groupSize int) (string, error) {
	return "group", nil
}
func (c *stubCataloger) InsertDisk(ctx context.Context, d db.Disk) error     { return nil }
func (c *stubCataloger) InsertFile(ctx context.Context, f db.FileRecord) error { return nil }

func TestStartBurn_RejectedWhileRetrievalInProgress(t *testing.T) {
	staging := t.TempDir()
	if err := writeFixtureFile(staging, "a.bin", "some bytes"); err != nil {
		t.Fatal(err)
	}
	burnMgr := burn.NewManager(&stubCataloger{}, &execx.FakeExecutor{}, staging, t.TempDir(), t.TempDir()+"/device")

	retrieveMgr := retrieve.NewManager(stubCatalog{
		files: map[string]db.FileRecord{"f1": {ID: "f1", DiskID: "BD:0001", OriginalPath: "a.bin"}},
	}, &execx.FakeExecutor{}, t.TempDir(), t.TempDir(), t.TempDir()+"/device")
	if err := retrieveMgr.Start(context.Background(), "f1"); err != nil {
		t.Fatalf("retrieveMgr.Start: %v", err)
	}

	s := &Server{burnMgr: burnMgr, retrieveMgr: retrieveMgr}
	form := url.Values{"media_type": {"BD-R"}, "parity_percent": {"10"}}
	r := httptest.NewRequest("POST", "/burn", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	s.startBurn(w, r)

	if !strings.Contains(w.Body.String(), "retrieval") {
		t.Errorf("body = %q, want a rejection mentioning the in-progress retrieval", w.Body.String())
	}
	if burnMgr.Current() != nil {
		t.Error("expected Start to be rejected before a Job was created")
	}
}

func TestStartRetrieve_RejectedWhileBurnInProgress(t *testing.T) {
	staging := t.TempDir()
	if err := writeFixtureFile(staging, "a.bin", "some bytes"); err != nil {
		t.Fatal(err)
	}
	burnMgr := burn.NewManager(&stubCataloger{}, &execx.FakeExecutor{}, staging, t.TempDir(), t.TempDir()+"/device")
	if err := burnMgr.Start(context.Background(), burn.Options{MediaType: "BD-R", CapacityBytes: 1000, ParityPercent: 10}); err != nil {
		t.Fatalf("burnMgr.Start: %v", err)
	}

	retrieveMgr := retrieve.NewManager(stubCatalog{
		files: map[string]db.FileRecord{"f1": {ID: "f1", DiskID: "BD:0001", OriginalPath: "a.bin"}},
	}, &execx.FakeExecutor{}, t.TempDir(), t.TempDir(), t.TempDir()+"/device")

	s := &Server{burnMgr: burnMgr, retrieveMgr: retrieveMgr}
	form := url.Values{"file_id": {"f1"}}
	r := httptest.NewRequest("POST", "/retrieve", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	s.startRetrieve(w, r)

	if w.Code == 200 || w.Code == 0 {
		// startRetrieve on success renders 200 with no explicit code set;
		// a rejection must use a non-2xx status (409, matching Start's
		// own "already in progress" convention from plan 01/03).
	}
	if w.Result().StatusCode != 409 {
		t.Errorf("status = %d, want 409", w.Result().StatusCode)
	}
}

type stubCatalog struct {
	files map[string]db.FileRecord
}

func (c stubCatalog) GetFile(ctx context.Context, fileID string) (db.FileRecord, error) {
	return c.files[fileID], nil
}
func (c stubCatalog) GetDisk(ctx context.Context, diskID string) (db.Disk, error) { return db.Disk{}, nil }
func (c stubCatalog) GroupMembers(ctx context.Context, groupID string) ([]db.Disk, error) {
	return nil, nil
}
func (c stubCatalog) MediaCapacity(ctx context.Context, mediaType string) (int64, error) {
	return 1000, nil
}

func writeFixtureFile(dir, name, content string) error {
	return osWriteFile(dir, name, content)
}
```

`osWriteFile` above is a placeholder — replace it in the next step with a
direct `os.WriteFile` call (kept separate only so the import list change
is explicit):

- [ ] **Step 2: Fix the fixture helper**

Replace the `writeFixtureFile`/`osWriteFile` pair with:

```go
func writeFixtureFile(dir, name, content string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}
```

Add `"os"` and `"path/filepath"` to handlers_test.go's imports.

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/web/... -run 'TestStartBurn_RejectedWhileRetrievalInProgress|TestStartRetrieve_RejectedWhileBurnInProgress' -v`
Expected: FAIL — no lock check exists yet, so both operations currently
succeed regardless of what the other manager is doing

- [ ] **Step 4: Add the guard to startBurn**

In `internal/web/jobs.go`, at the top of `startBurn`, right after
`r.ParseForm()`:

```go
	if rj := s.retrieveMgr.Current(); rj != nil && rj.State != retrieve.StateDone && rj.State != retrieve.StateFailed {
		fmt.Fprintf(w, `<p class="error">drive busy: a retrieval is in progress (%s)</p>`, rj.State)
		return
	}
```

Add `"varis/internal/retrieve"` to jobs.go's imports.

- [ ] **Step 5: Add the guard to startRetrieve**

In `internal/web/library.go`, at the top of `startRetrieve`, right after
`r.ParseForm()`:

```go
	if bj := s.burnMgr.Current(); bj != nil && bj.State != burn.StateDone && bj.State != burn.StateFailed {
		http.Error(w, fmt.Sprintf("drive busy: a burn is in progress (%s)", bj.State), http.StatusConflict)
		return
	}
```

Add `"fmt"` and `"varis/internal/burn"` to library.go's imports (it
currently only imports `"net/http"`, `"varis/internal/db"`, and
`"varis/internal/retrieve"` from Task 6 Step 4/Step 5's `retrieveView`).

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/web/... -v`
Expected: PASS (all web package tests)

- [ ] **Step 7: Commit**

```bash
git add internal/web
git commit -m "$(cat <<'EOF'
Reject starting a burn while a retrieval is in progress and vice versa

The single physical drive can only serve one operation at a time; burn.Manager and retrieve.Manager each already enforce this within themselves but not against each other.

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 2: End-to-end test — burn a group, lose a disc, reconstruct it

This test lives in the `retrieve` package (which already imports `burn`)
and exercises the full path spec §5 promises: burn 2 data discs + 1 parity
disc, "lose" one data disc, and recover the file it held by reading the
two survivors.

Since `execx.FakeExecutor` doesn't implement real ISO 9660 encoding, this
test simulates the disc format with a small in-memory "manifest" (disk ID
+ named file contents) instead of literal ISO bytes — it verifies *this
project's own orchestration* (state transitions, correct disc-ID/group
bookkeeping, correct XOR math end to end), not `xorriso`/`par2`/`wodim`
themselves, which plan 01 already established can't run in CI.

**Files:**
- Create: `internal/retrieve/integration_test.go`

- [ ] **Step 1: Write the failing test**

```go
package retrieve

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"varis/internal/burn"
	"varis/internal/db"
	"varis/internal/execx"
)

// fakeManifest stands in for a real ISO 9660 image: it's what the fake
// xorriso calls below read and write instead of an actual filesystem.
type fakeManifest struct {
	Files map[string][]byte `json:"files"`
}

// shelf simulates the physical media library — one burned disc's bytes,
// keyed by disc ID, available to be "inserted" into the shared "device"
// file later.
type shelf struct{ dir string }

func (s shelf) put(diskID string, data []byte) error {
	return os.WriteFile(filepath.Join(s.dir, diskID+".shelf"), data, 0o644)
}
func (s shelf) insert(diskID, devicePath string) error {
	data, err := os.ReadFile(filepath.Join(s.dir, diskID+".shelf"))
	if err != nil {
		return err
	}
	return os.WriteFile(devicePath, data, 0o644)
}

// sharedFakeExecutor's xorriso hook handles both call shapes used across
// the codebase: BuildISO's "-as mkisofs -o <isoPath> ... <sourceDir>" and
// ExtractFromDisc's "-indev <src> -extract <pathInISO> <destPath>".
func sharedFakeExecutor(shelf shelf) *execx.FakeExecutor {
	return &execx.FakeExecutor{
		Funcs: map[string]func(args []string) execx.Result{
			"par2create": func(args []string) execx.Result {
				dataPath := args[len(args)-1]
				os.WriteFile(dataPath+".par2", []byte("par2-index"), 0o644)
				return execx.Result{}
			},
			"par2verify": func(args []string) execx.Result { return execx.Result{} },
			"xorriso": func(args []string) execx.Result {
				if args[0] == "-as" {
					isoPath := ""
					sourceDir := args[len(args)-1]
					for i, a := range args {
						if a == "-o" && i+1 < len(args) {
							isoPath = args[i+1]
						}
					}
					entries, err := os.ReadDir(sourceDir)
					if err != nil {
						return execx.Result{Err: err}
					}
					m := fakeManifest{Files: map[string][]byte{}}
					for _, e := range entries {
						data, err := os.ReadFile(filepath.Join(sourceDir, e.Name()))
						if err != nil {
							return execx.Result{Err: err}
						}
						m.Files["/"+e.Name()] = data
					}
					blob, err := json.Marshal(m)
					if err != nil {
						return execx.Result{Err: err}
					}
					return execx.Result{Err: os.WriteFile(isoPath, blob, 0o644)}
				}
				// "-indev", src, "-extract", pathInISO, destPath
				src, pathInISO, destPath := args[1], args[3], args[4]
				blob, err := os.ReadFile(src)
				if err != nil {
					return execx.Result{Err: err}
				}
				var m fakeManifest
				if err := json.Unmarshal(blob, &m); err != nil {
					return execx.Result{Err: err}
				}
				content, ok := m.Files[pathInISO]
				if !ok {
					return execx.Result{Err: os.ErrNotExist}
				}
				return execx.Result{Err: os.WriteFile(destPath, content, 0o644)}
			},
			"wodim": func(args []string) execx.Result {
				devicePath := strings.TrimPrefix(args[0], "dev=")
				isoPath := args[len(args)-1]
				data, err := os.ReadFile(isoPath)
				if err != nil {
					return execx.Result{Err: err}
				}
				if err := os.WriteFile(devicePath, data, 0o644); err != nil {
					return execx.Result{Err: err}
				}
				diskID := strings.TrimSuffix(filepath.Base(isoPath), ".iso")
				return execx.Result{Err: shelf.put(diskID, data)}
			},
		},
	}
}

// inMemoryCataloger/inMemoryCatalog implement both burn.Cataloger and
// Catalog against one shared map, so a disc burned via burn.Manager is
// immediately visible to retrieve.Manager, matching how they'd both sit
// on top of the same real Postgres database in production.
type inMemoryStore struct {
	seq        map[string]int
	groupSeq   int
	burnJobSeq int
	disks      map[string]db.Disk
	files      []db.FileRecord
}

func newInMemoryStore() *inMemoryStore {
	return &inMemoryStore{seq: map[string]int{}, disks: map[string]db.Disk{}}
}

func (s *inMemoryStore) NextDiskID(ctx context.Context, prefix string) (string, error) {
	s.seq[prefix]++
	return prefix + ":" + itoa4(s.seq[prefix]), nil
}
func (s *inMemoryStore) NewBurnJobID(ctx context.Context) (string, error) {
	s.burnJobSeq++
	return "burnjob-" + itoa4(s.burnJobSeq), nil
}
func (s *inMemoryStore) NextGroupID(ctx context.Context, burnJobID string, groupSize int) (string, error) {
	s.groupSeq++
	return "group-" + itoa4(s.groupSeq), nil
}
func (s *inMemoryStore) InsertDisk(ctx context.Context, d db.Disk) error {
	s.disks[d.ID] = d
	return nil
}
func (s *inMemoryStore) InsertFile(ctx context.Context, f db.FileRecord) error {
	s.files = append(s.files, f)
	return nil
}
func (s *inMemoryStore) GetFile(ctx context.Context, fileID string) (db.FileRecord, error) {
	for _, f := range s.files {
		if f.ID == fileID {
			return f, nil
		}
	}
	return db.FileRecord{}, os.ErrNotExist
}
func (s *inMemoryStore) GetDisk(ctx context.Context, diskID string) (db.Disk, error) {
	return s.disks[diskID], nil
}
func (s *inMemoryStore) GroupMembers(ctx context.Context, groupID string) ([]db.Disk, error) {
	var out []db.Disk
	for _, d := range s.disks {
		if d.GroupID != nil && *d.GroupID == groupID {
			out = append(out, d)
		}
	}
	return out, nil
}
func (s *inMemoryStore) MediaCapacity(ctx context.Context, mediaType string) (int64, error) {
	return 4096, nil
}

func itoa4(n int) string {
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}

func TestEndToEnd_BurnGroupLoseADiscReconstruct(t *testing.T) {
	staging := t.TempDir()
	spool := t.TempDir()
	scratch := t.TempDir()
	shelfDir := t.TempDir()
	device := filepath.Join(t.TempDir(), "device")

	mustWrite := func(name, content string) {
		if err := os.WriteFile(filepath.Join(staging, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("photo1.jpg", strings.Repeat("A", 500))
	mustWrite("photo2.jpg", strings.Repeat("B", 500))

	store := newInMemoryStore()
	shelf := shelf{dir: shelfDir}
	ex := sharedFakeExecutor(shelf)

	// --- Burn: 2 data discs (1 file each) + 1 parity disc for the group ---
	burnMgr := burn.NewManager(store, ex, staging, spool, device)
	burnOpts := burn.Options{
		MediaType:       "BD-R",
		CapacityBytes:   700, // forces exactly 1 file per data disc
		ParityPercent:   10,
		CrossDiscParity: true,
		GroupSize:       2,
	}
	if err := burnMgr.Start(context.Background(), burnOpts); err != nil {
		t.Fatalf("burnMgr.Start: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := burnMgr.ContinueNextDisc(context.Background()); err != nil {
			t.Fatalf("ContinueNextDisc[%d]: %v (job err: %v)", i, err, burnMgr.Current().Err)
		}
	}
	if burnMgr.Current().State != burn.StateDone {
		t.Fatalf("burn State = %s, want DONE", burnMgr.Current().State)
	}

	// Find photo1.jpg's disc so we know which one to "lose".
	var lostDiskID, survivorDataID, parityID string
	for _, f := range store.files {
		if f.OriginalPath == "photo1.jpg" {
			lostDiskID = f.DiskID
		}
	}
	for id, d := range store.disks {
		if d.Role == "data" && id != lostDiskID {
			survivorDataID = id
		}
		if d.Role == "parity" {
			parityID = id
		}
	}
	if lostDiskID == "" || survivorDataID == "" || parityID == "" {
		t.Fatalf("expected to find lost/survivor/parity discs, got disks=%+v", store.disks)
	}

	// --- Retrieve photo1.jpg: its disc is "lost" (simulate an unreadable disc). ---
	retrievedDir := t.TempDir()
	retrieveMgr := NewManager(store, ex, retrievedDir, scratch, device)

	var photo1ID string
	for _, f := range store.files {
		if f.OriginalPath == "photo1.jpg" {
			photo1ID = f.ID
		}
	}
	if err := retrieveMgr.Start(context.Background(), photo1ID); err != nil {
		t.Fatalf("retrieveMgr.Start: %v", err)
	}

	// Simulate the drive containing garbage instead of the requested disc.
	if err := os.WriteFile(device, []byte("not a valid disc"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := retrieveMgr.ReadDisk(context.Background()); err == nil {
		t.Fatal("expected ReadDisk to fail against a garbage device")
	}
	if retrieveMgr.Current().State != StateNeedsReconstruction {
		t.Fatalf("State = %s, want NEEDS_RECONSTRUCTION", retrieveMgr.Current().State)
	}

	if err := retrieveMgr.StartReconstruction(context.Background()); err != nil {
		t.Fatalf("StartReconstruction: %v", err)
	}

	remaining := retrieveMgr.Remaining()
	if len(remaining) != 2 {
		t.Fatalf("Remaining() = %v, want 2 entries (the survivor data disc and the parity disc)", remaining)
	}

	for _, diskID := range remaining {
		if err := shelf.insert(diskID, device); err != nil {
			t.Fatalf("inserting %s: %v", diskID, err)
		}
		if err := retrieveMgr.ReadReconstructionDisc(context.Background(), diskID); err != nil {
			t.Fatalf("ReadReconstructionDisc(%s): %v", diskID, err)
		}
	}

	if retrieveMgr.Current().State != StateDone {
		t.Fatalf("State = %s, want DONE (err=%v)", retrieveMgr.Current().State, retrieveMgr.Current().Err)
	}
	got, err := os.ReadFile(filepath.Join(retrievedDir, "photo1.jpg"))
	if err != nil {
		t.Fatalf("reading recovered file: %v", err)
	}
	if got := string(got); got != strings.Repeat("A", 500) {
		t.Errorf("recovered content = %q (len %d), want 500 A's", got[:min(20, len(got))], len(got))
	}

	_ = survivorDataID
	_ = parityID
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/retrieve/... -run TestEndToEnd -v`
Expected: FAIL initially if any wiring assumption above doesn't match the
actual signatures from plans 01–03 — this is the point of an integration
test: it catches exactly this kind of cross-plan mismatch. Fix any
signature mismatches found (there should be none if plans 00–03 were
implemented as written) rather than changing the test's intent.

- [ ] **Step 3: Run test to verify it passes**

Run: `go test ./internal/retrieve/... -v`
Expected: PASS (all retrieve package tests, including this one)

- [ ] **Step 4: Commit**

```bash
git add internal/retrieve/integration_test.go
git commit -m "$(cat <<'EOF'
Add end-to-end test: burn a cross-disc-parity group, lose a disc, reconstruct it

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 3: README

This project has had no README since plan 00. Per project convention,
documentation is written with a stronger writing-focused model where one
is available — if your working environment lets you select a model for
this task specifically, use the strongest documentation-capable model you
have (e.g. an Opus-tier model); otherwise proceed with whatever model is
running this plan.

**Files:**
- Create: `README.md`

- [ ] **Step 1: Write README.md**

Cover, at minimum:
- What this project is (one paragraph: optical disc archival with
  cross-disc RAID5-style parity, referencing
  `docs/superpowers/specs/2026-09-16-optical-disc-archive-design.md` for
  the full design).
- Requirements: a Linux host with a real optical drive and Docker (macOS
  is a WebDAV/browser client only — see spec §2).
- Quick start: `docker compose up --build -d`, mounting
  `http://localhost:8080/webdav/staging/` as a network drive, opening
  `http://localhost:8080/`.
- Environment variables from `internal/config/config.go`
  (`DATABASE_URL`, `STAGING_DIR`, `SPOOL_DIR`, `RETRIEVED_DIR`,
  `HTTP_ADDR`, `OPTICAL_DEVICE`), with their defaults.
- How to burn (dashboard walkthrough: media type, parity %, compression,
  cross-disc parity + group size, multi-disc sequencing) and how to
  retrieve (search, Get, insert disc, Read Disk, and what "Reconstruct
  from group" means and when it appears).
- How to run tests: `go test ./...` for everything that needs no live
  Postgres; `docker compose up -d archive-db && DATABASE_URL=... go test
  ./internal/db/...` for the DB-backed tests (plan 00 Task 3/4, plan 03
  Task 3).
- A pointer to the spec and the five plan documents under
  `docs/superpowers/plans/` for anyone who wants the full design rationale
  or is picking up implementation from here.

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "$(cat <<'EOF'
Add README covering setup, environment variables, and usage

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

## Plan Self-Review

**Spec coverage:** The single-drive mutual-exclusion gap flagged in plan
03's self-review (§7's "single global job lock" requirement, read as
covering the *whole* drive, not just each manager's own job) is now closed.
The end-to-end test directly exercises spec §5's central promise — losing
one disc in a group and recovering its file from the rest — across real
package boundaries instead of within a single package's unit tests.
Documentation (a global project requirement, not spec-specific) is added.

**Placeholder scan:** Task 1 Step 1's `osWriteFile` is a deliberately
named-but-undefined placeholder, corrected in Step 2 — consistent with the
scaffolding pattern used in plans 01–03, not a leftover TBD. No other
TBD/TODO remain; the README task lists required content precisely enough
that "cover, at minimum" is a checklist, not a vague instruction.

**Type consistency:** `inMemoryStore` in Task 2 implements both
`burn.Cataloger` and `retrieve.Catalog` against the same underlying maps —
deliberately, to mirror how one real Postgres database backs both
managers in production (see `main.go` in plan 03 Task 7, which
constructs `burn.NewDBCataloger(pool)` and `retrieve.NewDBCatalog(pool)`
against the same `pool`). `stubCataloger`/`stubCatalog` in Task 1 are
intentionally separate, minimal fakes (not reusing `inMemoryStore`) since
Task 1's tests only need "a job is in progress," not correct end-to-end
data flow — reusing the heavier fixture there would obscure what's being
tested.
