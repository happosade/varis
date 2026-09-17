package burn

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"varis/internal/binpack"
)

// WriteTar writes bucket's files (read from stagingRoot) into a tar archive
// at destPath, gzipped if compress is true.
func WriteTar(stagingRoot string, bucket binpack.Bucket, destPath string, compress bool) error {
	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	var w io.Writer = out
	var gz *gzip.Writer
	if compress {
		gz = gzip.NewWriter(out)
		w = gz
	}

	tw := tar.NewWriter(w)
	for _, f := range bucket.Files {
		if err := addFileToTar(tw, stagingRoot, f); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if gz != nil {
		return gz.Close()
	}
	return nil
}

func addFileToTar(tw *tar.Writer, root string, f binpack.FileInfo) error {
	fullPath := filepath.Join(root, f.Path)
	info, err := os.Stat(fullPath)
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = f.Path
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	in, err := os.Open(fullPath)
	if err != nil {
		return err
	}
	defer in.Close()
	_, err = io.Copy(tw, in)
	return err
}

// ExtractFile pulls one member (relPath) out of a tar archive at tarPath
// (gzipped if compressed) and writes it to destPath.
func ExtractFile(tarPath string, compressed bool, relPath, destPath string) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()

	var r io.Reader = f
	if compressed {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		r = gz
	}

	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("file %s not found in %s", relPath, tarPath)
		}
		if err != nil {
			return err
		}
		if hdr.Name == relPath {
			out, err := os.Create(destPath)
			if err != nil {
				return err
			}
			defer out.Close()
			_, err = io.Copy(out, tr)
			return err
		}
	}
}
