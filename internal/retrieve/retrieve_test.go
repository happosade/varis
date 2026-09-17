package retrieve

import (
	"archive/tar"
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
				pathInISO := args[3] // ["-indev", src, "-extract", pathInISO, destPath]
				destPath := args[4]
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

// writeTarFixture is a tiny local helper using archive/tar directly (not
// exported from the burn package on purpose — retrieve's tests shouldn't
// need to import burn just to build a fixture).
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
