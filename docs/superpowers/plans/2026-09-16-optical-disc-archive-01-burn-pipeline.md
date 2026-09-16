# Optical Disc Archive — 01: Burn Pipeline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the single-disc burn state machine (PLANNING → PACKING → PARITY → ISO → BURNING → VERIFYING → DONE) as a library, fully testable without real hardware or a live Postgres.

**Architecture:** A `burn.Manager` holds at most one `Job` (single-drive reality), driven step by step by a `Cataloger` interface (persists disc/file rows — real impl wraps Postgres, tests use an in-memory fake) and an `execx.Executor` (runs `par2create`/`xorriso`/`wodim` — tests use `execx.FakeExecutor`). Cross-disc parity (spec §5) is deliberately **not** handled here — plan 02 extends this package's planning and disc-role handling. This plan only produces "data" role discs.

**Tech Stack:** Go 1.26+ stdlib (`archive/tar`, `compress/gzip`, `crypto/sha256`, `syscall` for free-space checks), the `execx` and `db` packages from plan 00.

**Depends on:** `docs/superpowers/plans/2026-09-16-optical-disc-archive-00-overview.md` (uses `execx.Executor`/`FakeExecutor`, `db.Disk`, `db.FileRecord`, `db.NextDiskID`, `db.InsertDisk`, `db.InsertFile`).

---

## Package Layout (this plan creates)

```
internal/toc/toc.go
internal/binpack/binpack.go
internal/burn/
  pack.go
  parity.go
  iso.go
  write.go
  verify.go
  pipeline.go
```

---

### Task 1: TOC (disk header) format

**Files:**
- Create: `internal/toc/toc.go`
- Test: `internal/toc/toc_test.go`

- [ ] **Step 1: Write the failing test**

```go
package toc

import (
	"testing"
	"time"
)

func TestMarshalAndParse_RoundTrip(t *testing.T) {
	orig := TOC{
		DiskID:        "BD:0001",
		MediaType:     "BD-R",
		ParityPercent: 10,
		Role:          "data",
		CreatedAt:     time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC),
		Files: []FileEntry{
			{Path: "photos/a.jpg", SizeBytes: 1024, SHA256: "abc123"},
		},
	}

	data, err := orig.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.DiskID != orig.DiskID || len(got.Files) != 1 || got.Files[0].Path != "photos/a.jpg" {
		t.Errorf("Parse round-trip = %+v, want match of %+v", got, orig)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/toc/... -v`
Expected: FAIL — package doesn't exist yet

- [ ] **Step 3: Implement toc.go**

```go
package toc

import (
	"encoding/json"
	"time"
)

type FileEntry struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256,omitempty"`
}

// TOC is the on-disc header written alongside every burned disc's payload,
// mirroring an LTO-FS style table of contents.
type TOC struct {
	DiskID        string      `json:"disk_id"`
	MediaType     string      `json:"media_type"`
	ParityPercent int         `json:"parity_percent"`
	GroupID       string      `json:"group_id,omitempty"`
	Role          string      `json:"role"`
	SlotIndex     int         `json:"slot_index,omitempty"`
	CreatedAt     time.Time   `json:"created_at"`
	Files         []FileEntry `json:"files"`
}

func (t TOC) Marshal() ([]byte, error) {
	return json.MarshalIndent(t, "", "  ")
}

func Parse(data []byte) (TOC, error) {
	var t TOC
	err := json.Unmarshal(data, &t)
	return t, err
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/toc/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/toc
git commit -m "Add TOC (disk header) format"
```

---

### Task 2: Bin-packing

**Files:**
- Create: `internal/binpack/binpack.go`
- Test: `internal/binpack/binpack_test.go`

- [ ] **Step 1: Write the failing test**

```go
package binpack

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTargetDataSize(t *testing.T) {
	got := TargetDataSize(25_025_314_816, 10)
	want := int64(25_025_314_816 * 100 / 110)
	if got != want {
		t.Errorf("TargetDataSize = %d, want %d", got, want)
	}
}

func TestScanStaging(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("1234"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "b.txt"), []byte("12345678"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := ScanStaging(root)
	if err != nil {
		t.Fatalf("ScanStaging: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("len(files) = %d, want 2", len(files))
	}
	if files[0].Path != "a.txt" || files[0].Size != 4 {
		t.Errorf("files[0] = %+v", files[0])
	}
	if files[1].Path != filepath.Join("sub", "b.txt") || files[1].Size != 8 {
		t.Errorf("files[1] = %+v", files[1])
	}
}

func TestPack_SplitsIntoTargetSizedBuckets(t *testing.T) {
	files := []FileInfo{
		{Path: "a", Size: 30},
		{Path: "b", Size: 30},
		{Path: "c", Size: 30},
	}

	buckets := Pack(files, 70)

	if len(buckets) != 2 {
		t.Fatalf("len(buckets) = %d, want 2", len(buckets))
	}
	if len(buckets[0].Files) != 2 || buckets[0].TotalSize != 60 {
		t.Errorf("buckets[0] = %+v, want 2 files totaling 60", buckets[0])
	}
	if len(buckets[1].Files) != 1 || buckets[1].TotalSize != 30 {
		t.Errorf("buckets[1] = %+v, want 1 file totaling 30", buckets[1])
	}
}

func TestPack_OversizedFileGetsOwnBucket(t *testing.T) {
	files := []FileInfo{
		{Path: "small", Size: 10},
		{Path: "huge", Size: 100}, // exceeds targetSize
		{Path: "small2", Size: 10},
	}

	buckets := Pack(files, 70)

	if len(buckets) != 3 {
		t.Fatalf("len(buckets) = %d, want 3", len(buckets))
	}
	if len(buckets[1].Files) != 1 || buckets[1].Files[0].Path != "huge" || buckets[1].TotalSize != 100 {
		t.Errorf("buckets[1] = %+v, want a lone 'huge' file totaling 100", buckets[1])
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/binpack/... -v`
Expected: FAIL — package doesn't exist yet

- [ ] **Step 3: Implement binpack.go**

```go
package binpack

import (
	"io/fs"
	"path/filepath"
	"sort"
)

type FileInfo struct {
	Path string // relative to the staging root
	Size int64
}

type Bucket struct {
	Files     []FileInfo
	TotalSize int64
}

// TargetDataSize returns the data budget for one disc so that
// data + par2(data, parityPercent) ~= capacity.
func TargetDataSize(capacity int64, parityPercent int) int64 {
	return capacity * 100 / int64(100+parityPercent)
}

// ScanStaging walks root and returns every regular file, relative to root,
// in deterministic (sorted) order.
func ScanStaging(root string) ([]FileInfo, error) {
	var files []FileInfo
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			files = append(files, FileInfo{Path: rel, Size: info.Size()})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// Pack greedily buckets files so each bucket's TotalSize stays at or under
// targetSize. A single file larger than targetSize gets its own oversized
// bucket — it still gets burned, it just won't share a disc with anything.
func Pack(files []FileInfo, targetSize int64) []Bucket {
	var buckets []Bucket
	var current Bucket
	for _, f := range files {
		if current.TotalSize > 0 && current.TotalSize+f.Size > targetSize {
			buckets = append(buckets, current)
			current = Bucket{}
		}
		current.Files = append(current.Files, f)
		current.TotalSize += f.Size
	}
	if len(current.Files) > 0 {
		buckets = append(buckets, current)
	}
	return buckets
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/binpack/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/binpack
git commit -m "Add bin-packing for disc-sized file buckets"
```

---

### Task 3: Packing (tar + optional gzip)

**Files:**
- Create: `internal/burn/pack.go`
- Test: `internal/burn/pack_test.go`

- [ ] **Step 1: Write the failing test**

```go
package burn

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"

	"varis/internal/binpack"
)

func TestWriteTar_Uncompressed(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	bucket := binpack.Bucket{Files: []binpack.FileInfo{{Path: "a.txt", Size: 5}}, TotalSize: 5}

	dest := filepath.Join(t.TempDir(), "out.tar")
	if err := WriteTar(root, bucket, dest, false); err != nil {
		t.Fatalf("WriteTar: %v", err)
	}

	f, err := os.Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	hdr, err := tr.Next()
	if err != nil {
		t.Fatalf("tar.Next: %v", err)
	}
	if hdr.Name != "a.txt" {
		t.Errorf("hdr.Name = %q, want a.txt", hdr.Name)
	}
	content, _ := io.ReadAll(tr)
	if string(content) != "hello" {
		t.Errorf("content = %q, want hello", content)
	}
}

func TestWriteTar_Compressed(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	bucket := binpack.Bucket{Files: []binpack.FileInfo{{Path: "a.txt", Size: 5}}, TotalSize: 5}

	dest := filepath.Join(t.TempDir(), "out.tar.gz")
	if err := WriteTar(root, bucket, dest, true); err != nil {
		t.Fatalf("WriteTar: %v", err)
	}

	f, err := os.Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	tr := tar.NewReader(gz)
	hdr, err := tr.Next()
	if err != nil {
		t.Fatalf("tar.Next: %v", err)
	}
	if hdr.Name != "a.txt" {
		t.Errorf("hdr.Name = %q, want a.txt", hdr.Name)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/burn/... -run TestWriteTar -v`
Expected: FAIL — package doesn't exist yet

- [ ] **Step 3: Implement pack.go**

```go
package burn

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"

	"varis/internal/binpack"
)

// WriteTar writes bucket's files (read from stagingRoot) into a tar archive
// at destPath, gzipped if compress is true.
func WriteTar(stagingRoot string, bucket binpack.Bucket, destPath string, compress bool) error {
	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	var w io.Writer = out
	var gz *gzip.Writer
	if compress {
		gz = gzip.NewWriter(out)
		w = gz
	}

	tw := tar.NewWriter(w)
	for _, f := range bucket.Files {
		if err := addFileToTar(tw, stagingRoot, f); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if gz != nil {
		return gz.Close()
	}
	return nil
}

func addFileToTar(tw *tar.Writer, root string, f binpack.FileInfo) error {
	fullPath := filepath.Join(root, f.Path)
	info, err := os.Stat(fullPath)
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = f.Path
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	in, err := os.Open(fullPath)
	if err != nil {
		return err
	}
	defer in.Close()
	_, err = io.Copy(tw, in)
	return err
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/burn/... -run TestWriteTar -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/burn/pack.go internal/burn/pack_test.go
git commit -m "Add tar/gzip packing for burn buckets"
```

---

### Task 4: Parity, ISO, burn, and verify wrappers

These are thin `os/exec` wrappers around `par2create`/`par2verify`, `xorriso`,
and `wodim`, tested by asserting the right command/args reach `execx.Executor`.

**Files:**
- Create: `internal/burn/parity.go`
- Create: `internal/burn/iso.go`
- Create: `internal/burn/write.go`
- Create: `internal/burn/verify.go`
- Test: `internal/burn/parity_test.go`
- Test: `internal/burn/verify_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// internal/burn/parity_test.go
package burn

import (
	"context"
	"testing"

	"varis/internal/execx"
)

func TestCreateParity_InvokesPar2CreateWithPercent(t *testing.T) {
	fake := &execx.FakeExecutor{}
	if err := CreateParity(context.Background(), fake, "/spool/BD0001.tar", 15); err != nil {
		t.Fatalf("CreateParity: %v", err)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Name != "par2create" {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[0].Args[0] != "-r15" || calls[0].Args[1] != "/spool/BD0001.tar" {
		t.Errorf("args = %v", calls[0].Args)
	}
}

func TestBuildISO_InvokesXorriso(t *testing.T) {
	fake := &execx.FakeExecutor{}
	if err := BuildISO(context.Background(), fake, "/spool/BD0001-src", "/spool/BD0001.iso"); err != nil {
		t.Fatalf("BuildISO: %v", err)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Name != "xorriso" {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestBurnISO_InvokesWodimWithDevice(t *testing.T) {
	fake := &execx.FakeExecutor{}
	if err := BurnISO(context.Background(), fake, "/dev/sr0", "/spool/BD0001.iso"); err != nil {
		t.Fatalf("BurnISO: %v", err)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Name != "wodim" {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[0].Args[0] != "dev=/dev/sr0" {
		t.Errorf("args = %v", calls[0].Args)
	}
}
```

```go
// internal/burn/verify_test.go
package burn

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"varis/internal/execx"
)

func TestHashFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash, err := HashFile(path)
	if err != nil {
		t.Fatalf("HashFile: %v", err)
	}
	// sha256("hello")
	want := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if hash != want {
		t.Errorf("HashFile = %s, want %s", hash, want)
	}
}

func TestVerifyBurn_MismatchedHashFails(t *testing.T) {
	devicePath := filepath.Join(t.TempDir(), "device")
	if err := os.WriteFile(devicePath, []byte("different content"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &execx.FakeExecutor{}
	err := VerifyBurn(context.Background(), fake, devicePath, "0000000000000000000000000000000000000000000000000000000000000000", "/spool/anything")
	if err == nil {
		t.Fatal("expected hash mismatch error")
	}
}

func TestVerifyBurn_MatchingHashRunsPar2Verify(t *testing.T) {
	devicePath := filepath.Join(t.TempDir(), "device")
	if err := os.WriteFile(devicePath, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &execx.FakeExecutor{}
	err := VerifyBurn(context.Background(), fake, devicePath, "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824", "/spool/anything")
	if err != nil {
		t.Fatalf("VerifyBurn: %v", err)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Name != "par2verify" {
		t.Fatalf("calls = %+v", calls)
	}
}
```

Note: the sha256 constant above is intentionally wrong-length filler for the
mismatch test (any string that isn't the real hash works); only the matching
test's hash must be the real `sha256("hello")`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/burn/... -run 'TestCreateParity|TestBuildISO|TestBurnISO|TestHashFile|TestVerifyBurn' -v`
Expected: FAIL — functions don't exist yet

- [ ] **Step 3: Implement parity.go**

```go
package burn

import (
	"context"
	"fmt"

	"varis/internal/execx"
)

// CreateParity runs par2create against dataPath with the given redundancy
// percentage, producing dataPath + ".par2" (and volume files) alongside it.
func CreateParity(ctx context.Context, ex execx.Executor, dataPath string, parityPercent int) error {
	_, err := ex.Run(ctx, "par2create", fmt.Sprintf("-r%d", parityPercent), dataPath)
	return err
}

// VerifyParity runs par2verify against dataPath's .par2 index, repairing
// minor corruption in place where possible.
func VerifyParity(ctx context.Context, ex execx.Executor, dataPath string) error {
	_, err := ex.Run(ctx, "par2verify", dataPath+".par2")
	return err
}
```

- [ ] **Step 4: Implement iso.go**

```go
package burn

import (
	"context"

	"varis/internal/execx"
)

// BuildISO wraps everything under sourceDir into an ISO 9660 image at isoPath.
func BuildISO(ctx context.Context, ex execx.Executor, sourceDir, isoPath string) error {
	_, err := ex.Run(ctx, "xorriso", "-as", "mkisofs", "-o", isoPath, "-r", "-J", sourceDir)
	return err
}
```

- [ ] **Step 5: Implement write.go**

```go
package burn

import (
	"context"

	"varis/internal/execx"
)

// BurnISO writes isoPath to the optical device using wodim.
func BurnISO(ctx context.Context, ex execx.Executor, device, isoPath string) error {
	_, err := ex.Run(ctx, "wodim", "dev="+device, isoPath)
	return err
}
```

- [ ] **Step 6: Implement verify.go**

```go
package burn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"varis/internal/execx"
)

// HashFile returns the hex-encoded SHA256 of the file at path.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// VerifyBurn re-reads the device and confirms its hash matches the ISO that
// was burned, then runs par2verify against the parity-protected payload as
// a finer-grained corruption check than a gross hash mismatch would give.
func VerifyBurn(ctx context.Context, ex execx.Executor, device, isoHash, dataPathOnDisc string) error {
	readBack, err := HashFile(device)
	if err != nil {
		return fmt.Errorf("reading back device: %w", err)
	}
	if readBack != isoHash {
		return fmt.Errorf("burned disc hash %s does not match ISO hash %s", readBack, isoHash)
	}
	return VerifyParity(ctx, ex, dataPathOnDisc)
}
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/burn/... -v`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/burn/parity.go internal/burn/iso.go internal/burn/write.go internal/burn/verify.go internal/burn/parity_test.go internal/burn/verify_test.go
git commit -m "Add parity/ISO/burn/verify command wrappers"
```

---

### Task 5: Extend FakeExecutor with side-effect hooks

Real `par2create`/`xorriso`/`wodim` calls produce files as a side effect
(`.par2` files, the `.iso` file, bytes written to the device). `FakeExecutor`
as built in plan 00 only returns configured output/error — it doesn't touch
the filesystem. The pipeline test in Task 6 needs to simulate those side
effects, so `FakeExecutor` needs an optional per-command hook.

**Files:**
- Modify: `internal/execx/fake.go`
- Test: `internal/execx/fake_test.go`

- [ ] **Step 1: Write the failing test**

```go
// add to internal/execx/fake_test.go
func TestFakeExecutor_FuncOverridesResults(t *testing.T) {
	fake := &FakeExecutor{
		Results: map[string]Result{"xorriso": {Output: []byte("ignored")}},
		Funcs: map[string]func(args []string) Result{
			"xorriso": func(args []string) Result {
				return Result{Output: []byte("from func")}
			},
		},
	}
	out, err := fake.Run(context.Background(), "xorriso", "-as", "mkisofs")
	if err != nil || string(out) != "from func" {
		t.Fatalf("Run = %q, %v", out, err)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/execx/... -run TestFakeExecutor_FuncOverridesResults -v`
Expected: FAIL — `Funcs` field doesn't exist yet

- [ ] **Step 3: Update fake.go**

```go
package execx

import (
	"context"
	"sync"
)

type Call struct {
	Name string
	Args []string
}

type Result struct {
	Output []byte
	Err    error
}

// FakeExecutor records every call and returns a configured Result: from
// Funcs (for tests that need to simulate a command's filesystem side
// effects, like xorriso writing an ISO file) if present for that command
// name, otherwise from Results (zero value: nil output, nil error).
type FakeExecutor struct {
	Results map[string]Result
	Funcs   map[string]func(args []string) Result

	mu    sync.Mutex
	calls []Call
}

func (f *FakeExecutor) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, Call{Name: name, Args: args})
	f.mu.Unlock()

	if fn, ok := f.Funcs[name]; ok {
		r := fn(args)
		return r.Output, r.Err
	}
	r := f.Results[name]
	return r.Output, r.Err
}

func (f *FakeExecutor) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/execx/... -v`
Expected: PASS (all execx tests, including the earlier ones from plan 00)

- [ ] **Step 5: Commit**

```bash
git add internal/execx
git commit -m "Add side-effect hooks to FakeExecutor for pipeline testing"
```

---

### Task 6: The burn state machine (Manager, Job, Cataloger)

**Files:**
- Create: `internal/burn/pipeline.go`
- Test: `internal/burn/pipeline_test.go`

- [ ] **Step 1: Write pipeline.go**

```go
package burn

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"varis/internal/binpack"
	"varis/internal/db"
	"varis/internal/execx"
	"varis/internal/toc"
)

type State string

const (
	StateAwaitingDisc State = "AWAITING_DISC"
	StatePacking      State = "PACKING"
	StateParity       State = "PARITY"
	StateISO          State = "ISO"
	StateBurning      State = "BURNING"
	StateVerifying    State = "VERIFYING"
	StateDone         State = "DONE"
	StateFailed       State = "FAILED"
)

// Options configures one burn job — the exact inputs exposed on the
// dashboard (spec §4/§5). CrossDiscParity/GroupSize are read by plan 02's
// extension of planJob; this plan's planJob ignores them and always
// produces "data" role discs.
type Options struct {
	MediaType       string
	CapacityBytes   int64
	ParityPercent   int
	Compress        bool
	CrossDiscParity bool
	GroupSize       int
}

// DiscPlan is one disc's worth of work.
type DiscPlan struct {
	DiskID    string
	Role      string // "data" | "parity"
	GroupID   string
	SlotIndex int
	Bucket    binpack.Bucket // empty for parity discs (plan 02)
}

// Job tracks one burn run through the state machine, one disc at a time.
type Job struct {
	Options      Options
	Plans        []DiscPlan
	CurrentIndex int
	State        State
	Err          error
}

// Cataloger persists burned discs and their files. Production code uses
// NewDBCataloger (backed by Postgres); tests use an in-memory fake so the
// state machine can be tested without a live database.
type Cataloger interface {
	NextDiskID(ctx context.Context, prefix string) (string, error)
	InsertDisk(ctx context.Context, d db.Disk) error
	InsertFile(ctx context.Context, f db.FileRecord) error
}

type dbCataloger struct{ pool interface {
	Query(ctx context.Context, sql string, args ...any) (interface{ Close() }, error)
} }

// NewDBCataloger adapts *pgxpool.Pool (via the db package's functions) to
// the Cataloger interface used by Manager.
func NewDBCataloger(pool *dbPool) Cataloger { return realCataloger{pool: pool} }

type dbPool = pgxPoolType

type realCataloger struct{ pool *dbPool }

func (c realCataloger) NextDiskID(ctx context.Context, prefix string) (string, error) {
	return db.NextDiskID(ctx, c.pool, prefix)
}
func (c realCataloger) InsertDisk(ctx context.Context, d db.Disk) error {
	return db.InsertDisk(ctx, c.pool, d)
}
func (c realCataloger) InsertFile(ctx context.Context, f db.FileRecord) error {
	return db.InsertFile(ctx, c.pool, f)
}

// Manager runs at most one Job at a time, matching the single-drive reality.
type Manager struct {
	cat        Cataloger
	ex         execx.Executor
	stagingDir string
	spoolDir   string
	device     string
	freeSpace  func(path string) (uint64, error)

	mu  sync.Mutex
	job *Job
}

func NewManager(cat Cataloger, ex execx.Executor, stagingDir, spoolDir, device string) *Manager {
	return &Manager{
		cat:        cat,
		ex:         ex,
		stagingDir: stagingDir,
		spoolDir:   spoolDir,
		device:     device,
		freeSpace:  diskFreeBytes,
	}
}

// Current returns the in-flight (or just-finished/failed) job, or nil if
// none has been started yet.
func (m *Manager) Current() *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.job
}

func diskFreeBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}

func mediaPrefix(mediaType string) string {
	switch mediaType {
	case "BD-R":
		return "BD"
	case "BD-R DL":
		return "BDDL"
	default:
		return "DISC"
	}
}

// planJob scans staging and greedily buckets files into disc-sized plans.
// Plan 02 wraps this to additionally assign group/slot info and append
// parity-role DiscPlans.
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
	return plans, nil
}

// Start plans a new job. It fails if a job is already in progress, if
// there isn't enough free space in spoolDir for the working set, or if
// staging is empty.
func (m *Manager) Start(ctx context.Context, opts Options) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job != nil && m.job.State != StateDone && m.job.State != StateFailed {
		return fmt.Errorf("a burn job is already in progress (state %s)", m.job.State)
	}

	free, err := m.freeSpace(m.spoolDir)
	if err != nil {
		return fmt.Errorf("checking free space: %w", err)
	}
	if free < uint64(opts.CapacityBytes)*2 {
		return fmt.Errorf("not enough free space in spool dir: need ~%d bytes, have %d", opts.CapacityBytes*2, free)
	}

	plans, err := planJob(ctx, m.stagingDir, opts, m.cat)
	if err != nil {
		return fmt.Errorf("planning: %w", err)
	}
	if len(plans) == 0 {
		return fmt.Errorf("no files staged to burn")
	}

	m.job = &Job{Options: opts, Plans: plans, CurrentIndex: 0, State: StateAwaitingDisc}
	return nil
}

// ContinueNextDisc burns the current disc plan after the caller confirms a
// blank disc has been inserted. On success it advances to the next disc
// (or DONE); on failure it sets StateFailed without touching staged files.
func (m *Manager) ContinueNextDisc(ctx context.Context) error {
	m.mu.Lock()
	job := m.job
	m.mu.Unlock()
	if job == nil || job.State != StateAwaitingDisc {
		return fmt.Errorf("no disc awaiting burn")
	}

	plan := job.Plans[job.CurrentIndex]
	if err := m.runDisc(ctx, job, plan); err != nil {
		m.mu.Lock()
		job.State = StateFailed
		job.Err = err
		m.mu.Unlock()
		return err
	}

	m.mu.Lock()
	job.CurrentIndex++
	if job.CurrentIndex >= len(job.Plans) {
		job.State = StateDone
	} else {
		job.State = StateAwaitingDisc
	}
	m.mu.Unlock()
	return nil
}

// Retry re-attempts the current disc after a FAILED step (e.g. a bad blank
// disc), without re-planning or touching staged files.
func (m *Manager) Retry(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil || m.job.State != StateFailed {
		return fmt.Errorf("no failed job to retry")
	}
	m.job.State = StateAwaitingDisc
	m.job.Err = nil
	return nil
}

func (m *Manager) setState(job *Job, s State) {
	m.mu.Lock()
	job.State = s
	m.mu.Unlock()
}

func (m *Manager) runDisc(ctx context.Context, job *Job, plan DiscPlan) error {
	if plan.Role != "data" {
		return fmt.Errorf("runDisc: role %q not supported (cross-disc parity discs are burned via plan 02's extension)", plan.Role)
	}

	m.setState(job, StatePacking)
	tarPath := filepath.Join(m.spoolDir, plan.DiskID+".tar")
	if err := WriteTar(m.stagingDir, plan.Bucket, tarPath, job.Options.Compress); err != nil {
		return fmt.Errorf("packing: %w", err)
	}

	m.setState(job, StateParity)
	if err := CreateParity(ctx, m.ex, tarPath, job.Options.ParityPercent); err != nil {
		return fmt.Errorf("parity: %w", err)
	}

	t := toc.TOC{
		DiskID:        plan.DiskID,
		MediaType:     job.Options.MediaType,
		ParityPercent: job.Options.ParityPercent,
		Role:          plan.Role,
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

// movePathsInto moves any of paths that exist into destDir, skipping any
// that don't (e.g. a .par2 volume file par2create didn't need to produce).
func movePathsInto(destDir string, paths ...string) error {
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if err := os.Rename(p, filepath.Join(destDir, filepath.Base(p))); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) commitDisc(ctx context.Context, job *Job, plan DiscPlan, isoHash string) error {
	err := m.cat.InsertDisk(ctx, db.Disk{
		ID:            plan.DiskID,
		MediaType:     job.Options.MediaType,
		ParityPercent: job.Options.ParityPercent,
		Role:          plan.Role,
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
		// The file is now archived and protected on disc — remove it from
		// staging so the drop zone only ever shows what's not yet burned.
		if err := os.Remove(originalPath); err != nil {
			return fmt.Errorf("removing archived file from staging: %w", err)
		}
	}
	return nil
}
```

The `dbCataloger`/`dbPool`/`pgxPoolType` indirection above is a placeholder
that won't compile — replace it with a direct, real adapter in the next step
instead (this keeps the plan's main listing focused on the state machine,
and the real adapter needs the actual pgx import):

- [ ] **Step 2: Replace the Cataloger adapter with a real one**

Delete the `dbCataloger`, `NewDBCataloger`, `dbPool`, and `realCataloger`
block from Step 1's listing and replace it with:

```go
type pgxCataloger struct{ pool *pgxpool.Pool }

// NewDBCataloger adapts a *pgxpool.Pool to the Cataloger interface.
func NewDBCataloger(pool *pgxpool.Pool) Cataloger { return pgxCataloger{pool: pool} }

func (c pgxCataloger) NextDiskID(ctx context.Context, prefix string) (string, error) {
	return db.NextDiskID(ctx, c.pool, prefix)
}
func (c pgxCataloger) InsertDisk(ctx context.Context, d db.Disk) error {
	return db.InsertDisk(ctx, c.pool, d)
}
func (c pgxCataloger) InsertFile(ctx context.Context, f db.FileRecord) error {
	return db.InsertFile(ctx, c.pool, f)
}
```

Add `"github.com/jackc/pgx/v5/pgxpool"` to pipeline.go's imports.

- [ ] **Step 3: Build to catch mistakes**

Run: `go build ./...`
Expected: compiles cleanly once Step 2's replacement is in place.

- [ ] **Step 4: Write pipeline_test.go**

```go
package burn

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"varis/internal/execx"
)

// FakeCataloger is an in-memory Cataloger for tests.
type FakeCataloger struct {
	mu      sync.Mutex
	seq     map[string]int
	Disks   []db.Disk
	Files   []db.FileRecord
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
				// args[len-1] is the data file; touch a .par2 next to it.
				dataPath := args[len(args)-1]
				os.WriteFile(dataPath+".par2", []byte("par2-index"), 0o644)
				return execx.Result{}
			},
			"xorriso": func(args []string) execx.Result {
				// "-o", isoPath, ... is the contract from BuildISO.
				for i, a := range args {
					if a == "-o" && i+1 < len(args) {
						os.WriteFile(args[i+1], []byte("iso-bytes"), 0o644)
					}
				}
				return execx.Result{}
			},
			"wodim": func(args []string) execx.Result {
				// args = ["dev="+device, isoPath]; copy the ISO's bytes to
				// devicePath so VerifyBurn's hash check matches.
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
```

Add `"varis/internal/db"` to pipeline_test.go's imports (used by `FakeCataloger`).

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/burn/... -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/burn/pipeline.go internal/burn/pipeline_test.go
git commit -m "Add burn state machine: Manager, Job, Cataloger, planJob"
```

---

## Plan Self-Review

**Spec coverage:** Burn pipeline states, parity%, compression toggle,
sequential multi-disc handling (§4), free-space check, single global job
lock, FAILED-state + retry-without-reburn-from-scratch, staged files
untouched until DONE (§7) — all covered. Cross-disc parity (§5) is
explicitly deferred to plan 02, and note that `Manager.freeSpace` is
exported as a struct field (not a constructor param) specifically so tests
can override it — this is used directly in the tests above.

**Placeholder scan:** the one intentionally-broken snippet in Task 6 Step 1
is flagged and immediately corrected in Step 2 — this is deliberate
scaffolding to keep the state-machine listing readable before introducing
the pgx import, not a left-over placeholder. No other TBD/TODO remain.

**Type consistency:** `Options.CapacityBytes` (not a media-type string
lookup) is the single source of truth `planJob` and `Start` both use —
plan 03's HTTP handler is responsible for resolving `MediaType` to
`CapacityBytes` via `db.ListMediaTypes` before calling `Manager.Start`.
`DiscPlan.Role`/`GroupID`/`SlotIndex` are already present on the struct
(unused by this plan's `planJob`) specifically so plan 02 can populate them
without changing the struct shape.
