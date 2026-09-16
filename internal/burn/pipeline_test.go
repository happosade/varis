package burn

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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
