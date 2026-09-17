package burn

import (
	"fmt"

	"varis/internal/db"
	"varis/internal/xordisk"
)

// Reconstructor drives the manual "insert every other disc in the group"
// flow needed to rebuild one lost or unreadable disc.
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
func NewReconstructor(group []db.Disk, missingID string, capacityBytes int64) *Reconstructor {
	r := &Reconstructor{missingID: missingID, images: map[string][]byte{}, capacityBytes: capacityBytes}
	for _, d := range group {
		if d.ID != missingID {
			r.otherIDs = append(r.otherIDs, d.ID)
		}
	}
	return r
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

func (r *Reconstructor) NeedsMore() bool {
	return len(r.Remaining()) > 0
}

// SupplyDiscImage records one other member's raw ISO bytes, already
// verified via par2verify by the caller before this is called.
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
