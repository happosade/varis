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
