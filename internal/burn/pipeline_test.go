package burn

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"varis/internal/db"
	"varis/internal/execx"
)

// FakeCataloger is an in-memory Cataloger for tests.
type FakeCataloger struct {
	mu    sync.Mutex
	seq   map[string]int
	Disks []db.Disk
	Files []db.FileRecord
}

func newFakeCataloger() *FakeCataloger {
	return &FakeCataloger{seq: map[string]int{}}
}

func (f *FakeCataloger) NextDiskID(ctx context.Context, prefix string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq[prefix]++
	return fmt.Sprintf("%s:%04d", prefix, f.seq[prefix]), nil
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
	mgr := NewManager(cat, ex, staging, spool, device)
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
	mgr := NewManager(cat, ex, staging, spool, device)
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

	mgr := NewManager(newFakeCataloger(), fakeExecutorForHappyPath(device), staging, spool, device)
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

	mgr := NewManager(newFakeCataloger(), fakeExecutorForHappyPath(device), staging, spool, device)
	mgr.freeSpace = func(string) (uint64, error) { return 100, nil } // way too little

	opts := Options{MediaType: "BD-R", CapacityBytes: 1000, ParityPercent: 10}
	err := mgr.Start(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "not enough free space") {
		t.Fatalf("Start error = %v, want a free-space error", err)
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
	mgr := NewManager(cat, ex, staging, spool, device)
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
	mgr := NewManager(cat, ex, staging, spool, device)
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
