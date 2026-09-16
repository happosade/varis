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

// Pack buckets files sequentially, never splitting a file across buckets.
// It treats the files as one continuous byte stream and cuts a new bucket
// whenever a file's cumulative end offset crosses into the next
// targetSize-wide band. Because files aren't split, a bucket's TotalSize is
// not strictly capped at targetSize: a run of files that all land in the
// same band before the next one lands in the following band can leave a
// bucket over budget, and a single file larger than targetSize gets its own
// oversized bucket — either still gets burned, it just won't necessarily
// share a disc's capacity cleanly.
func Pack(files []FileInfo, targetSize int64) []Bucket {
	var buckets []Bucket
	var current Bucket
	var cumulative int64
	band := int64(-1)
	for _, f := range files {
		cumulative += f.Size
		fileBand := (cumulative - 1) / targetSize
		if band != -1 && fileBand != band && len(current.Files) > 0 {
			buckets = append(buckets, current)
			current = Bucket{}
		}
		band = fileBand
		current.Files = append(current.Files, f)
		current.TotalSize += f.Size
	}
	if len(current.Files) > 0 {
		buckets = append(buckets, current)
	}
	return buckets
}
