# Optical Disc Archive — 02: Cross-Disc Parity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the opt-in RAID5-style cross-disc parity layer (spec §5): group data discs into fixed-size groups, compute one XOR parity disc per group, and provide a reconstruction path for a lost/unreadable disc.

**Architecture:** Extends the `burn` package from plan 01 — `planJob` grows a grouping step, and a new `xordisk` package computes the parity image. Burning the parity disc reuses plan 01's `runDisc`/state machine unchanged (a parity disc is "just" a disc whose bytes come from XOR instead of a tar). Reconstruction is a separate, `Manager`-driven flow that reads every other disc in a group one at a time.

**Tech Stack:** Go 1.23 stdlib only (`bytes`, no new dependencies).

**Depends on:** `docs/superpowers/plans/2026-09-16-optical-disc-archive-01-burn-pipeline.md` (extends `planJob`, `DiscPlan`, `Manager`, `runDisc`; reuses `Cataloger`, `execx.Executor`).

---

## Package Layout (this plan creates/modifies)

```
internal/xordisk/xor.go          (new)
internal/burn/pipeline.go         (modified: planJob, runDisc, Options)
internal/burn/reconstruct.go      (new)
```

---

### Task 1: XOR reconstruction math

**Files:**
- Create: `internal/xordisk/xor.go`
- Test: `internal/xordisk/xor_test.go`

- [ ] **Step 1: Write the failing test**

```go
package xordisk

import (
	"bytes"
	"testing"
)

func TestXOR_ComputesParityAndReconstructsAnyMissingMember(t *testing.T) {
	a := []byte{0x01, 0x02, 0x03, 0x04}
	b := []byte{0x10, 0x20, 0x30, 0x40}
	c := []byte{0xFF, 0x00, 0xFF, 0x00}

	parity := XOR([][]byte{a, b, c}, 4)

	// Reconstruct "b" by XORing everything else (a, c, parity) together.
	reconstructedB := XOR([][]byte{a, c, parity}, 4)
	if !bytes.Equal(reconstructedB, b) {
		t.Errorf("reconstructedB = %x, want %x", reconstructedB, b)
	}
}

func TestXOR_PadsShorterImagesWithZeros(t *testing.T) {
	a := []byte{0x01, 0x02}
	b := []byte{0x0F}

	got := XOR([][]byte{a, b}, 4)
	want := []byte{0x01 ^ 0x0F, 0x02, 0x00, 0x00}
	if !bytes.Equal(got, want) {
		t.Errorf("XOR = %x, want %x", got, want)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/xordisk/... -v`
Expected: FAIL — package doesn't exist yet

- [ ] **Step 3: Implement xor.go**

```go
package xordisk

// XOR zero-pads every image in images to length, then XORs them together
// byte-for-byte. Used both to compute a group's parity disc image (XOR of
// all data discs) and to reconstruct one missing member (XOR of every
// other member, parity disc included) — the same operation both ways,
// which is the whole point of XOR parity.
func XOR(images [][]byte, length int64) []byte {
	out := make([]byte, length)
	for _, img := range images {
		n := int64(len(img))
		if n > length {
			n = length
		}
		for i := int64(0); i < n; i++ {
			out[i] ^= img[i]
		}
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/xordisk/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/xordisk
git commit -m "$(cat <<'EOF'
Add XOR parity/reconstruction math for cross-disc groups

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 2: Group assignment in planJob

Extend `Options` with the group-size input already described in the spec,
and make `planJob` assign `GroupID`/`SlotIndex` to every data disc when
`CrossDiscParity` is on, appending one parity `DiscPlan` per full group of
`GroupSize` data discs. A trailing partial group (fewer than `GroupSize`
data discs) still gets its own parity disc — XOR works over any group size
≥ 2.

**Files:**
- Modify: `internal/burn/pipeline.go`
- Test: `internal/burn/pipeline_test.go`

- [ ] **Step 1: Write the failing test**

```go
// add to internal/burn/pipeline_test.go
func TestPlanJob_GroupsDataDiscsAndAddsParityDiscs(t *testing.T) {
	staging := t.TempDir()
	for i := 0; i < 5; i++ {
		writeStagingFile(t, staging, fmt.Sprintf("f%d.bin", i), strings.Repeat("x", 100))
	}
	cat := newFakeCataloger()

	opts := Options{
		MediaType:       "BD-R",
		CapacityBytes:   150, // forces 5 buckets of ~100 bytes each (1 file per disc)
		ParityPercent:   10,
		CrossDiscParity: true,
		GroupSize:       2,
	}

	plans, err := planJob(context.Background(), staging, opts, cat)
	if err != nil {
		t.Fatalf("planJob: %v", err)
	}

	var dataCount, parityCount int
	groupIDs := map[string]bool{}
	for _, p := range plans {
		switch p.Role {
		case "data":
			dataCount++
			if p.GroupID == "" {
				t.Errorf("data plan %+v missing GroupID", p)
			}
			groupIDs[p.GroupID] = true
		case "parity":
			parityCount++
		default:
			t.Errorf("unexpected role %q", p.Role)
		}
	}
	if dataCount != 5 {
		t.Fatalf("dataCount = %d, want 5", dataCount)
	}
	// 5 data discs, group size 2 -> groups of [2,2,1] -> 3 parity discs.
	if parityCount != 3 {
		t.Fatalf("parityCount = %d, want 3", parityCount)
	}
	if len(groupIDs) != 3 {
		t.Fatalf("distinct groups = %d, want 3", len(groupIDs))
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/burn/... -run TestPlanJob_GroupsDataDiscsAndAddsParityDiscs -v`
Expected: FAIL — plans currently have no `GroupID`/no parity role entries

- [ ] **Step 3: Update planJob in pipeline.go**

Replace the existing `planJob` function with:

```go
// planJob scans staging and greedily buckets files into disc-sized plans.
// When opts.CrossDiscParity is set, data discs are assigned into groups of
// opts.GroupSize (a trailing short group still gets its own parity disc),
// and one "parity" role DiscPlan is appended per group — its Bucket is
// left empty; runDisc computes its bytes via XOR at burn time (see
// buildParityImage in reconstruct.go).
func planJob(ctx context.Context, stagingDir string, opts Options, cat Cataloger) ([]DiscPlan, error) {
	files, err := binpack.ScanStaging(stagingDir)
	if err != nil {
		return nil, err
	}
	target := binpack.TargetDataSize(opts.CapacityBytes, opts.ParityPercent)
	buckets := binpack.Pack(files, target)

	prefix := mediaPrefix(opts.MediaType)
	var plans []DiscPlan
	for _, b := range buckets {
		id, err := cat.NextDiskID(ctx, prefix)
		if err != nil {
			return nil, err
		}
		plans = append(plans, DiscPlan{DiskID: id, Role: "data", Bucket: b})
	}

	if !opts.CrossDiscParity || len(plans) == 0 {
		return plans, nil
	}

	groupSize := opts.GroupSize
	if groupSize <= 0 {
		groupSize = 10
	}

	burnJobID, err := cat.NewBurnJobID(ctx)
	if err != nil {
		return nil, err
	}

	var withGroups []DiscPlan
	for start := 0; start < len(plans); start += groupSize {
		end := start + groupSize
		if end > len(plans) {
			end = len(plans)
		}
		groupID, err := cat.NextGroupID(ctx, burnJobID, len(plans[start:end]))
		if err != nil {
			return nil, err
		}
		for i, p := range plans[start:end] {
			p.GroupID = groupID
			p.SlotIndex = i
			withGroups = append(withGroups, p)
		}
		parityID, err := cat.NextDiskID(ctx, prefix)
		if err != nil {
			return nil, err
		}
		withGroups = append(withGroups, DiscPlan{
			DiskID:    parityID,
			Role:      "parity",
			GroupID:   groupID,
			SlotIndex: len(plans[start:end]),
		})
	}
	return withGroups, nil
}
```

This calls two new `Cataloger` methods — `NewBurnJobID` (one UUID shared by
every group created in this planning pass) and `NextGroupID` — so the
interface must grow. Postgres already generates UUIDs elsewhere in this
schema (`gen_random_uuid()` on `disk_groups.id`), so reuse that instead of
adding a UUID library dependency just for `burn_job_id`.

- [ ] **Step 4: Add NewBurnJobID and NextGroupID to the Cataloger interface and both implementations**

In `internal/burn/pipeline.go`, update the `Cataloger` interface:

```go
type Cataloger interface {
	NextDiskID(ctx context.Context, prefix string) (string, error)
	NewBurnJobID(ctx context.Context) (string, error)
	NextGroupID(ctx context.Context, burnJobID string, groupSize int) (string, error)
	InsertDisk(ctx context.Context, d db.Disk) error
	InsertFile(ctx context.Context, f db.FileRecord) error
}
```

And its Postgres-backed implementation:

```go
func (c pgxCataloger) NewBurnJobID(ctx context.Context) (string, error) {
	var id string
	err := c.pool.QueryRow(ctx, `SELECT gen_random_uuid()`).Scan(&id)
	return id, err
}

func (c pgxCataloger) NextGroupID(ctx context.Context, burnJobID string, groupSize int) (string, error) {
	return db.InsertDiskGroup(ctx, c.pool, burnJobID, groupSize)
}
```

- [ ] **Step 5: Add NewBurnJobID and NextGroupID to FakeCataloger in pipeline_test.go**

```go
func (f *FakeCataloger) NewBurnJobID(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.burnJobSeq++
	return fmt.Sprintf("burnjob-%d", f.burnJobSeq), nil
}

func (f *FakeCataloger) NextGroupID(ctx context.Context, burnJobID string, groupSize int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.groupSeq++
	return fmt.Sprintf("group-%d", f.groupSeq), nil
}
```

Add `burnJobSeq int` and `groupSeq int` fields to the `FakeCataloger` struct.

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./internal/burn/... -v`
Expected: PASS (all burn package tests, including plan 01's)

- [ ] **Step 7: Commit**

```bash
git add internal/burn
git commit -m "$(cat <<'EOF'
Group data discs and add parity DiscPlans when cross-disc parity is enabled

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 3: Burning a parity disc

`runDisc` (plan 01) currently rejects any `Role != "data"`. Teach it to
build a parity disc's payload via XOR of its group's data-disc ISOs
instead of tarring a bucket, then reuse the same PARITY→ISO→BURN→VERIFY
steps unchanged.

**Files:**
- Modify: `internal/burn/pipeline.go`
- Test: `internal/burn/pipeline_test.go`

- [ ] **Step 1: Write the failing test**

```go
// add to internal/burn/pipeline_test.go
func TestManager_BurnsGroupWithParityDisc(t *testing.T) {
	staging := t.TempDir()
	spool := t.TempDir()
	device := filepath.Join(t.TempDir(), "device")
	writeStagingFile(t, staging, "a.bin", strings.Repeat("A", 60))
	writeStagingFile(t, staging, "b.bin", strings.Repeat("B", 60))

	cat := newFakeCataloger()
	ex := fakeExecutorForHappyPath(device)
	mgr := NewManager(cat, ex, staging, spool, device)
	mgr.freeSpace = func(string) (uint64, error) { return 1 << 40, nil }

	opts := Options{
		MediaType:       "BD-R",
		CapacityBytes:   70, // 1 file per data disc
		ParityPercent:   10,
		CrossDiscParity: true,
		GroupSize:       2,
	}
	if err := mgr.Start(context.Background(), opts); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// 2 data discs + 1 parity disc for the group.
	for i := 0; i < 3; i++ {
		if err := mgr.ContinueNextDisc(context.Background()); err != nil {
			t.Fatalf("ContinueNextDisc[%d]: %v (job err: %v)", i, err, mgr.Current().Err)
		}
	}

	job := mgr.Current()
	if job.State != StateDone {
		t.Fatalf("State = %s, want DONE", job.State)
	}

	var parityDisks int
	for _, d := range cat.Disks {
		if d.Role == "parity" {
			parityDisks++
		}
	}
	if parityDisks != 1 {
		t.Fatalf("parityDisks = %d, want 1 (got disks: %+v)", parityDisks, cat.Disks)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/burn/... -run TestManager_BurnsGroupWithParityDisc -v`
Expected: FAIL — `runDisc: role "parity" not supported`

- [ ] **Step 3: Update runDisc in pipeline.go**

Replace the top of `runDisc` (the `if plan.Role != "data" { return ... }`
guard and the packing step) with a branch that handles both roles. The
full updated function:

```go
func (m *Manager) runDisc(ctx context.Context, job *Job, plan DiscPlan) error {
	m.setState(job, StatePacking)
	tarPath := filepath.Join(m.spoolDir, plan.DiskID+".tar")

	switch plan.Role {
	case "data":
		if err := WriteTar(m.stagingDir, plan.Bucket, tarPath, job.Options.Compress); err != nil {
			return fmt.Errorf("packing: %w", err)
		}
	case "parity":
		if err := m.buildParityPayload(job, plan, tarPath); err != nil {
			return fmt.Errorf("building parity payload: %w", err)
		}
	default:
		return fmt.Errorf("runDisc: unknown role %q", plan.Role)
	}

	m.setState(job, StateParity)
	if err := CreateParity(ctx, m.ex, tarPath, job.Options.ParityPercent); err != nil {
		return fmt.Errorf("parity: %w", err)
	}

	t := toc.TOC{
		DiskID:        plan.DiskID,
		MediaType:     job.Options.MediaType,
		ParityPercent: job.Options.ParityPercent,
		GroupID:       plan.GroupID,
		Role:          plan.Role,
		SlotIndex:     plan.SlotIndex,
		CreatedAt:     time.Now(),
	}
	for _, f := range plan.Bucket.Files {
		t.Files = append(t.Files, toc.FileEntry{Path: f.Path, SizeBytes: f.Size})
	}
	tocBytes, err := t.Marshal()
	if err != nil {
		return fmt.Errorf("toc: %w", err)
	}
	tocPath := filepath.Join(m.spoolDir, plan.DiskID+".toc.json")
	if err := os.WriteFile(tocPath, tocBytes, 0o644); err != nil {
		return fmt.Errorf("writing toc: %w", err)
	}

	m.setState(job, StateISO)
	isoDir := filepath.Join(m.spoolDir, plan.DiskID+"-src")
	if err := os.MkdirAll(isoDir, 0o755); err != nil {
		return fmt.Errorf("iso staging dir: %w", err)
	}
	if err := movePathsInto(isoDir, tarPath, tocPath, tarPath+".par2"); err != nil {
		return fmt.Errorf("staging iso contents: %w", err)
	}
	isoPath := filepath.Join(m.spoolDir, plan.DiskID+".iso")
	if err := BuildISO(ctx, m.ex, isoDir, isoPath); err != nil {
		return fmt.Errorf("iso: %w", err)
	}
	isoHash, err := HashFile(isoPath)
	if err != nil {
		return fmt.Errorf("hashing iso: %w", err)
	}

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
	return nil
}

// buildParityPayload XORs the already-burned data discs' ISO images in
// plan's group together and writes the result to destPath. It relies on
// each data disc's ISO still being present in the spool dir under
// "<diskID>.iso" — runDisc never deletes ISOs, only movePathsInto's
// per-file inputs, so this holds for any parity disc processed in the
// same job right after its group's data discs.
//
// Every image is padded (or truncated) to job.Options.CapacityBytes — the
// disc's full nominal capacity — rather than to the largest actual ISO
// size. This is deliberate, not an approximation: at reconstruction time
// (see reconstruct.go) the missing disc's real size is exactly what's
// unknown, so the only length both sides can agree on without it is the
// fixed, known-in-advance media capacity. The cost is that a parity
// disc's payload is as large as a full disc's capacity even though the
// actual data on each member disc is normally much smaller (mostly zero
// padding) — an acceptable trade for correct reconstruction regardless of
// which member is lost.
func (m *Manager) buildParityPayload(job *Job, plan DiscPlan, destPath string) error {
	var images [][]byte
	for _, p := range job.Plans {
		if p.GroupID != plan.GroupID || p.Role != "data" {
			continue
		}
		isoPath := filepath.Join(m.spoolDir, p.DiskID+".iso")
		data, err := os.ReadFile(isoPath)
		if err != nil {
			return fmt.Errorf("reading data disc image %s: %w", p.DiskID, err)
		}
		images = append(images, data)
	}
	if len(images) == 0 {
		return fmt.Errorf("no data discs found for group %s", plan.GroupID)
	}
	parity := xordisk.XOR(images, job.Options.CapacityBytes)
	return os.WriteFile(destPath, parity, 0o644)
}
```

Add `"varis/internal/xordisk"` to pipeline.go's imports.

- [ ] **Step 4: Fix commitDisc to persist GroupID/SlotIndex**

Plan 01's `commitDisc` builds `db.Disk{...}` without `GroupID`/`SlotIndex` —
harmless when every disc is ungrouped, but it silently drops the group
data this task just started populating on `DiscPlan`. `db.Disk.GroupID` is
`*string` and `SlotIndex` is `*int` (both nullable columns), so an empty
`DiscPlan.GroupID` must become `nil`, not `""`. Update `commitDisc`:

```go
func (m *Manager) commitDisc(ctx context.Context, job *Job, plan DiscPlan, isoHash string) error {
	var groupID *string
	var slotIndex *int
	if plan.GroupID != "" {
		groupID = &plan.GroupID
		slotIndex = &plan.SlotIndex
	}

	err := m.cat.InsertDisk(ctx, db.Disk{
		ID:            plan.DiskID,
		MediaType:     job.Options.MediaType,
		ParityPercent: job.Options.ParityPercent,
		GroupID:       groupID,
		Role:          plan.Role,
		SlotIndex:     slotIndex,
		ISOHash:       isoHash,
	})
	if err != nil {
		return err
	}
	for _, f := range plan.Bucket.Files {
		originalPath := filepath.Join(m.stagingDir, f.Path)
		hash, err := HashFile(originalPath)
		if err != nil {
			return err
		}
		if err := m.cat.InsertFile(ctx, db.FileRecord{
			DiskID:       plan.DiskID,
			OriginalPath: f.Path,
			SizeBytes:    f.Size,
			FileHash:     hash,
		}); err != nil {
			return err
		}
		if err := os.Remove(originalPath); err != nil {
			return fmt.Errorf("removing archived file from staging: %w", err)
		}
	}
	return nil
}
```

(A parity disc's `Bucket.Files` is empty, so the file loop above is a no-op
for parity discs — nothing needs to change there.)

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/burn/... -v`
Expected: PASS (all burn package tests)

- [ ] **Step 6: Commit**

```bash
git add internal/burn
git commit -m "$(cat <<'EOF'
Burn parity discs via XOR of their group's data-disc images

Persist GroupID/SlotIndex on commit so grouping survives past the in-memory Job.

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 4: Reconstruction from a group

When a disc fails verification (or is confirmed lost), reconstruct it from
the other members of its group. This is a separate, manual, one-disc-at-a-
time flow: the caller (plan 03's HTTP layer) tells the user which disc to
insert next, reads it, and once all others are in, XORs them together.

**Files:**
- Create: `internal/burn/reconstruct.go`
- Test: `internal/burn/reconstruct_test.go`

- [ ] **Step 1: Write the failing test**

```go
package burn

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"varis/internal/db"
	"varis/internal/xordisk"
)

func TestReconstructor_RebuildsMissingDiscFromOthers(t *testing.T) {
	a := bytes.Repeat([]byte{0xAA}, 100)
	b := bytes.Repeat([]byte{0xBB}, 100)
	parity := xordisk.XOR([][]byte{a, b}, 100) // stands in for a lost "c" disc's sibling parity

	group := []db.Disk{
		{ID: "BD:0001", Role: "data", GroupID: "g1", SlotIndex: intPtr(0)},
		{ID: "BD:0002", Role: "parity", GroupID: "g1", SlotIndex: intPtr(1)},
	}

	r := NewReconstructor(group, "BD:0001", 100) // pretend BD:0001 is the missing disc; 100 = the group's fixed capacity
	if !r.NeedsMore() {
		t.Fatal("expected reconstructor to need input before any discs are supplied")
	}

	// Supply every OTHER member: here just the parity disc BD:0002, whose
	// bytes we simulate as `b` (since parity = XOR(a, b), XOR(parity, b) = a).
	if err := r.SupplyDiscImage("BD:0002", b); err != nil {
		t.Fatalf("SupplyDiscImage: %v", err)
	}
	_ = parity // parity computed above only to document the relationship; not used directly

	if r.NeedsMore() {
		t.Fatalf("still needs more after supplying the only other member: %v", r.Remaining())
	}

	got, err := r.Reconstruct()
	if err != nil {
		t.Fatalf("Reconstruct: %v", err)
	}
	if !bytes.Equal(got, a) {
		t.Errorf("Reconstruct = %x, want %x", got, a)
	}
}

func TestReconstructor_RemainingListsUnsuppliedMembers(t *testing.T) {
	group := []db.Disk{
		{ID: "BD:0001", Role: "data"},
		{ID: "BD:0002", Role: "data"},
		{ID: "BD:0003", Role: "parity"},
	}
	r := NewReconstructor(group, "BD:0002", 1000)

	remaining := r.Remaining()
	if len(remaining) != 2 || remaining[0] != "BD:0001" || remaining[1] != "BD:0003" {
		t.Errorf("Remaining() = %v, want [BD:0001 BD:0003]", remaining)
	}
}

func intPtr(i int) *int { return &i }
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/burn/... -run TestReconstructor -v`
Expected: FAIL — package doesn't have `NewReconstructor` yet

- [ ] **Step 3: Implement reconstruct.go**

```go
package burn

import (
	"fmt"

	"varis/internal/db"
	"varis/internal/xordisk"
)

// Reconstructor drives the manual "insert every other disc in the group"
// flow (spec §5) needed to rebuild one lost or unreadable disc.
type Reconstructor struct {
	missingID     string
	otherIDs      []string
	images        map[string][]byte
	capacityBytes int64
}

// NewReconstructor prepares to reconstruct missingID from every other disc
// in group. capacityBytes must be the same fixed media capacity
// buildParityPayload used when computing the group's parity disc — not
// any member's actual ISO size, which is exactly what's unknown for the
// missing disc. Every disc's media type within a group is the same (see
// plan 01's planJob), so capacityBytes is the group's single media type's
// capacity, e.g. from db.GetMediaTypeCapacity.
func NewReconstructor(group []db.Disk, missingID string, capacityBytes int64) *Reconstructor {
	r := &Reconstructor{missingID: missingID, images: map[string][]byte{}, capacityBytes: capacityBytes}
	for _, d := range group {
		if d.ID != missingID {
			r.otherIDs = append(r.otherIDs, d.ID)
		}
	}
	return r
}

// Remaining lists the disc IDs still needed, in the order they were found
// in the group.
func (r *Reconstructor) Remaining() []string {
	var out []string
	for _, id := range r.otherIDs {
		if _, ok := r.images[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}

func (r *Reconstructor) NeedsMore() bool {
	return len(r.Remaining()) > 0
}

// SupplyDiscImage records one other member's raw ISO bytes, already
// verified via par2verify by the caller before this is called.
func (r *Reconstructor) SupplyDiscImage(diskID string, image []byte) error {
	found := false
	for _, id := range r.otherIDs {
		if id == diskID {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("disc %s is not a member of this group", diskID)
	}
	r.images[diskID] = image
	return nil
}

// Reconstruct XORs every supplied image together, recovering the missing
// disc's image, padded/truncated to the group's fixed capacityBytes —
// the same length buildParityPayload used, which is what makes this
// correct regardless of which member is missing. Fails if any member is
// still unsupplied.
func (r *Reconstructor) Reconstruct() ([]byte, error) {
	if r.NeedsMore() {
		return nil, fmt.Errorf("still need discs: %v", r.Remaining())
	}
	var images [][]byte
	for _, id := range r.otherIDs {
		images = append(images, r.images[id])
	}
	return xordisk.XOR(images, r.capacityBytes), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/burn/... -v`
Expected: PASS (all burn package tests)

- [ ] **Step 5: Commit**

```bash
git add internal/burn/reconstruct.go internal/burn/reconstruct_test.go
git commit -m "$(cat <<'EOF'
Add group-reconstruction flow for a lost or unreadable disc

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

Note: `db.Disk.SlotIndex` is `*int` (nullable in Postgres) — the test above
uses `intPtr` to construct one inline; plan 03's HTTP layer will get these
values straight from `db.GroupMembers`, no conversion needed there.

---

## Plan Self-Review

**Spec coverage:** Opt-in toggle, default group size 10 with configurable
size, one parity disc per group (including a trailing short group), XOR
computation, and the "insert every other member, one at a time" manual
reconstruction flow (§5) are all covered. Normal single-disc retrieval is
untouched, matching the spec's "never touches this machinery unless the
specific disc fails" requirement — enforced structurally, since
`Reconstructor` is only ever constructed by plan 03's retrieval handler
when a `Get` fails verification, never on the happy path.

**Placeholder scan:** none — every step has runnable code. The unused
`parity` variable in Task 4's first test is deliberately computed and
discarded only to document *why* XORing `parity` and `b` would recover
`a`; removed the assertion's dependency on it by using the underscore
assignment.

**Type consistency:** `DiscPlan.GroupID`/`SlotIndex` (introduced in plan 01,
populated here), `Cataloger.NextGroupID` (new method, implemented on both
`pgxCataloger` and `FakeCataloger`), and `Reconstructor` all use `db.Disk`
and plain disk-ID strings consistently — no divergent ID types introduced.

**Correctness fix found on review:** the first draft of `buildParityPayload`
XORed data-disc images padded to `maxLen` (the largest *actual* ISO among
them), and `Reconstructor.Reconstruct` independently did the same from
whatever images it happened to be supplied. Those two lengths would only
agree by coincidence — real reconstruction needs both sides to pad to the
*same* length, and the only one knowable at reconstruction time (when the
missing disc's real size is exactly what's unknown) is the group's fixed
media capacity. Both were changed to use `job.Options.CapacityBytes` /
`Reconstructor.capacityBytes` instead. Plan 03's `StartReconstruction`
must pass this same capacity into `NewReconstructor` — see its Task 4.
