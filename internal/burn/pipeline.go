package burn

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"varis/internal/binpack"
	"varis/internal/db"
	"varis/internal/execx"
	"varis/internal/toc"
	"varis/internal/xordisk"
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
// dashboard (spec §4/§5). When CrossDiscParity is set, planJob groups data
// discs into GroupSize-sized groups and appends a "parity" role DiscPlan
// per group.
type Options struct {
	MediaType       string
	CapacityBytes   int64
	ParityPercent   int
	Compress        bool
	CrossDiscParity bool
	GroupSize       int
	DryRun          bool
	IDPrefix        string
	WriteKind       string // "optical" | "filesystem" — ignored when DryRun is set
	TargetPath      string // device path or filesystem target — ignored when DryRun is set
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
	NextDiskID(ctx context.Context, prefix string, alreadyAllocated int) (string, error)
	NewBurnJobID(ctx context.Context) (string, error)
	NextGroupID(ctx context.Context, burnJobID string, groupSize int) (string, error)
	InsertDisk(ctx context.Context, d db.Disk) error
	InsertFile(ctx context.Context, f db.FileRecord) error
	// ConsumeStagedMetadata reads AND DELETES path's staged metadata — a
	// path with no metadata returns (nil, "", nil), not an error. There is
	// no peek-without-consuming variant; see commitDisc's call site for
	// why that's an accepted, bounded tradeoff.
	ConsumeStagedMetadata(ctx context.Context, path string) (tags []string, description string, err error)
}

// Manager runs at most one Job at a time, matching the single-drive reality.
type Manager struct {
	cat        Cataloger
	ex         execx.Executor
	stagingDir string
	spoolDir   string
	dryRunDir  string
	freeSpace  func(path string) (uint64, error)

	mu  sync.Mutex
	job *Job
}

func NewManager(cat Cataloger, ex execx.Executor, stagingDir, spoolDir, dryRunDir string) *Manager {
	return &Manager{
		cat:        cat,
		ex:         ex,
		stagingDir: stagingDir,
		spoolDir:   spoolDir,
		dryRunDir:  dryRunDir,
		freeSpace:  diskFreeBytes,
	}
}

// Current returns a snapshot of the in-flight (or just-finished/failed)
// job, or nil if none has been started yet. It returns a value copy (not
// the live job) so callers never race with runDisc's concurrent state
// updates; sharing Plans' backing array across the copy is safe because
// nothing mutates Plans after Start creates it.
func (m *Manager) Current() *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil {
		return nil
	}
	jobCopy := *m.job
	return &jobCopy
}

func diskFreeBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}

// planJob scans staging and greedily buckets files into disc-sized plans.
// When opts.CrossDiscParity is set, data discs are assigned into groups of
// opts.GroupSize (a trailing short group still gets its own parity disc),
// and one "parity" role DiscPlan is appended per group — its Bucket is
// left empty; runDisc computes its bytes via XOR at burn time (see
// buildParityPayload in pipeline.go).
func planJob(ctx context.Context, stagingDir string, opts Options, cat Cataloger) ([]DiscPlan, error) {
	files, err := binpack.ScanStaging(stagingDir)
	if err != nil {
		return nil, err
	}
	target := binpack.TargetDataSize(opts.CapacityBytes, opts.ParityPercent)
	buckets := binpack.Pack(files, target)

	prefix := opts.IDPrefix
	allocated := map[string]int{}
	var plans []DiscPlan
	for _, b := range buckets {
		id, err := cat.NextDiskID(ctx, prefix, allocated[prefix])
		if err != nil {
			return nil, err
		}
		allocated[prefix]++
		plans = append(plans, DiscPlan{DiskID: id, Role: "data", Bucket: b})
	}

	if !opts.CrossDiscParity || len(plans) == 0 {
		return plans, nil
	}

	groupSize := opts.GroupSize
	if groupSize <= 1 {
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
		parityID, err := cat.NextDiskID(ctx, prefix, allocated[prefix])
		if err != nil {
			return nil, err
		}
		allocated[prefix]++
		withGroups = append(withGroups, DiscPlan{
			DiskID:    parityID,
			Role:      "parity",
			GroupID:   groupID,
			SlotIndex: len(plans[start:end]),
		})
	}
	return withGroups, nil
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
	if job == nil || job.State != StateAwaitingDisc {
		m.mu.Unlock()
		return fmt.Errorf("no disc awaiting burn")
	}
	// Claim the job atomically in the same critical section as the check
	// above, so two concurrent callers can't both observe AwaitingDisc and
	// both enter runDisc for the same disc plan.
	job.State = StatePacking
	m.mu.Unlock()

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
	tarPath := filepath.Join(m.spoolDir, plan.DiskID+".tar")
	tocPath := filepath.Join(m.spoolDir, plan.DiskID+".toc.json")
	isoPath := filepath.Join(m.spoolDir, plan.DiskID+".iso")
	isoDir := filepath.Join(m.spoolDir, plan.DiskID+"-src")
	if err := removeStaleArtifacts(tarPath, tarPath+".par2", tocPath, isoPath, isoDir); err != nil {
		return fmt.Errorf("clearing stale artifacts from a prior attempt: %w", err)
	}

	// Checked after clearing stale artifacts so a prior failed attempt's
	// leftovers (about to be reclaimed above) don't cause a spurious
	// "not enough free space" failure on retry.
	free, err := m.freeSpace(m.spoolDir)
	if err != nil {
		return fmt.Errorf("checking free space: %w", err)
	}
	if free < uint64(job.Options.CapacityBytes)*2 {
		return fmt.Errorf("not enough free space in spool dir: need ~%d bytes, have %d", job.Options.CapacityBytes*2, free)
	}

	m.setState(job, StatePacking)
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
		Compressed:    job.Options.Compress,
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
	if err := os.WriteFile(tocPath, tocBytes, 0o644); err != nil {
		return fmt.Errorf("writing toc: %w", err)
	}

	m.setState(job, StateISO)
	if err := os.MkdirAll(isoDir, 0o755); err != nil {
		return fmt.Errorf("iso staging dir: %w", err)
	}
	if err := movePathsInto(isoDir, tarPath, tocPath, tarPath+".par2"); err != nil {
		return fmt.Errorf("staging iso contents: %w", err)
	}
	if err := BuildISO(ctx, m.ex, isoDir, isoPath); err != nil {
		return fmt.Errorf("iso: %w", err)
	}
	isoHash, err := HashFile(isoPath)
	if err != nil {
		return fmt.Errorf("hashing iso: %w", err)
	}

	m.setState(job, StateBurning)
	switch {
	case job.Options.DryRun:
		if err := os.MkdirAll(m.dryRunDir, 0o755); err != nil {
			return fmt.Errorf("dry run dir: %w", err)
		}
		if err := copyFile(filepath.Join(m.dryRunDir, plan.DiskID+".iso"), isoPath); err != nil {
			return fmt.Errorf("dry run copy: %w", err)
		}
	case job.Options.WriteKind == "filesystem":
		if err := copyFile(job.Options.TargetPath, isoPath); err != nil {
			return fmt.Errorf("copy: %w", err)
		}
		m.setState(job, StateVerifying)
		if err := VerifyBurn(ctx, m.ex, job.Options.TargetPath, isoHash, filepath.Join(isoDir, filepath.Base(tarPath))); err != nil {
			return fmt.Errorf("verify: %w", err)
		}
		// Independently checkable without trusting the app: standard
		// sha256sum -c input, "<hash>  <filename>\n".
		checksumLine := isoHash + "  " + filepath.Base(job.Options.TargetPath) + "\n"
		if err := os.WriteFile(job.Options.TargetPath+".sha256", []byte(checksumLine), 0o644); err != nil {
			return fmt.Errorf("writing checksum file: %w", err)
		}
	default: // "optical"
		if err := BurnISO(ctx, m.ex, job.Options.TargetPath, isoPath); err != nil {
			return fmt.Errorf("burn: %w", err)
		}

		m.setState(job, StateVerifying)
		if err := VerifyBurn(ctx, m.ex, job.Options.TargetPath, isoHash, filepath.Join(isoDir, filepath.Base(tarPath))); err != nil {
			return fmt.Errorf("verify: %w", err)
		}
	}

	if err := m.commitDisc(ctx, job, plan, isoHash); err != nil {
		return fmt.Errorf("db commit: %w", err)
	}

	if plan.Role == "parity" {
		m.cleanupGroupDataISOs(job, plan)
	}
	return nil
}

// cleanupGroupDataISOs removes the spool ".iso" files for plan's group's
// data discs once that group's parity disc has been fully burned, verified,
// and committed. Those ISOs exist only so buildParityPayload can read them
// while the group's parity disc is being processed; once the parity disc
// is done, nothing else needs them, and leaving them behind would let spool
// usage grow with every completed group in a multi-group job. A failure
// here is logged rather than returned: the disc calling this has already
// succeeded (burned, verified, committed), so a stale spool file is not
// worth failing an otherwise-complete disc over.
func (m *Manager) cleanupGroupDataISOs(job *Job, plan DiscPlan) {
	for _, p := range job.Plans {
		if p.GroupID != plan.GroupID || p.Role != "data" {
			continue
		}
		isoPath := filepath.Join(m.spoolDir, p.DiskID+".iso")
		if err := os.Remove(isoPath); err != nil && !os.IsNotExist(err) {
			log.Printf("burn: cleaning up data disc image %s after group %s's parity disc committed: %v", isoPath, plan.GroupID, err)
		}
	}
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
// the missing disc's real size is exactly what's unknown, so the only
// length both sides can agree on without it is the fixed, known-in-advance
// media capacity. The cost is that a parity disc's payload is as large as
// a full disc's capacity even though the actual data on each member disc
// is normally much smaller (mostly zero padding) — an acceptable trade
// for correct reconstruction regardless of which member is lost.
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

// removeStaleArtifacts clears any spool files/dirs left over from a prior,
// partially-completed attempt at the same DiskID (e.g. a failure mid-PARITY
// leaves tarPath+".par2" behind before the ISO-stage move). Real par2create
// refuses to run against a data file that already has recovery files, so
// retries must start from a clean slate regardless of where the previous
// attempt failed.
func removeStaleArtifacts(paths ...string) error {
	for _, p := range paths {
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	return nil
}

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
		IsDryRun:      job.Options.DryRun,
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
		// Consumed before InsertFile, so a subsequent InsertFile failure
		// still loses this file's staged tags/description even though the
		// burn as a whole is safe to retry (the staging file itself is
		// untouched until InsertFile succeeds, per the removal below).
		// Ordering it the other way would require a peek-then-delete,
		// giving up ConsumeStagedMetadata's single-round-trip DELETE ...
		// RETURNING — accepted since the residual cost is re-entering one
		// file's metadata, not any catalog corruption.
		tags, description, err := m.cat.ConsumeStagedMetadata(ctx, f.Path)
		if err != nil {
			return err
		}
		if err := m.cat.InsertFile(ctx, db.FileRecord{
			DiskID:       plan.DiskID,
			OriginalPath: f.Path,
			SizeBytes:    f.Size,
			FileHash:     hash,
			Tags:         tags,
			Description:  description,
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

type pgxCataloger struct{ pool *pgxpool.Pool }

// NewDBCataloger adapts a *pgxpool.Pool to the Cataloger interface.
func NewDBCataloger(pool *pgxpool.Pool) Cataloger { return pgxCataloger{pool: pool} }

func (c pgxCataloger) NextDiskID(ctx context.Context, prefix string, alreadyAllocated int) (string, error) {
	return db.NextDiskID(ctx, c.pool, prefix, alreadyAllocated)
}
func (c pgxCataloger) NewBurnJobID(ctx context.Context) (string, error) {
	var id string
	err := c.pool.QueryRow(ctx, `SELECT gen_random_uuid()`).Scan(&id)
	return id, err
}
func (c pgxCataloger) NextGroupID(ctx context.Context, burnJobID string, groupSize int) (string, error) {
	return db.InsertDiskGroup(ctx, c.pool, burnJobID, groupSize)
}
func (c pgxCataloger) InsertDisk(ctx context.Context, d db.Disk) error {
	return db.InsertDisk(ctx, c.pool, d)
}
func (c pgxCataloger) InsertFile(ctx context.Context, f db.FileRecord) error {
	return db.InsertFile(ctx, c.pool, f)
}
func (c pgxCataloger) ConsumeStagedMetadata(ctx context.Context, path string) ([]string, string, error) {
	return db.ConsumeStagedMetadata(ctx, c.pool, path)
}
