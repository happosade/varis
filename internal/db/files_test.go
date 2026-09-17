package db

import (
	"context"
	"testing"
)

func TestGetFile(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	if err := SeedMediaTypes(ctx, pool); err != nil {
		t.Fatalf("SeedMediaTypes: %v", err)
	}
	id, err := NextDiskID(ctx, pool, "BDGETFILE", 0)
	if err != nil {
		t.Fatalf("NextDiskID: %v", err)
	}
	if err := InsertDisk(ctx, pool, Disk{ID: id, MediaType: "BD-R", ParityPercent: 10, Role: "data"}); err != nil {
		t.Fatalf("InsertDisk: %v", err)
	}
	if err := InsertFile(ctx, pool, FileRecord{DiskID: id, OriginalPath: "a/b.txt", SizeBytes: 5, FileHash: "x"}); err != nil {
		t.Fatalf("InsertFile: %v", err)
	}

	files, err := SearchFiles(ctx, pool, "b.txt")
	if err != nil || len(files) == 0 {
		t.Fatalf("SearchFiles: %v, %+v", err, files)
	}

	got, err := GetFile(ctx, pool, files[0].ID)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if got.DiskID != id || got.OriginalPath != "a/b.txt" {
		t.Errorf("GetFile = %+v", got)
	}
}

func TestInsertAndGetFile_CarriesTagsAndDescription(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	if err := SeedMediaTypes(ctx, pool); err != nil {
		t.Fatalf("SeedMediaTypes: %v", err)
	}
	id, err := NextDiskID(ctx, pool, "BDTAGTEST", 0)
	if err != nil {
		t.Fatalf("NextDiskID: %v", err)
	}
	if err := InsertDisk(ctx, pool, Disk{ID: id, MediaType: "BD-R", ParityPercent: 10, Role: "data"}); err != nil {
		t.Fatalf("InsertDisk: %v", err)
	}

	err = InsertFile(ctx, pool, FileRecord{
		DiskID:       id,
		OriginalPath: "vacation/photo.jpg",
		SizeBytes:    123,
		FileHash:     "abc",
		Tags:         []string{"family", "2019"},
		Description:  "vacation photos",
	})
	if err != nil {
		t.Fatalf("InsertFile: %v", err)
	}

	var fileID string
	err = pool.QueryRow(ctx, `SELECT id FROM files WHERE disk_id = $1`, id).Scan(&fileID)
	if err != nil {
		t.Fatalf("looking up inserted file id: %v", err)
	}

	got, err := GetFile(ctx, pool, fileID)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if len(got.Tags) != 2 || got.Description != "vacation photos" {
		t.Errorf("GetFile = %+v, want Tags=[family 2019] Description=\"vacation photos\"", got)
	}
}

func TestInsertFile_NilTagsDefaultToEmpty(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	if err := SeedMediaTypes(ctx, pool); err != nil {
		t.Fatalf("SeedMediaTypes: %v", err)
	}
	id, err := NextDiskID(ctx, pool, "BDNILTEST", 0)
	if err != nil {
		t.Fatalf("NextDiskID: %v", err)
	}
	if err := InsertDisk(ctx, pool, Disk{ID: id, MediaType: "BD-R", ParityPercent: 10, Role: "data"}); err != nil {
		t.Fatalf("InsertDisk: %v", err)
	}

	// Tags is nil here — the common case for a file that was never
	// staged with metadata (ConsumeStagedMetadata returns nil, not []).
	err = InsertFile(ctx, pool, FileRecord{DiskID: id, OriginalPath: "untagged.jpg", SizeBytes: 1})
	if err != nil {
		t.Fatalf("InsertFile with nil Tags: %v", err)
	}
}

func TestSearchFiles_MatchesTagsAndDescription(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	if err := SeedMediaTypes(ctx, pool); err != nil {
		t.Fatalf("SeedMediaTypes: %v", err)
	}
	id, err := NextDiskID(ctx, pool, "BDSEARCHTEST", 0)
	if err != nil {
		t.Fatalf("NextDiskID: %v", err)
	}
	if err := InsertDisk(ctx, pool, Disk{ID: id, MediaType: "BD-R", ParityPercent: 10, Role: "data"}); err != nil {
		t.Fatalf("InsertDisk: %v", err)
	}
	err = InsertFile(ctx, pool, FileRecord{
		DiskID:       id,
		OriginalPath: "IMG_00421.jpg",
		SizeBytes:    1,
		Tags:         []string{"family videos 2019"},
	})
	if err != nil {
		t.Fatalf("InsertFile: %v", err)
	}

	results, err := SearchFiles(ctx, pool, "family videos 2019")
	if err != nil {
		t.Fatalf("SearchFiles: %v", err)
	}
	found := false
	for _, f := range results {
		if f.OriginalPath == "IMG_00421.jpg" {
			found = true
		}
	}
	if !found {
		t.Error("expected SearchFiles to match by tag even though the tag doesn't appear in the filename")
	}
}

func TestGetMediaTypeCapacity(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	if err := SeedMediaTypes(ctx, pool); err != nil {
		t.Fatalf("SeedMediaTypes: %v", err)
	}
	capacity, err := GetMediaTypeCapacity(ctx, pool, "BD-R")
	if err != nil {
		t.Fatalf("GetMediaTypeCapacity: %v", err)
	}
	if capacity != 25_025_314_816 {
		t.Errorf("capacity = %d, want 25025314816", capacity)
	}
}
