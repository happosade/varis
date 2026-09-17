package burn

import (
	"bytes"
	"testing"

	"varis/internal/db"
	"varis/internal/xordisk"
)

func TestReconstructor_RebuildsMissingDiscFromOthers(t *testing.T) {
	a := bytes.Repeat([]byte{0xAA}, 100)
	b := bytes.Repeat([]byte{0xBB}, 100)
	parity := xordisk.XOR([][]byte{a, b}, 100) // the group's parity disc = XOR of its two data discs

	group := []db.Disk{
		{ID: "BD:0001", Role: "data", GroupID: strPtr("g1"), SlotIndex: intPtr(0)},
		{ID: "BD:0002", Role: "data", GroupID: strPtr("g1"), SlotIndex: intPtr(1)},
		{ID: "BD:0003", Role: "parity", GroupID: strPtr("g1"), SlotIndex: intPtr(2)},
	}

	r, err := NewReconstructor(group, "BD:0001", 100) // pretend BD:0001 is the missing disc; 100 = the group's fixed capacity
	if err != nil {
		t.Fatalf("NewReconstructor: %v", err)
	}
	if !r.NeedsMore() {
		t.Fatal("expected reconstructor to need input before any discs are supplied")
	}

	// Supply every OTHER member one at a time: the surviving data disc
	// BD:0002 (bytes `b`), then the parity disc BD:0003 (bytes
	// XOR(a, b)) — since XOR(b, XOR(a, b)) = a.
	if err := r.SupplyDiscImage("BD:0002", b); err != nil {
		t.Fatalf("SupplyDiscImage: %v", err)
	}
	if !r.NeedsMore() {
		t.Fatalf("expected still needing the parity disc after supplying only BD:0002")
	}
	if err := r.SupplyDiscImage("BD:0003", parity); err != nil {
		t.Fatalf("SupplyDiscImage: %v", err)
	}

	if r.NeedsMore() {
		t.Fatalf("still needs more after supplying every other member: %v", r.Remaining())
	}

	got, err := r.Reconstruct()
	if err != nil {
		t.Fatalf("Reconstruct: %v", err)
	}
	if !bytes.Equal(got, a) {
		t.Errorf("Reconstruct = %x, want %x", got, a)
	}
}

func TestReconstructor_RemainingListsUnsuppliedMembers(t *testing.T) {
	group := []db.Disk{
		{ID: "BD:0001", Role: "data"},
		{ID: "BD:0002", Role: "data"},
		{ID: "BD:0003", Role: "parity"},
	}
	r, err := NewReconstructor(group, "BD:0002", 1000)
	if err != nil {
		t.Fatalf("NewReconstructor: %v", err)
	}

	remaining := r.Remaining()
	if len(remaining) != 2 || remaining[0] != "BD:0001" || remaining[1] != "BD:0003" {
		t.Errorf("Remaining() = %v, want [BD:0001 BD:0003]", remaining)
	}
}

func TestReconstructor_SupplyDiscImage_RejectsNonMember(t *testing.T) {
	group := []db.Disk{
		{ID: "BD:0001", Role: "data"},
		{ID: "BD:0002", Role: "parity"},
	}
	r, err := NewReconstructor(group, "BD:0001", 100)
	if err != nil {
		t.Fatalf("NewReconstructor: %v", err)
	}

	err = r.SupplyDiscImage("BD:9999", bytes.Repeat([]byte{0x01}, 100))
	if err == nil {
		t.Fatal("expected error supplying a disc that isn't a member of this group, got nil")
	}
	want := "disc BD:9999 is not a member of this group"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestReconstructor_Reconstruct_FailsWhenStillMissingDiscs(t *testing.T) {
	group := []db.Disk{
		{ID: "BD:0001", Role: "data"},
		{ID: "BD:0002", Role: "parity"},
	}
	r, err := NewReconstructor(group, "BD:0001", 100)
	if err != nil {
		t.Fatalf("NewReconstructor: %v", err)
	}

	_, err = r.Reconstruct()
	if err == nil {
		t.Fatal("expected error reconstructing before all other members are supplied, got nil")
	}
	want := "still need discs: [BD:0002]"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }
