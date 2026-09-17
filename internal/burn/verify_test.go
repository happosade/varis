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
