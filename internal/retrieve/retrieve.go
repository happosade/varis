package retrieve

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"varis/internal/burn"
	"varis/internal/execx"
	"varis/internal/toc"
)

type State string

const (
	StateAwaitingMedia       State = "AWAITING_MEDIA"
	StateReadingDisc         State = "READING_DISC"
	StateDone                State = "DONE"
	StateFailed              State = "FAILED"
	StateNeedsReconstruction State = "NEEDS_RECONSTRUCTION"
	// StateStartingReconstruction is held only while StartReconstruction's
	// DB lookups and Reconstructor construction are in flight, claiming
	// the job so a concurrent caller can't also enter StartReconstruction.
	StateStartingReconstruction State = "STARTING_RECONSTRUCTION"
	StateReconstructing         State = "RECONSTRUCTING"
	// StateReadingReconstructionDisc is held only while ReadReconstructionDisc
	// is reading and folding one member disc into the Reconstructor,
	// claiming the job so a concurrent caller can't also enter it.
	StateReadingReconstructionDisc State = "READING_RECONSTRUCTION_DISC"
)

type Job struct {
	FileID       string
	DiskID       string
	OriginalPath string
	State        State
	Err          error
}

// Manager runs at most one retrieval at a time. Callers are responsible
// for not starting a retrieval while a burn.Manager job is in progress on
// the same drive (a later plan closes this gap once both managers exist
// for a handler to check against each other).
type Manager struct {
	cat          Catalog
	ex           execx.Executor
	retrievedDir string
	scratchDir   string
	device       string

	mu                     sync.Mutex
	job                    *Job
	reconstructor          *burn.Reconstructor
	reconstructionCapacity int64
}

func NewManager(cat Catalog, ex execx.Executor, retrievedDir, scratchDir, device string) *Manager {
	return &Manager{cat: cat, ex: ex, retrievedDir: retrievedDir, scratchDir: scratchDir, device: device}
}

// Current returns a snapshot of the in-flight (or just-finished/failed)
// job, or nil if none has been started yet. It returns a value copy (not
// the live job) so callers never race with readFrom/ReadReconstructionDisc's
// concurrent state updates.
func (m *Manager) Current() *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil {
		return nil
	}
	jobCopy := *m.job
	return &jobCopy
}

// Abort discards the current job (and any in-progress reconstruction),
// giving a caller an escape hatch when a retrieval or reconstruction is
// truly unrecoverable (e.g. too many group members are also damaged) —
// without it, a job stuck outside StateDone/StateFailed would permanently
// block any future Start call.
func (m *Manager) Abort() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.job = nil
	m.reconstructor = nil
	m.reconstructionCapacity = 0
}

func (m *Manager) Start(ctx context.Context, fileID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job != nil && m.job.State != StateDone && m.job.State != StateFailed {
		return fmt.Errorf("a retrieval is already in progress (state %s)", m.job.State)
	}
	f, err := m.cat.GetFile(ctx, fileID)
	if err != nil {
		return err
	}
	m.job = &Job{FileID: fileID, DiskID: f.DiskID, OriginalPath: f.OriginalPath, State: StateAwaitingMedia}
	return nil
}

// ReadDisk is called once the user has inserted the disc named by the
// current job and clicked "Read Disk".
func (m *Manager) ReadDisk(ctx context.Context) error {
	m.mu.Lock()
	job := m.job
	if job == nil || job.State != StateAwaitingMedia {
		m.mu.Unlock()
		return fmt.Errorf("no retrieval awaiting media")
	}
	// Claim the job atomically in the same critical section as the check
	// above, so two concurrent callers can't both observe AwaitingMedia and
	// both enter readFrom for the same job. readFrom's fail/needsReconstruction/
	// success paths always set a next state before returning, so this claim
	// alone is enough to prevent double-entry.
	job.State = StateReadingDisc
	m.mu.Unlock()
	return m.readFrom(ctx, job, m.device)
}

// readFrom does the actual TOC-check + parity-verify + extract, reading
// from src (a device path or, from ReadReconstructionDisc, a reconstructed
// .iso file — both work identically via ExtractFromDisc's -indev).
// readFrom routes every failure to one of two outcomes: a disc that's
// unreadable or fails verification means the *physical media* may be
// damaged — exactly the case cross-disc parity exists to survive — so
// those go to StateNeedsReconstruction, not a dead-end StateFailed.
// Two things are hard failures instead: (1) "you inserted the wrong
// disc", since reconstruction can't fix a user simply grabbing the wrong
// one off the shelf — the fix there is just inserting the right disc and
// calling ReadDisk again; and (2) the requested file isn't found inside a
// tar that has already passed parity verification — at that point the tar
// itself is proven intact, so a missing member means the file genuinely
// isn't in the archive (stale/bad metadata, or a real absence), not media
// damage. Reconstruction would only rebuild the same, still-missing tar,
// so it can't help here either.
func (m *Manager) readFrom(ctx context.Context, job *Job, src string) error {
	workDir, err := os.MkdirTemp(m.scratchDir, "retrieve-")
	if err != nil {
		return m.fail(job, err)
	}
	defer os.RemoveAll(workDir)

	tocPath := filepath.Join(workDir, job.DiskID+".toc.json")
	if err := burn.ExtractFromDisc(ctx, m.ex, src, "/"+job.DiskID+".toc.json", tocPath); err != nil {
		return m.needsReconstruction(job, fmt.Errorf("reading TOC: %w", err))
	}
	tocBytes, err := os.ReadFile(tocPath)
	if err != nil {
		return m.needsReconstruction(job, err)
	}
	t, err := toc.Parse(tocBytes)
	if err != nil {
		return m.needsReconstruction(job, err)
	}
	if t.DiskID != job.DiskID {
		return m.fail(job, fmt.Errorf("wrong disc: expected %s, found %s", job.DiskID, t.DiskID))
	}

	tarPath := filepath.Join(workDir, job.DiskID+".tar")
	if err := burn.ExtractFromDisc(ctx, m.ex, src, "/"+job.DiskID+".tar", tarPath); err != nil {
		return m.needsReconstruction(job, fmt.Errorf("reading data: %w", err))
	}
	if err := burn.ExtractFromDisc(ctx, m.ex, src, "/"+job.DiskID+".tar.par2", tarPath+".par2"); err != nil {
		return m.needsReconstruction(job, fmt.Errorf("reading parity index: %w", err))
	}

	if err := burn.VerifyParity(ctx, m.ex, tarPath); err != nil {
		return m.needsReconstruction(job, err)
	}

	destPath := filepath.Join(m.retrievedDir, filepath.Base(job.OriginalPath))
	if err := burn.ExtractFile(tarPath, t.Compressed, job.OriginalPath, destPath); err != nil {
		return m.fail(job, fmt.Errorf("extracting file: %w", err))
	}

	m.mu.Lock()
	job.State = StateDone
	m.mu.Unlock()
	return nil
}

func (m *Manager) fail(job *Job, err error) error {
	m.mu.Lock()
	job.State = StateFailed
	job.Err = err
	m.mu.Unlock()
	return err
}

func (m *Manager) needsReconstruction(job *Job, err error) error {
	m.mu.Lock()
	job.State = StateNeedsReconstruction
	job.Err = err
	m.mu.Unlock()
	return err
}

// StartReconstruction begins the "insert every other disc in the group"
// flow after ReadDisk reports StateNeedsReconstruction. It resolves the
// group's fixed media capacity once here (all members share one media
// type) and passes it into NewReconstructor, since that's the same fixed
// length buildParityPayload used and is the only thing both sides can
// agree on without knowing the missing disc's real size.
func (m *Manager) StartReconstruction(ctx context.Context) error {
	m.mu.Lock()
	job := m.job
	if job == nil || job.State != StateNeedsReconstruction {
		m.mu.Unlock()
		return fmt.Errorf("no disc needing reconstruction")
	}
	// Claim the job atomically in the same critical section as the check
	// above, so two concurrent callers can't both observe
	// NeedsReconstruction and both start building a Reconstructor for the
	// same job.
	job.State = StateStartingReconstruction
	m.mu.Unlock()

	disk, err := m.cat.GetDisk(ctx, job.DiskID)
	if err != nil {
		return m.revertToNeedsReconstruction(job, err)
	}
	if disk.GroupID == nil {
		return m.revertToNeedsReconstruction(job, fmt.Errorf("disc %s is not part of a cross-disc parity group", job.DiskID))
	}
	members, err := m.cat.GroupMembers(ctx, *disk.GroupID)
	if err != nil {
		return m.revertToNeedsReconstruction(job, err)
	}
	capacity, err := m.cat.MediaCapacity(ctx, disk.MediaType)
	if err != nil {
		return m.revertToNeedsReconstruction(job, err)
	}
	reconstructor, err := burn.NewReconstructor(members, job.DiskID, capacity)
	if err != nil {
		return m.revertToNeedsReconstruction(job, err)
	}

	m.mu.Lock()
	m.reconstructor = reconstructor
	m.reconstructionCapacity = capacity
	job.Err = nil
	job.State = StateReconstructing
	m.mu.Unlock()
	return nil
}

// revertToNeedsReconstruction is used when StartReconstruction fails after
// claiming StateStartingReconstruction: it puts the job back into
// StateNeedsReconstruction (rather than a terminal failure) with err
// recorded, so the user can simply retry "Reconstruct from group".
func (m *Manager) revertToNeedsReconstruction(job *Job, err error) error {
	m.mu.Lock()
	job.State = StateNeedsReconstruction
	job.Err = err
	m.mu.Unlock()
	return err
}

// Remaining lists the disc IDs still needed for reconstruction.
func (m *Manager) Remaining() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reconstructor == nil {
		return nil
	}
	return m.reconstructor.Remaining()
}

// ReadReconstructionDisc is called once the user has inserted the next
// disc named by Remaining() and clicked "Read". Once every other member
// has been supplied, it reconstructs the missing disc's image and
// extracts the originally requested file straight out of it. It reuses
// the same capacity StartReconstruction resolved, rather than taking a
// media type from the caller — every member of a group shares one media
// type, so there's nothing for a caller to legitimately vary here.
func (m *Manager) ReadReconstructionDisc(ctx context.Context, diskID string) error {
	m.mu.Lock()
	job := m.job
	r := m.reconstructor
	capacity := m.reconstructionCapacity
	if job == nil || r == nil || job.State != StateReconstructing {
		m.mu.Unlock()
		return fmt.Errorf("no reconstruction in progress")
	}
	// Claim the job atomically in the same critical section as the check
	// above, so two concurrent callers can't both observe Reconstructing
	// and both fold a disc image into the same Reconstructor at once.
	job.State = StateReadingReconstructionDisc
	m.mu.Unlock()

	imagePath := filepath.Join(m.scratchDir, diskID+".img")
	if err := burn.ReadRawImage(m.device, imagePath, capacity); err != nil {
		return m.revertToReconstructing(job, err)
	}
	data, err := os.ReadFile(imagePath)
	if err != nil {
		return m.revertToReconstructing(job, err)
	}
	if err := r.SupplyDiscImage(diskID, data); err != nil {
		return m.revertToReconstructing(job, err)
	}
	// The image's bytes are now safely held in memory by r — the file on
	// disk is no longer needed. A failure here doesn't affect the already
	// -recorded reconstruction progress, so it's logged rather than failing
	// the call, mirroring burn's cleanupGroupDataISOs pattern.
	if err := os.Remove(imagePath); err != nil {
		log.Printf("retrieve: cleaning up reconstruction image %s after folding into memory: %v", imagePath, err)
	}

	if r.NeedsMore() {
		m.mu.Lock()
		job.Err = nil
		job.State = StateReconstructing
		m.mu.Unlock()
		return nil
	}

	image, err := r.Reconstruct()
	if err != nil {
		return m.revertToReconstructing(job, err)
	}
	reconstructedPath := filepath.Join(m.scratchDir, job.DiskID+".reconstructed.iso")
	if err := os.WriteFile(reconstructedPath, image, 0o644); err != nil {
		return m.revertToReconstructing(job, err)
	}
	defer os.Remove(reconstructedPath)

	return m.readFrom(ctx, job, reconstructedPath)
}

// revertToReconstructing is used when ReadReconstructionDisc fails after
// claiming StateReadingReconstructionDisc: it puts the job back into
// StateReconstructing (rather than a terminal failure) with err recorded,
// so Remaining() still reports what's needed and the user can simply try
// inserting a disc again without losing progress already supplied to r.
func (m *Manager) revertToReconstructing(job *Job, err error) error {
	m.mu.Lock()
	job.State = StateReconstructing
	job.Err = err
	m.mu.Unlock()
	return err
}
