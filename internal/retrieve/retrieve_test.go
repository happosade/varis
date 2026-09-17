package retrieve

import (
	"archive/tar"
	"bytes"
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

// TestManager_ReadDisk_FileNotInTarRejected exercises the second
// StateFailed exception in readFrom: the TOC matches and the tar's parity
// verifies fine (i.e. the disc itself is intact), but the requested
// file's OriginalPath isn't a member of that tar. Since the tar is proven
// intact, this is a cataloging/metadata problem, not media damage, so it
// must land in StateFailed rather than StateNeedsReconstruction.
func TestManager_ReadDisk_FileNotInTarRejected(t *testing.T) {
	retrievedDir := t.TempDir()
	scratchDir := t.TempDir()
	device := filepath.Join(t.TempDir(), "device")

	tocBytes, _ := toc.TOC{DiskID: "BD:0001", Compressed: false}.Marshal()

	tarPath := filepath.Join(t.TempDir(), "src.tar")
	writeTarFixture(t, tarPath, map[string]string{"other.jpg": "bytes"})
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
	if err := mgr.ReadDisk(context.Background()); err == nil {
		t.Fatal("expected file-not-found error")
	}
	if mgr.Current().State != StateFailed {
		t.Fatalf("State = %s, want FAILED", mgr.Current().State)
	}
}

// srcAwareDiscExecutor is like fakeDiscExecutor, but only serves files
// when the requested src (the -indev device/image path) equals goodSrc.
// Used to distinguish "reading the original physical device" (which
// should fail, simulating damaged media) from "reading the reconstructed
// .iso image" (which should succeed) within a single test, since
// ReadReconstructionDisc/readFrom compute the reconstructed image's path
// deterministically as "<scratchDir>/<job.DiskID>.reconstructed.iso".
// srcAwareDiscExecutor's xorriso fake serves fixture content by exact
// (src, pathInISO) match against goodSrc, and — for any other src (e.g. a
// parity disc's physical device, extracted during reconstruction to pull
// out its raw payload) — passes that src file's raw bytes straight
// through regardless of pathInISO. Reconstruction tests only care that
// some payload bytes get read and folded in, not that they're byte
// -accurate; the reconstructed image is what actually gets decoded via
// goodSrc's fixture content, checked precisely.
func srcAwareDiscExecutor(goodSrc string, files map[string][]byte) *execx.FakeExecutor {
	return &execx.FakeExecutor{
		Funcs: map[string]func(args []string) execx.Result{
			"xorriso": func(args []string) execx.Result {
				src := args[1]
				pathInISO := args[3] // ["-indev", src, "-extract", pathInISO, destPath]
				destPath := args[4]
				if src != goodSrc {
					data, err := os.ReadFile(src)
					if err != nil {
						return execx.Result{Err: os.ErrNotExist}
					}
					return execx.Result{Err: os.WriteFile(destPath, data, 0o644)}
				}
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

// TestManager_ReconstructionFlow_HappyPath exercises the full "disc failed,
// reconstruct from group" recovery flow end-to-end: a 2-member group (one
// data disc, one parity disc), a ReadDisk that fails against the physical
// device (simulating damaged media), StartReconstruction, Remaining()
// listing the surviving parity disc, ReadReconstructionDisc supplying it,
// and the final readFrom succeeding against the reconstructed image.
func TestManager_ReconstructionFlow_HappyPath(t *testing.T) {
	retrievedDir := t.TempDir()
	scratchDir := t.TempDir()
	device := filepath.Join(t.TempDir(), "device")

	// The missing disc is BD:0001 (a data disc); BD:0002 is its group's
	// parity disc, the only other member — with just one data disc in the
	// group, the parity disc's payload is that data disc's image
	// zero-padded to capacity (see xordisk.XOR), so reconstruction from it
	// alone recovers BD:0001's image exactly.
	groupID := "g1"
	cat := fakeCatalog{
		files: map[string]db.FileRecord{
			"file1": {ID: "file1", DiskID: "BD:0001", OriginalPath: "photo.jpg"},
		},
		disks: map[string]db.Disk{
			"BD:0001": {ID: "BD:0001", MediaType: "BD-R", Role: "data", GroupID: &groupID, SlotIndex: intPtr(0)},
			"BD:0002": {ID: "BD:0002", MediaType: "BD-R", Role: "parity", GroupID: &groupID, SlotIndex: intPtr(1)},
		},
	}

	tocBytes, _ := toc.TOC{DiskID: "BD:0001", Compressed: false}.Marshal()
	tarPath := filepath.Join(t.TempDir(), "src.tar")
	writeTarFixture(t, tarPath, map[string]string{"photo.jpg": "bytes"})
	tarBytes, err := os.ReadFile(tarPath)
	if err != nil {
		t.Fatal(err)
	}

	// The reconstructed image's path is deterministic: readFrom is always
	// called with "<scratchDir>/<job.DiskID>.reconstructed.iso" once
	// ReadReconstructionDisc finishes reconstructing. Only that path (not
	// the physical device) serves BD:0001's TOC/tar/parity-index, so the
	// initial ReadDisk against the device fails (needs reconstruction) and
	// the later read against the reconstructed image succeeds.
	reconstructedPath := filepath.Join(scratchDir, "BD:0001.reconstructed.iso")
	ex := srcAwareDiscExecutor(reconstructedPath, map[string][]byte{
		"/BD:0001.toc.json": tocBytes,
		"/BD:0001.tar":       tarBytes,
		"/BD:0001.tar.par2":  []byte("index"),
	})

	mgr := NewManager(cat, ex, retrievedDir, scratchDir, device)
	if err := mgr.Start(context.Background(), "file1"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := mgr.ReadDisk(context.Background()); err == nil {
		t.Fatal("expected ReadDisk against the physical device to fail")
	}
	if mgr.Current().State != StateNeedsReconstruction {
		t.Fatalf("State = %s, want NEEDS_RECONSTRUCTION", mgr.Current().State)
	}

	if err := mgr.StartReconstruction(context.Background()); err != nil {
		t.Fatalf("StartReconstruction: %v", err)
	}
	if mgr.Current().State != StateReconstructing {
		t.Fatalf("State = %s, want RECONSTRUCTING", mgr.Current().State)
	}
	remaining := mgr.Remaining()
	if len(remaining) != 1 || remaining[0] != "BD:0002" {
		t.Fatalf("Remaining() = %v, want [BD:0002]", remaining)
	}

	// Simulate physically inserting BD:0002 and reading it raw.
	if err := os.WriteFile(device, bytes.Repeat([]byte{0xBB}, 1000), 0o644); err != nil {
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
		t.Errorf("content = %q, want bytes", got)
	}
	if _, err := os.Stat(filepath.Join(scratchDir, "BD:0002.img")); !os.IsNotExist(err) {
		t.Errorf("expected per-member reconstruction image to be cleaned up, stat err = %v", err)
	}
	if _, err := os.Stat(reconstructedPath); !os.IsNotExist(err) {
		t.Errorf("expected reconstructed image to be cleaned up, stat err = %v", err)
	}
}

// TestManager_ReadReconstructionDisc_UnknownMemberReverts confirms that a
// failure inside ReadReconstructionDisc (here, supplying a disc ID that
// isn't a member of the group) records the error on the job and reverts
// its state back to StateReconstructing — not stuck in the transient
// "reading" state, and not silently ignored.
func TestManager_ReadReconstructionDisc_UnknownMemberReverts(t *testing.T) {
	retrievedDir := t.TempDir()
	scratchDir := t.TempDir()
	device := filepath.Join(t.TempDir(), "device")

	groupID := "g1"
	cat := fakeCatalog{
		files: map[string]db.FileRecord{
			"file1": {ID: "file1", DiskID: "BD:0001", OriginalPath: "photo.jpg"},
		},
		disks: map[string]db.Disk{
			"BD:0001": {ID: "BD:0001", MediaType: "BD-R", Role: "data", GroupID: &groupID, SlotIndex: intPtr(0)},
			"BD:0002": {ID: "BD:0002", MediaType: "BD-R", Role: "parity", GroupID: &groupID, SlotIndex: intPtr(1)},
		},
	}
	ex := fakeDiscExecutor(nil) // ReadDisk against the device always fails here

	mgr := NewManager(cat, ex, retrievedDir, scratchDir, device)
	if err := mgr.Start(context.Background(), "file1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := mgr.ReadDisk(context.Background()); err == nil {
		t.Fatal("expected ReadDisk to fail")
	}
	if err := mgr.StartReconstruction(context.Background()); err != nil {
		t.Fatalf("StartReconstruction: %v", err)
	}

	if err := os.WriteFile(device, bytes.Repeat([]byte{0xAA}, 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mgr.ReadReconstructionDisc(context.Background(), "BD:9999"); err == nil {
		t.Fatal("expected error supplying an unknown group member")
	}

	job := mgr.Current()
	if job.State != StateReconstructing {
		t.Fatalf("State = %s, want RECONSTRUCTING", job.State)
	}
	if job.Err == nil {
		t.Fatal("expected job.Err to be set")
	}
	// Remaining() still works — the reconstructor wasn't torn down.
	remaining := mgr.Remaining()
	if len(remaining) != 1 || remaining[0] != "BD:0002" {
		t.Fatalf("Remaining() = %v, want [BD:0002]", remaining)
	}
}

func intPtr(i int) *int { return &i }

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
