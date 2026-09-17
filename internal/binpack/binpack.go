package binpack

import (
	"io/fs"
	"path/filepath"
	"sort"
)

type FileInfo struct {
	Path string // relative to the staging root
	Size int64
}

type Bucket struct {
	Files     []FileInfo
	TotalSize int64
}

// TargetDataSize returns the data budget for one disc so that
// data + par2(data, parityPercent) ~= capacity.
func TargetDataSize(capacity int64, parityPercent int) int64 {
	return capacity * 100 / int64(100+parityPercent)
}

// ScanStaging walks root and returns every regular file, relative to root,
// in deterministic (sorted) order.
func ScanStaging(root string) ([]FileInfo, error) {
	var files []FileInfo
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			files = append(files, FileInfo{Path: rel, Size: info.Size()})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// Pack greedily buckets files so each bucket's TotalSize stays at or under
// targetSize. A single file larger than targetSize gets its own oversized
// bucket — it still gets burned, it just won't share a disc with anything.
func Pack(files []FileInfo, targetSize int64) []Bucket {
	var buckets []Bucket
	var current Bucket
	for _, f := range files {
		if current.TotalSize > 0 && current.TotalSize+f.Size > targetSize {
			buckets = append(buckets, current)
			current = Bucket{}
		}
		current.Files = append(current.Files, f)
		current.TotalSize += f.Size
	}
	if len(current.Files) > 0 {
		buckets = append(buckets, current)
	}
	return buckets
}
