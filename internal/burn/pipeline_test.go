package burn

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"varis/internal/db"
	"varis/internal/execx"
	"varis/internal/xordisk"
)

// FakeCataloger is an in-memory Cataloger for tests.
type FakeCataloger struct {
	mu         sync.Mutex
	seq        map[string]int
	burnJobSeq int
	groupSeq   int
	Disks      []db.Disk
	Files      []db.FileRecord
	// StagedMetadata is populated directly by tests before starting the
	// Manager (single-goroutine setup, not under f.mu); ConsumeStagedMetadata
	// itself locks for its read+delete once the Manager is running.
	StagedMetadata map[string]db.StagedMetadata
}

func newFakeCataloger() *FakeCataloger {
	return &FakeCataloger{seq: map[string]int{}, StagedMetadata: map[string]db.StagedMetadata{}}
}

func (f *FakeCataloger) NextDiskID(ctx context.Context, prefix string, alreadyAllocated int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq[prefix]++
	return fmt.Sprintf("%s:%04d", prefix, f.seq[prefix]), nil
}

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

func (f *FakeCataloger) InsertDisk(ctx context.Context, d db.Disk) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Disks = append(f.Disks, d)
	return nil
}

func (f *FakeCataloger) InsertFile(ctx context.Context, fr db.FileRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Files = append(f.Files, fr)
	return nil
}

func (f *FakeCataloger) ConsumeStagedMetadata(ctx context.Context, path string) ([]string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.StagedMetadata[path]
	if !ok {
		return nil, "", nil
	}
	delete(f.StagedMetadata, path)
	return m.Tags, m.Description, nil
}

// fakeExecutorForHappyPath configures Funcs that simulate the filesystem
// side effects of par2create, xorriso, and wodim so runDisc's orchestration
// can be tested without real binaries.
func fakeExecutorForHappyPath(devicePath string) *execx.FakeExecutor {
	return &execx.FakeExecutor{
		Funcs: map[string]func(args []string) execx.Result{
			"par2create": func(args []string) execx.Result {
				dataPath := args[len(args)-1]
				os.WriteFile(dataPath+".par2", []byte("par2-index"), 0o644)
				return execx.Result{}
			},
			"xorriso": func(args []string) execx.Result {
				for i, a := range args {
					if a == "-o" && i+1 < len(args) {
						os.WriteFile(args[i+1], []byte("iso-bytes"), 0o644)
					}
				}
				return execx.Result{}
			},
			"wodim": func(args []string) execx.Result {
				isoPath := args[len(args)-1]
				data, _ := os.ReadFile(isoPath)
				os.WriteFile(devicePath, data, 0o644)
				return execx.Result{}
			},
			"par2verify": func(args []string) execx.Result {
				return execx.Result{}
			},
		},
	}
}

// fakeExecutorWithContentAwareISO is like fakeExecutorForHappyPath, except
// its fake xorriso embeds the real bytes of sourceDir (every file in it,
// concatenated in sorted order) into the ISO instead of a fixed placeholder.
// Parity-disc tests need this: buildParityPayload's XOR only reads distinct
// bytes from an actual data disc's ISO, so a fixed placeholder ISO would
// make every group's parity payload identically zero and unable to
// distinguish "computed the right XOR" from "computed nothing at all".
func fakeExecutorWithContentAwareISO(devicePath string) *execx.FakeExecutor {
	ex := fakeExecutorForHappyPath(devicePath)
	ex.Funcs["xorriso"] = func(args []string) execx.Result {
		sourceDir := args[len(args)-1]
		var outPath string
		for i, a := range args {
			if a == "-o" && i+1 < len(args) {
				outPath = args[i+1]
			}
		}
		entries, err := os.ReadDir(sourceDir)
		if err != nil {
			return execx.Result{Err: err}
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		var buf bytes.Buffer
		for _, n := range names {
			data, err := os.ReadFile(filepath.Join(sourceDir, n))
			if err != nil {
				return execx.Result{Err: err}
			}
			buf.Write(data)
		}
		if err := os.WriteFile(outPath, buf.Bytes(), 0o644); err != nil {
			return execx.Result{Err: err}
		}
		return execx.Result{}
	}
	return ex
}

func writeStagingFile(t *testing.T, dir, relPath, content string) {
	t.Helper()
	full := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestManager_HappyPath_SingleDisc(t *testing.T) {
	staging := t.TempDir()
	spool := t.TempDir()
	device := filepath.Join(t.TempDir(), "device")
	writeStagingFile(t, staging, "photo.jpg", "some bytes")

	cat := newFakeCataloger()
	ex := fakeExecutorForHappyPath(device)
	mgr := NewManager(cat, ex, staging, spool, device, t.TempDir())
	mgr.freeSpace = func(string) (uint64, error) { return 1 << 40, nil } // 1TB, plenty

	opts := Options{MediaType: "BD-R", CapacityBytes: 1000, ParityPercent: 10}
	if err := mgr.Start(context.Background(), opts); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if mgr.Current().State != StateAwaitingDisc {
		t.Fatalf("State = %s, want AWAITING_DISC", mgr.Current().State)
	}

	if err := mgr.ContinueNextDisc(context.Background()); err != nil {
		t.Fatalf("ContinueNextDisc: %v", err)
	}

	job := mgr.Current()
	if job.State != StateDone {
		t.Fatalf("State = %s, want DONE (err=%v)", job.State, job.Err)
	}
	if len(cat.Disks) != 1 || cat.Disks[0].ID != "BD:0001" {
		t.Errorf("Disks = %+v", cat.Disks)
	}
	if len(cat.Files) != 1 || cat.Files[0].OriginalPath != "photo.jpg" {
		t.Errorf("Files = %+v", cat.Files)
	}
	if _, err := os.Stat(filepath.Join(staging, "photo.jpg")); !os.IsNotExist(err) {
		t.Error("expected photo.jpg to be removed from staging after a successful burn")
	}
}

func TestManager_MultiDiscJob_BurnsSequentially(t *testing.T) {
	staging := t.TempDir()
	spool := t.TempDir()
	device := filepath.Join(t.TempDir(), "device")
	// CapacityBytes=1000, ParityPercent=10 -> target data size = 909 bytes.
	// Two 600-byte files can't share a bucket (600+600 > 909), so binpack.Pack
	// splits them into two buckets/discs.
	writeStagingFile(t, staging, "a.bin", strings.Repeat("a", 600))
	writeStagingFile(t, staging, "b.bin", strings.Repeat("b", 600))

	cat := newFakeCataloger()
	ex := fakeExecutorForHappyPath(device)
	mgr := NewManager(cat, ex, staging, spool, device, t.TempDir())
	mgr.freeSpace = func(string) (uint64, error) { return 1 << 40, nil }

	opts := Options{MediaType: "BD-R", CapacityBytes: 1000, ParityPercent: 10}
	if err := mgr.Start(context.Background(), opts); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := len(mgr.Current().Plans); got != 2 {
		t.Fatalf("len(Plans) = %d, want 2", got)
	}

	// First disc: should land back on AWAITING_DISC, not DONE.
	if err := mgr.ContinueNextDisc(context.Background()); err != nil {
		t.Fatalf("ContinueNextDisc (disc 1): %v", err)
	}
	job := mgr.Current()
	if job.State != StateAwaitingDisc {
		t.Fatalf("after disc 1: State = %s, want AWAITING_DISC (err=%v)", job.State, job.Err)
	}
	if job.CurrentIndex != 1 {
		t.Fatalf("after disc 1: CurrentIndex = %d, want 1", job.CurrentIndex)
	}

	// Second (last) disc: only now should it reach DONE.
	if err := mgr.ContinueNextDisc(context.Background()); err != nil {
		t.Fatalf("ContinueNextDisc (disc 2): %v", err)
	}
	job = mgr.Current()
	if job.State != StateDone {
		t.Fatalf("after disc 2: State = %s, want DONE (err=%v)", job.State, job.Err)
	}
	if job.CurrentIndex != 2 {
		t.Fatalf("after disc 2: CurrentIndex = %d, want 2", job.CurrentIndex)
	}

	if len(cat.Disks) != 2 || cat.Disks[0].ID != "BD:0001" || cat.Disks[1].ID != "BD:0002" {
		t.Errorf("Disks = %+v", cat.Disks)
	}
	if len(cat.Files) != 2 {
		t.Errorf("Files = %+v", cat.Files)
	}
	for _, name := range []string{"a.bin", "b.bin"} {
		if _, err := os.Stat(filepath.Join(staging, name)); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed from staging after burning", name)
		}
	}
}

func TestManager_FailedStepLeavesStagingUntouchedAndAllowsRetry(t *testing.T) {
	staging := t.TempDir()
	spool := t.TempDir()
	device := filepath.Join(t.TempDir(), "device")
	writeStagingFile(t, staging, "photo.jpg", "some bytes")

	cat := newFakeCataloger()
	ex := fakeExecutorForHappyPath(device)
	ex.Funcs["wodim"] = func(args []string) execx.Result {
		return execx.Result{Err: fmt.Errorf("burn failed: bad disc")}
	}
	mgr := NewManager(cat, ex, staging, spool, device, t.TempDir())
	mgr.freeSpace = func(string) (uint64, error) { return 1 << 40, nil }

	opts := Options{MediaType: "BD-R", CapacityBytes: 1000, ParityPercent: 10}
	if err := mgr.Start(context.Background(), opts); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := mgr.ContinueNextDisc(context.Background()); err == nil {
		t.Fatal("expected ContinueNextDisc to fail")
	}
	if mgr.Current().State != StateFailed {
		t.Fatalf("State = %s, want FAILED", mgr.Current().State)
	}
	if _, err := os.Stat(filepath.Join(staging, "photo.jpg")); err != nil {
		t.Error("expected photo.jpg to remain in staging after a failed burn")
	}

	// Fix the fake burn and retry.
	ex.Funcs["wodim"] = fakeExecutorForHappyPath(device).Funcs["wodim"]
	if err := mgr.Retry(context.Background()); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if err := mgr.ContinueNextDisc(context.Background()); err != nil {
		t.Fatalf("ContinueNextDisc after retry: %v", err)
	}
	if mgr.Current().State != StateDone {
		t.Fatalf("State = %s, want DONE after retry", mgr.Current().State)
	}
}

func TestManager_RejectsSecondJobWhileOneInProgress(t *testing.T) {
	staging := t.TempDir()
	spool := t.TempDir()
	device := filepath.Join(t.TempDir(), "device")
	writeStagingFile(t, staging, "photo.jpg", "some bytes")

	mgr := NewManager(newFakeCataloger(), fakeExecutorForHappyPath(device), staging, spool, device, t.TempDir())
	mgr.freeSpace = func(string) (uint64, error) { return 1 << 40, nil }

	opts := Options{MediaType: "BD-R", CapacityBytes: 1000, ParityPercent: 10}
	if err := mgr.Start(context.Background(), opts); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := mgr.Start(context.Background(), opts); err == nil {
		t.Fatal("expected second Start to be rejected while a job is AWAITING_DISC")
	}
}

func TestManager_RejectsWhenNotEnoughFreeSpace(t *testing.T) {
	staging := t.TempDir()
	spool := t.TempDir()
	device := filepath.Join(t.TempDir(), "device")
	writeStagingFile(t, staging, "photo.jpg", "some bytes")

	mgr := NewManager(newFakeCataloger(), fakeExecutorForHappyPath(device), staging, spool, device, t.TempDir())
	mgr.freeSpace = func(string) (uint64, error) { return 100, nil } // way too little

	opts := Options{MediaType: "BD-R", CapacityBytes: 1000, ParityPercent: 10}
	err := mgr.Start(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "not enough free space") {
		t.Fatalf("Start error = %v, want a free-space error", err)
	}
}

func TestManager_CommitDisc_FoldsStagedMetadataIntoFileRecord(t *testing.T) {
	staging := t.TempDir()
	writeStagingFile(t, staging, "a.bin", "hello")

	device := filepath.Join(t.TempDir(), "device")
	cat := newFakeCataloger()
	cat.StagedMetadata["a.bin"] = db.StagedMetadata{Tags: []string{"family"}, Description: "a note"}
	ex := fakeExecutorForHappyPath(device)

	mgr := NewManager(cat, ex, staging, t.TempDir(), device, t.TempDir())
	mgr.freeSpace = func(string) (uint64, error) { return 1 << 40, nil }
	if err := mgr.Start(context.Background(), Options{MediaType: "BD-R", CapacityBytes: 1000, ParityPercent: 10}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := mgr.ContinueNextDisc(context.Background()); err != nil {
		t.Fatalf("ContinueNextDisc: %v", err)
	}

	if len(cat.Files) != 1 {
		t.Fatalf("Files = %v, want exactly 1", cat.Files)
	}
	f := cat.Files[0]
	if len(f.Tags) != 1 || f.Tags[0] != "family" || f.Description != "a note" {
		t.Errorf("committed FileRecord = %+v, want Tags=[family] Description=\"a note\"", f)
	}
	if _, stillStaged := cat.StagedMetadata["a.bin"]; stillStaged {
		t.Error("expected a.bin's staged metadata to be consumed (deleted) once burned")
	}
}

func TestManager_CommitDisc_UntaggedFileGetsEmptyMetadata(t *testing.T) {
	staging := t.TempDir()
	writeStagingFile(t, staging, "b.bin", "hello")

	device := filepath.Join(t.TempDir(), "device")
	cat := newFakeCataloger()
	ex := fakeExecutorForHappyPath(device)

	mgr := NewManager(cat, ex, staging, t.TempDir(), device, t.TempDir())
	mgr.freeSpace = func(string) (uint64, error) { return 1 << 40, nil }
	if err := mgr.Start(context.Background(), Options{MediaType: "BD-R", CapacityBytes: 1000, ParityPercent: 10}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := mgr.ContinueNextDisc(context.Background()); err != nil {
		t.Fatalf("ContinueNextDisc: %v", err)
	}

	if len(cat.Files) != 1 {
		t.Fatalf("Files = %v, want exactly 1", cat.Files)
	}
	if len(cat.Files[0].Tags) != 0 || cat.Files[0].Description != "" {
		t.Errorf("committed FileRecord = %+v, want empty Tags/Description for an untagged file", cat.Files[0])
	}
}

// TestManager_Current_SafeForConcurrentReadsDuringBurn is a regression test
// for a data race: Current() used to return the live *Job pointer, which
// concurrent callers read (.State/.CurrentIndex/.Err) without a lock while
// runDisc's setState mutated it under m.mu. Run with -race: it must not
// report a race. A slow step gives concurrent Current() calls a real chance
// to overlap with the in-flight state mutations.
func TestManager_Current_SafeForConcurrentReadsDuringBurn(t *testing.T) {
	staging := t.TempDir()
	spool := t.TempDir()
	device := filepath.Join(t.TempDir(), "device")
	writeStagingFile(t, staging, "photo.jpg", "some bytes")

	cat := newFakeCataloger()
	ex := fakeExecutorForHappyPath(device)
	slowXorriso := ex.Funcs["xorriso"]
	ex.Funcs["xorriso"] = func(args []string) execx.Result {
		time.Sleep(20 * time.Millisecond)
		return slowXorriso(args)
	}
	mgr := NewManager(cat, ex, staging, spool, device, t.TempDir())
	mgr.freeSpace = func(string) (uint64, error) { return 1 << 40, nil }

	opts := Options{MediaType: "BD-R", CapacityBytes: 1000, ParityPercent: 10}
	if err := mgr.Start(context.Background(), opts); err != nil {
		t.Fatalf("Start: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- mgr.ContinueNextDisc(context.Background())
	}()

	for i := 0; i < 100; i++ {
		job := mgr.Current()
		if job != nil {
			_ = job.State
			_ = job.CurrentIndex
			_ = job.Err
		}
		time.Sleep(time.Millisecond)
	}

	if err := <-done; err != nil {
		t.Fatalf("ContinueNextDisc: %v", err)
	}
	if mgr.Current().State != StateDone {
		t.Fatalf("State = %s, want DONE", mgr.Current().State)
	}
}

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

func TestPlanJob_ExactMultipleOfGroupSizeHasNoRemainderGroup(t *testing.T) {
	staging := t.TempDir()
	for i := 0; i < 4; i++ {
		writeStagingFile(t, staging, fmt.Sprintf("f%d.bin", i), strings.Repeat("x", 100))
	}
	cat := newFakeCataloger()

	opts := Options{
		MediaType:       "BD-R",
		CapacityBytes:   150,
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
		if p.Role == "data" {
			dataCount++
			groupIDs[p.GroupID] = true
		} else {
			parityCount++
		}
	}
	if dataCount != 4 {
		t.Fatalf("dataCount = %d, want 4", dataCount)
	}
	// 4 data discs, group size 2 -> groups of [2,2] -> exactly 2 parity discs.
	if parityCount != 2 {
		t.Fatalf("parityCount = %d, want 2", parityCount)
	}
	if len(groupIDs) != 2 {
		t.Fatalf("distinct groups = %d, want 2", len(groupIDs))
	}
}

func TestPlanJob_GroupSizeZeroDefaultsToTen(t *testing.T) {
	staging := t.TempDir()
	for i := 0; i < 3; i++ {
		writeStagingFile(t, staging, fmt.Sprintf("f%d.bin", i), strings.Repeat("x", 100))
	}
	cat := newFakeCataloger()

	opts := Options{
		MediaType:       "BD-R",
		CapacityBytes:   150,
		ParityPercent:   10,
		CrossDiscParity: true,
		GroupSize:       0,
	}

	plans, err := planJob(context.Background(), staging, opts, cat)
	if err != nil {
		t.Fatalf("planJob: %v", err)
	}

	var dataCount, parityCount int
	groupIDs := map[string]bool{}
	for _, p := range plans {
		if p.Role == "data" {
			dataCount++
			groupIDs[p.GroupID] = true
		} else {
			parityCount++
		}
	}
	if dataCount != 3 {
		t.Fatalf("dataCount = %d, want 3", dataCount)
	}
	// GroupSize 0 defaults to 10, so all 3 data discs land in one group.
	if parityCount != 1 {
		t.Fatalf("parityCount = %d, want 1", parityCount)
	}
	if len(groupIDs) != 1 {
		t.Fatalf("distinct groups = %d, want 1", len(groupIDs))
	}
}

// countBasedCataloger mimics the real db.NextDiskID's behavior: NextDiskID
// bases its suffix purely on how many disks with that prefix have actually
// been inserted so far (like `count(*) FROM disks WHERE id LIKE ...`), not
// on how many IDs this cataloger has handed out. Since planJob never calls
// InsertDisk during planning (that only happens later, per-disc, in
// commitDisc as each disc finishes burning), insertedByPrefix stays 0
// throughout a planning pass — exactly the scenario that caused every
// NextDiskID call in one planJob invocation to collide on the same ID
// before alreadyAllocated was threaded through.
type countBasedCataloger struct {
	insertedByPrefix map[string]int
}

func (c *countBasedCataloger) NextDiskID(ctx context.Context, prefix string, alreadyAllocated int) (string, error) {
	return fmt.Sprintf("%s:%04d", prefix, c.insertedByPrefix[prefix]+alreadyAllocated+1), nil
}
func (c *countBasedCataloger) NewBurnJobID(ctx context.Context) (string, error) {
	return "burnjob-1", nil
}
func (c *countBasedCataloger) NextGroupID(ctx context.Context, burnJobID string, groupSize int) (string, error) {
	return "group-1", nil
}
func (c *countBasedCataloger) InsertDisk(ctx context.Context, d db.Disk) error       { return nil }
func (c *countBasedCataloger) InsertFile(ctx context.Context, f db.FileRecord) error { return nil }
func (c *countBasedCataloger) ConsumeStagedMetadata(ctx context.Context, path string) ([]string, string, error) {
	return nil, "", nil
}

// TestPlanJob_AllocatesDistinctDiskIDsWithinOnePlanningPass is a regression
// test for a bug where planJob asked a count(*)-based Cataloger for one
// DiskID per disc in a job without ever inserting anything in between
// (InsertDisk happens later, one disc at a time, as each disc actually
// finishes burning) — every call within the same planning pass saw the same
// DB count and returned the identical DiskID for every disc, which would
// fail with a primary-key violation on the second InsertDisk of a real
// multi-disc burn. FakeCataloger's own incrementing counter doesn't model
// this, so it can't catch the regression; countBasedCataloger does.
func TestPlanJob_AllocatesDistinctDiskIDsWithinOnePlanningPass(t *testing.T) {
	staging := t.TempDir()
	for i := 0; i < 4; i++ {
		writeStagingFile(t, staging, fmt.Sprintf("f%d.bin", i), strings.Repeat("x", 100))
	}
	cat := &countBasedCataloger{insertedByPrefix: map[string]int{}}

	opts := Options{MediaType: "BD-R", CapacityBytes: 150, ParityPercent: 10}
	plans, err := planJob(context.Background(), staging, opts, cat)
	if err != nil {
		t.Fatalf("planJob: %v", err)
	}
	if len(plans) != 4 {
		t.Fatalf("len(plans) = %d, want 4", len(plans))
	}

	seen := map[string]bool{}
	for i, p := range plans {
		if seen[p.DiskID] {
			t.Fatalf("plan[%d].DiskID = %q is a duplicate", i, p.DiskID)
		}
		seen[p.DiskID] = true
		want := fmt.Sprintf("BD:%04d", i+1)
		if p.DiskID != want {
			t.Errorf("plan[%d].DiskID = %q, want %q", i, p.DiskID, want)
		}
	}
}

// TestManager_ContinueNextDisc_ConcurrentCallsRunExactlyOnce is a regression
// test for a TOCTOU race: ContinueNextDisc used to check job.State outside
// the lock and only transition state deep inside runDisc, leaving a window
// where two concurrent callers could both observe AWAITING_DISC and both
// burn the same disc plan. Now the check-and-claim happens atomically, so
// exactly one of two simultaneous callers should succeed and exactly one
// wodim (burn) call should happen.
func TestManager_ContinueNextDisc_ConcurrentCallsRunExactlyOnce(t *testing.T) {
	staging := t.TempDir()
	spool := t.TempDir()
	device := filepath.Join(t.TempDir(), "device")
	writeStagingFile(t, staging, "photo.jpg", "some bytes")

	cat := newFakeCataloger()
	ex := fakeExecutorForHappyPath(device)
	mgr := NewManager(cat, ex, staging, spool, device, t.TempDir())
	mgr.freeSpace = func(string) (uint64, error) { return 1 << 40, nil }

	opts := Options{MediaType: "BD-R", CapacityBytes: 1000, ParityPercent: 10}
	if err := mgr.Start(context.Background(), opts); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	for i := range errs {
		i := i
		go func() {
			defer wg.Done()
			errs[i] = mgr.ContinueNextDisc(context.Background())
		}()
	}
	wg.Wait()

	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successes = %d, want exactly 1 (errs=%v)", successes, errs)
	}

	wodimCalls := 0
	for _, c := range ex.Calls() {
		if c.Name == "wodim" {
			wodimCalls++
		}
	}
	if wodimCalls != 1 {
		t.Fatalf("wodim called %d times, want exactly 1 (a TOCTOU race would double-burn the same disc)", wodimCalls)
	}
	if len(cat.Disks) != 1 {
		t.Fatalf("Disks = %+v, want exactly 1", cat.Disks)
	}
}

func TestManager_BurnsGroupWithParityDisc(t *testing.T) {
	staging := t.TempDir()
	spool := t.TempDir()
	device := filepath.Join(t.TempDir(), "device")
	writeStagingFile(t, staging, "a.bin", strings.Repeat("A", 60))
	writeStagingFile(t, staging, "b.bin", strings.Repeat("B", 60))

	cat := newFakeCataloger()
	ex := fakeExecutorWithContentAwareISO(device)
	mgr := NewManager(cat, ex, staging, spool, device, t.TempDir())
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

	plans := mgr.Current().Plans
	if len(plans) != 3 {
		t.Fatalf("plans = %d, want 3 (got %+v)", len(plans), plans)
	}

	// 2 data discs + 1 parity disc for the group. Capture each data disc's
	// burned ISO bytes right after it's burned, since the group's parity
	// disc (burned last here) reclaims them once it commits.
	var dataISOs [][]byte
	for i, p := range plans {
		if err := mgr.ContinueNextDisc(context.Background()); err != nil {
			t.Fatalf("ContinueNextDisc[%d]: %v (job err: %v)", i, err, mgr.Current().Err)
		}
		if p.Role == "data" {
			data, err := os.ReadFile(filepath.Join(spool, p.DiskID+".iso"))
			if err != nil {
				t.Fatalf("reading data disc %s iso: %v", p.DiskID, err)
			}
			dataISOs = append(dataISOs, data)
		}
	}
	if len(dataISOs) != 2 {
		t.Fatalf("captured %d data disc ISOs, want 2", len(dataISOs))
	}

	job := mgr.Current()
	if job.State != StateDone {
		t.Fatalf("State = %s, want DONE", job.State)
	}

	var parityDisks int
	var groupID string
	slotByID := map[string]int{}
	for _, d := range cat.Disks {
		if d.GroupID == nil || d.SlotIndex == nil {
			t.Fatalf("disk %s committed with nil GroupID/SlotIndex: %+v", d.ID, d)
		}
		if groupID == "" {
			groupID = *d.GroupID
		} else if *d.GroupID != groupID {
			t.Fatalf("disk %s GroupID = %s, want %s (every disc in the job belongs to the same group here)", d.ID, *d.GroupID, groupID)
		}
		slotByID[d.ID] = *d.SlotIndex
		if d.Role == "parity" {
			parityDisks++
		}
	}
	if parityDisks != 1 {
		t.Fatalf("parityDisks = %d, want 1 (got disks: %+v)", parityDisks, cat.Disks)
	}
	wantSlots := map[string]int{plans[0].DiskID: 0, plans[1].DiskID: 1, plans[2].DiskID: 2}
	for id, want := range wantSlots {
		if got := slotByID[id]; got != want {
			t.Errorf("SlotIndex[%s] = %d, want %d", id, got, want)
		}
	}

	// The parity disc's actual committed payload must be the real XOR of
	// the two data discs' ISO bytes, padded/truncated to CapacityBytes —
	// not just "some parity-role disc got inserted".
	wantParity := xordisk.XOR(dataISOs, opts.CapacityBytes)
	parityID := plans[2].DiskID
	gotParity, err := os.ReadFile(filepath.Join(spool, parityID+"-src", parityID+".tar"))
	if err != nil {
		t.Fatalf("reading parity payload: %v", err)
	}
	if !bytes.Equal(gotParity, wantParity) {
		t.Fatalf("parity payload = %x, want XOR of data discs = %x", gotParity, wantParity)
	}

	// Once the group's parity disc has committed, its data discs' spool
	// ISOs should have been reclaimed.
	for _, p := range plans[:2] {
		if _, err := os.Stat(filepath.Join(spool, p.DiskID+".iso")); !os.IsNotExist(err) {
			t.Errorf("data disc %s iso still present in spool after its group's parity disc committed", p.DiskID)
		}
	}
}

func TestManager_ParityPayload_DoesNotLeakAcrossGroups(t *testing.T) {
	staging := t.TempDir()
	spool := t.TempDir()
	device := filepath.Join(t.TempDir(), "device")
	writeStagingFile(t, staging, "a.bin", strings.Repeat("A", 60))
	writeStagingFile(t, staging, "b.bin", strings.Repeat("B", 60))
	writeStagingFile(t, staging, "c.bin", strings.Repeat("C", 60))
	writeStagingFile(t, staging, "d.bin", strings.Repeat("D", 60))

	cat := newFakeCataloger()
	ex := fakeExecutorWithContentAwareISO(device)
	mgr := NewManager(cat, ex, staging, spool, device, t.TempDir())
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

	plans := mgr.Current().Plans
	if len(plans) != 6 {
		t.Fatalf("plans = %d, want 6 (2 groups of 2 data + 1 parity each), got %+v", len(plans), plans)
	}

	dataISOsByGroup := map[string][][]byte{}
	for i, p := range plans {
		if err := mgr.ContinueNextDisc(context.Background()); err != nil {
			t.Fatalf("ContinueNextDisc[%d]: %v (job err: %v)", i, err, mgr.Current().Err)
		}
		if p.Role == "data" {
			data, err := os.ReadFile(filepath.Join(spool, p.DiskID+".iso"))
			if err != nil {
				t.Fatalf("reading data disc %s iso: %v", p.DiskID, err)
			}
			dataISOsByGroup[p.GroupID] = append(dataISOsByGroup[p.GroupID], data)
		}
	}

	job := mgr.Current()
	if job.State != StateDone {
		t.Fatalf("State = %s, want DONE", job.State)
	}
	if len(dataISOsByGroup) != 2 {
		t.Fatalf("groups with captured data ISOs = %d, want 2", len(dataISOsByGroup))
	}

	var parityPlans []DiscPlan
	for _, p := range plans {
		if p.Role == "parity" {
			parityPlans = append(parityPlans, p)
		}
	}
	if len(parityPlans) != 2 {
		t.Fatalf("parity plans = %d, want 2", len(parityPlans))
	}

	var parityPayloads [][]byte
	for _, pp := range parityPlans {
		want := xordisk.XOR(dataISOsByGroup[pp.GroupID], opts.CapacityBytes)
		got, err := os.ReadFile(filepath.Join(spool, pp.DiskID+"-src", pp.DiskID+".tar"))
		if err != nil {
			t.Fatalf("reading parity payload for group %s: %v", pp.GroupID, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("group %s parity payload = %x, want XOR of its own 2 data discs = %x", pp.GroupID, got, want)
		}
		parityPayloads = append(parityPayloads, got)
	}

	// Distinct file content per group must produce distinct parity
	// payloads; if the two groups' payloads matched, that would mean one
	// group's parity disc leaked in the other group's data.
	if bytes.Equal(parityPayloads[0], parityPayloads[1]) {
		t.Fatalf("the two groups' parity payloads are identical (%x) — suggests cross-group leakage", parityPayloads[0])
	}
}

func TestManager_DryRun_CopiesISOInsteadOfBurning(t *testing.T) {
	staging := t.TempDir()
	writeStagingFile(t, staging, "a.bin", "hello")
	dryRunDir := t.TempDir()

	cat := newFakeCataloger()
	ex := fakeExecutorForHappyPath(filepath.Join(t.TempDir(), "device")) // registers par2create/xorriso/par2verify; wodim would just no-op if called, so we separately assert it never is

	mgr := NewManager(cat, ex, staging, t.TempDir(), t.TempDir()+"/device", dryRunDir)
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
