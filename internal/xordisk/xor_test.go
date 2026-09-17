package xordisk

import (
	"bytes"
	"testing"
)

func TestXOR_ComputesParityAndReconstructsAnyMissingMember(t *testing.T) {
	a := []byte{0x01, 0x02, 0x03, 0x04}
	b := []byte{0x10, 0x20, 0x30, 0x40}
	c := []byte{0xFF, 0x00, 0xFF, 0x00}

	parity := XOR([][]byte{a, b, c}, 4)

	// Reconstruct "b" by XORing everything else (a, c, parity) together.
	reconstructedB := XOR([][]byte{a, c, parity}, 4)
	if !bytes.Equal(reconstructedB, b) {
		t.Errorf("reconstructedB = %x, want %x", reconstructedB, b)
	}
}

func TestXOR_PadsShorterImagesWithZeros(t *testing.T) {
	a := []byte{0x01, 0x02}
	b := []byte{0x0F}

	got := XOR([][]byte{a, b}, 4)
	want := []byte{0x01 ^ 0x0F, 0x02, 0x00, 0x00}
	if !bytes.Equal(got, want) {
		t.Errorf("XOR = %x, want %x", got, want)
	}
}

func TestXOR_TruncatesLongerImages(t *testing.T) {
	a := []byte{0x01, 0x02, 0x03, 0x04, 0xAA, 0xBB}
	b := []byte{0x0F}

	got := XOR([][]byte{a, b}, 4)
	want := []byte{0x01 ^ 0x0F, 0x02, 0x03, 0x04}
	if !bytes.Equal(got, want) {
		t.Errorf("XOR = %x, want %x", got, want)
	}
}

func TestXOR_ZeroLengthReturnsEmptySlice(t *testing.T) {
	a := []byte{0x01, 0x02}
	b := []byte{0x0F}

	got := XOR([][]byte{a, b}, 0)
	if got == nil {
		t.Fatal("XOR returned nil, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("len(XOR) = %d, want 0", len(got))
	}
}
