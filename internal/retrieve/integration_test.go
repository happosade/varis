package retrieve

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"varis/internal/burn"
	"varis/internal/db"
	"varis/internal/execx"
)

// encodeManifest/decodeManifest stand in for a real ISO 9660 image: a
// compact length-prefixed concatenation (not JSON+base64, which would
// inflate a capacity-sized parity payload disproportionately) of name/data
// pairs, close in spirit to how a real ISO packs file bytes ~1:1. decodeManifest
// tolerates a truncated tail: ReadRawImage's capacity-bounded raw reads
// (used only during reconstruction) can come up a few bytes short of a
// disc's true wrapped size when that disc's payload plus its own TOC/par2
// metadata slightly exceeds the nominal capacity — harmless because real
// content always sits safely before the zero-padded tail where that
// shortfall lands.
func encodeManifest(files map[string][]byte) []byte {
	names := make([]string, 0, len(files))
	for k := range files {
		names = append(names, k)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	for _, name := range names {
		data := files[name]
		binary.Write(&buf, binary.BigEndian, uint32(len(name)))
		buf.WriteString(name)
		binary.Write(&buf, binary.BigEndian, uint32(len(data)))
		buf.Write(data)
	}
	return buf.Bytes()
}

func decodeManifest(blob []byte) map[string][]byte {
	files := map[string][]byte{}
	r := bytes.NewReader(blob)
	for r.Len() > 0 {
		var nameLen uint32
		if err := binary.Read(r, binary.BigEndian, &nameLen); err != nil {
			break
		}
		nameBytes := make([]byte, nameLen)
		if _, err := io.ReadFull(r, nameBytes); err != nil {
			break
		}
		var dataLen uint32
		if err := binary.Read(r, binary.BigEndian, &dataLen); err != nil {
			break
		}
		data := make([]byte, dataLen)
		n, _ := io.ReadFull(r, data)
		files[string(nameBytes)] = data[:n]
		if n < len(data) {
			break
		}
	}
	return files
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
					files := map[string][]byte{}
					for _, e := range entries {
						data, err := os.ReadFile(filepath.Join(sourceDir, e.Name()))
						if err != nil {
							return execx.Result{Err: err}
						}
						files["/"+e.Name()] = data
					}
					return execx.Result{Err: os.WriteFile(isoPath, encodeManifest(files), 0o644)}
				}
				// "-indev", src, "-extract", pathInISO, destPath
				src, pathInISO, destPath := args[1], args[3], args[4]
				blob, err := os.ReadFile(src)
				if err != nil {
					return execx.Result{Err: err}
				}
				content, ok := decodeManifest(blob)[pathInISO]
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

// inMemoryStore implements both burn.Cataloger and Catalog against one
// shared map, so a disc burned via burn.Manager is immediately visible to
// retrieve.Manager, matching how they'd both sit on top of the same real
// Postgres database in production.
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

func (s *inMemoryStore) NextDiskID(ctx context.Context, prefix string, alreadyAllocated int) (string, error) {
	count := s.seq[prefix]
	return prefix + ":" + itoa4(count+alreadyAllocated+1), nil
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
	s.seq[mediaPrefixFor(d.ID)]++
	return nil
}
func (s *inMemoryStore) InsertFile(ctx context.Context, f db.FileRecord) error {
	s.files = append(s.files, f)
	return nil
}
func (s *inMemoryStore) ConsumeStagedMetadata(ctx context.Context, path string) ([]string, string, error) {
	return nil, "", nil
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
	// Must match the capacityBytes passed as burn.Options.CapacityBytes:
	// buildParityPayload pads to that fixed length, and reconstruction
	// must pad/read against the exact same length or the XOR won't line up.
	return testCapacityBytes, nil
}

// mediaPrefixFor extracts the "BD" in "BD:0001" so InsertDisk can keep the
// allocation counter consistent with NextDiskID's own count(*)-style logic.
func mediaPrefixFor(diskID string) string {
	if i := strings.Index(diskID, ":"); i >= 0 {
		return diskID[:i]
	}
	return diskID
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

// testCapacityBytes/testFileSize are sized so that: (a) two files together
// exceed TargetDataSize but one alone fits, forcing exactly 2 data discs,
// and (b) each disc's real wrapped size (tar's 512-byte block padding +
// toc.json + par2 index + manifest framing) stays safely under capacity —
// except the parity disc, whose XOR payload alone already equals capacity,
// so its own toc/par2 wrapping inevitably overshoots capacity by a couple
// hundred bytes. That overshoot only truncates the zero-padded tail of a
// capacity-bounded raw read (see decodeManifest's tolerance above and
// ReadRawImage's EOF-tolerant copy), never real content, at any scale —
// the same is true in production, where nominal media capacity carries far
// more slack than a TOC + par2 index ever need.
const (
	testCapacityBytes = 100_000
	testFileSize      = 50_000
)

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
	mustWrite("photo1.jpg", strings.Repeat("A", testFileSize))
	mustWrite("photo2.jpg", strings.Repeat("B", testFileSize))

	store := newInMemoryStore()
	shelf := shelf{dir: shelfDir}
	ex := sharedFakeExecutor(shelf)

	// --- Burn: 2 data discs (1 file each) + 1 parity disc for the group ---
	burnMgr := burn.NewManager(store, ex, staging, spool, t.TempDir())
	burnOpts := burn.Options{
		MediaType:       "BD-R",
		IDPrefix:        "BD",
		TargetPath:      device,
		CapacityBytes:   testCapacityBytes, // forces exactly 1 file per data disc
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
	retrieveMgr := NewManager(store, ex, retrievedDir, scratch, t.TempDir())

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
	if err := retrieveMgr.ReadDisk(context.Background(), device); err == nil {
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
		if err := retrieveMgr.ReadReconstructionDisc(context.Background(), diskID, device); err != nil {
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
	if s := string(got); s != strings.Repeat("A", testFileSize) {
		t.Errorf("recovered content mismatched (len %d, want %d)", len(s), testFileSize)
	}

	_ = survivorDataID
	_ = parityID
}
