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

func TestWriteTar_MultipleFilesAndNestedPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "b.txt"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}
	bucket := binpack.Bucket{
		Files: []binpack.FileInfo{
			{Path: "a.txt", Size: 5},
			{Path: filepath.Join("sub", "b.txt"), Size: 5},
		},
		TotalSize: 10,
	}

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

	got := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		content, _ := io.ReadAll(tr)
		got[hdr.Name] = string(content)
	}

	if got["a.txt"] != "hello" {
		t.Errorf("a.txt = %q, want hello", got["a.txt"])
	}
	if got[filepath.Join("sub", "b.txt")] != "world" {
		t.Errorf("sub/b.txt = %q, want world (got map: %+v)", got[filepath.Join("sub", "b.txt")], got)
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
