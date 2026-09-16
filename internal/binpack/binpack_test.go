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
		{Path: "a", Size: 40},
		{Path: "b", Size: 40},
		{Path: "c", Size: 40},
	}

	buckets := Pack(files, 70)

	if len(buckets) != 2 {
		t.Fatalf("len(buckets) = %d, want 2", len(buckets))
	}
	if len(buckets[0].Files) != 1 || buckets[0].TotalSize != 40 {
		t.Errorf("buckets[0] = %+v", buckets[0])
	}
	if len(buckets[1].Files) != 2 || buckets[1].TotalSize != 80 {
		t.Errorf("buckets[1] = %+v", buckets[1])
	}
}
