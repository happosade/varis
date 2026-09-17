package burn

import (
	"fmt"
	"io"
	"os"

	"varis/internal/db"
	"varis/internal/xordisk"
)

// Reconstructor drives the manual "insert every other disc in the group"
// flow needed to rebuild one lost or unreadable disc.
//
// Not safe for concurrent use: a caller sharing one instance across
// multiple HTTP requests (as plan 03's retrieval flow will do) must
// serialize access itself.
//
// It holds every supplied member's full image in memory for the whole
// (potentially long, human-paced) reconstruction session — the same
// memory trade-off buildParityPayload makes on the burn side, but for a
// longer duration. Accepted for a manual, low-frequency recovery flow,
// not a bug.
type Reconstructor struct {
	missingID     string
	otherIDs      []string
	images        map[string][]byte
	capacityBytes int64
}

// NewReconstructor prepares to reconstruct missingID from every other disc
// in group. capacityBytes must be the same fixed media capacity
// buildParityPayload used when computing the group's parity disc — not
// any member's actual ISO size, which is exactly what's unknown for the
// missing disc. Every disc's media type within a group is the same (see
// planJob), so capacityBytes is the group's single media type's capacity,
// e.g. from db.GetMediaTypeCapacity (added by a later plan's HTTP layer).
//
// Returns an error if missingID isn't actually a member of group — that
// indicates a caller bug (wrong group resolved), and without this check
// every disc in group would silently be treated as an "other" member,
// producing a bogus non-error reconstruction result.
func NewReconstructor(group []db.Disk, missingID string, capacityBytes int64) (*Reconstructor, error) {
	found := false
	for _, d := range group {
		if d.ID == missingID {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("disc %s is not a member of this group", missingID)
	}
	r := &Reconstructor{missingID: missingID, images: map[string][]byte{}, capacityBytes: capacityBytes}
	for _, d := range group {
		if d.ID != missingID {
			r.otherIDs = append(r.otherIDs, d.ID)
		}
	}
	return r, nil
}

// Remaining lists the disc IDs still needed, in the order they were found
// in the group.
func (r *Reconstructor) Remaining() []string {
	var out []string
	for _, id := range r.otherIDs {
		if _, ok := r.images[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}

// NeedsMore reports whether at least one other member's image is still
// unsupplied.
func (r *Reconstructor) NeedsMore() bool {
	return len(r.Remaining()) > 0
}

// SupplyDiscImage records one other member's raw ISO bytes, already
// verified via par2verify by the caller before this is called. Calling it
// twice with the same diskID intentionally overwrites the previous image,
// letting an operator re-supply a disc that read badly the first time.
func (r *Reconstructor) SupplyDiscImage(diskID string, image []byte) error {
	found := false
	for _, id := range r.otherIDs {
		if id == diskID {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("disc %s is not a member of this group", diskID)
	}
	r.images[diskID] = image
	return nil
}

// Reconstruct XORs every supplied image together, recovering the missing
// disc's image, padded/truncated to the group's fixed capacityBytes —
// the same length buildParityPayload used, which is what makes this
// correct regardless of which member is missing. Fails if any member is
// still unsupplied.
func (r *Reconstructor) Reconstruct() ([]byte, error) {
	if r.NeedsMore() {
		return nil, fmt.Errorf("still need discs: %v", r.Remaining())
	}
	var images [][]byte
	for _, id := range r.otherIDs {
		images = append(images, r.images[id])
	}
	return xordisk.XOR(images, r.capacityBytes), nil
}

// ReadRawImage copies exactly sizeBytes from device into destPath, giving
// a byte-for-byte image matching what buildParityPayload XORed at burn
// time (every group member's image is zero-padded to the media's full
// capacity — see xordisk.XOR).
func ReadRawImage(device, destPath string, sizeBytes int64) error {
	in, err := os.Open(device)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.CopyN(out, in, sizeBytes)
	if err != nil && err != io.EOF {
		return err
	}
	return nil
}
