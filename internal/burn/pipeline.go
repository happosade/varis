package burn

import (
	"context"
	"fmt"
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
	if plan.Role != "data" {
		return fmt.Errorf("runDisc: role %q not supported (cross-disc parity discs are burned via plan 02's extension)", plan.Role)
	}

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
