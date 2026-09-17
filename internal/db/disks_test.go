package db

import (
	"context"
	"testing"
)

func TestDiskLifecycle(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	if err := SeedMediaTypes(ctx, pool); err != nil {
		t.Fatalf("SeedMediaTypes: %v", err)
	}

	id, err := NextDiskID(ctx, pool, "BDTEST", 0)
	if err != nil {
		t.Fatalf("NextDiskID: %v", err)
	}
	if id != "BDTEST:0001" {
		t.Fatalf("NextDiskID = %q, want BDTEST:0001", id)
	}

	err = InsertDisk(ctx, pool, Disk{
		ID:            id,
		MediaType:     "BD-R",
		ParityPercent: 10,
		Role:          "data",
		ISOHash:       "deadbeef",
	})
	if err != nil {
		t.Fatalf("InsertDisk: %v", err)
	}

	got, err := GetDisk(ctx, pool, id)
	if err != nil {
		t.Fatalf("GetDisk: %v", err)
	}
	if got.ParityPercent != 10 || got.Role != "data" {
		t.Errorf("GetDisk = %+v, want ParityPercent=10 Role=data", got)
	}

	id2, err := NextDiskID(ctx, pool, "BDTEST", 0)
	if err != nil {
		t.Fatalf("NextDiskID: %v", err)
	}
	if id2 != "BDTEST:0002" {
		t.Fatalf("NextDiskID = %q, want BDTEST:0002", id2)
	}
}

func TestInsertDisk_CarriesIsDryRun(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	if err := SeedMediaTypes(ctx, pool); err != nil {
		t.Fatalf("SeedMediaTypes: %v", err)
	}
	id, err := NextDiskID(ctx, pool, "BDDRYTEST", 0)
	if err != nil {
		t.Fatalf("NextDiskID: %v", err)
	}
	if err := InsertDisk(ctx, pool, Disk{ID: id, MediaType: "BD-R", ParityPercent: 10, Role: "data", IsDryRun: true}); err != nil {
		t.Fatalf("InsertDisk: %v", err)
	}

	got, err := GetDisk(ctx, pool, id)
	if err != nil {
		t.Fatalf("GetDisk: %v", err)
	}
	if !got.IsDryRun {
		t.Errorf("GetDisk.IsDryRun = false, want true")
	}
}

func TestDeleteDryRunDisc_RemovesDiskAndFileRows(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	if err := SeedMediaTypes(ctx, pool); err != nil {
		t.Fatalf("SeedMediaTypes: %v", err)
	}
	id, err := NextDiskID(ctx, pool, "BDDRYDEL", 0)
	if err != nil {
		t.Fatalf("NextDiskID: %v", err)
	}
	if err := InsertDisk(ctx, pool, Disk{ID: id, MediaType: "BD-R", ParityPercent: 10, Role: "data", IsDryRun: true}); err != nil {
		t.Fatalf("InsertDisk: %v", err)
	}
	if err := InsertFile(ctx, pool, FileRecord{DiskID: id, OriginalPath: "a.jpg", SizeBytes: 1}); err != nil {
		t.Fatalf("InsertFile: %v", err)
	}

	if err := DeleteDryRunDisc(ctx, pool, id); err != nil {
		t.Fatalf("DeleteDryRunDisc: %v", err)
	}

	if _, err := GetDisk(ctx, pool, id); err == nil {
		t.Error("expected the disk row to be gone after DeleteDryRunDisc")
	}
	var fileCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM files WHERE disk_id = $1`, id).Scan(&fileCount); err != nil {
		t.Fatalf("counting files: %v", err)
	}
	if fileCount != 0 {
		t.Errorf("file rows for %s = %d, want 0", id, fileCount)
	}
}

func TestDeleteDryRunDisc_RefusesARealDisc(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	if err := SeedMediaTypes(ctx, pool); err != nil {
		t.Fatalf("SeedMediaTypes: %v", err)
	}
	id, err := NextDiskID(ctx, pool, "BDDRYREAL", 0)
	if err != nil {
		t.Fatalf("NextDiskID: %v", err)
	}
	// IsDryRun left false: a real disc.
	if err := InsertDisk(ctx, pool, Disk{ID: id, MediaType: "BD-R", ParityPercent: 10, Role: "data"}); err != nil {
		t.Fatalf("InsertDisk: %v", err)
	}
	if err := InsertFile(ctx, pool, FileRecord{DiskID: id, OriginalPath: "a.jpg", SizeBytes: 1}); err != nil {
		t.Fatalf("InsertFile: %v", err)
	}

	if err := DeleteDryRunDisc(ctx, pool, id); err != nil {
		t.Fatalf("DeleteDryRunDisc: %v", err)
	}

	if _, err := GetDisk(ctx, pool, id); err != nil {
		t.Errorf("expected the real disc's row to survive DeleteDryRunDisc, got: %v", err)
	}
	var fileCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM files WHERE disk_id = $1`, id).Scan(&fileCount); err != nil {
		t.Fatalf("counting files: %v", err)
	}
	if fileCount != 1 {
		t.Errorf("file rows for real disc %s = %d, want 1 (untouched)", id, fileCount)
	}
}

func TestListDryRunDisks_OnlyReturnsDryRunDiscs(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	if err := SeedMediaTypes(ctx, pool); err != nil {
		t.Fatalf("SeedMediaTypes: %v", err)
	}
	dryID, err := NextDiskID(ctx, pool, "BDLISTDRY", 0)
	if err != nil {
		t.Fatalf("NextDiskID: %v", err)
	}
	if err := InsertDisk(ctx, pool, Disk{ID: dryID, MediaType: "BD-R", ParityPercent: 10, Role: "data", IsDryRun: true}); err != nil {
		t.Fatalf("InsertDisk (dry run): %v", err)
	}
	realID, err := NextDiskID(ctx, pool, "BDLISTREAL", 0)
	if err != nil {
		t.Fatalf("NextDiskID: %v", err)
	}
	if err := InsertDisk(ctx, pool, Disk{ID: realID, MediaType: "BD-R", ParityPercent: 10, Role: "data"}); err != nil {
		t.Fatalf("InsertDisk (real): %v", err)
	}

	discs, err := ListDryRunDisks(ctx, pool)
	if err != nil {
		t.Fatalf("ListDryRunDisks: %v", err)
	}
	foundDry, foundReal := false, false
	for _, d := range discs {
		if d.ID == dryID {
			foundDry = true
		}
		if d.ID == realID {
			foundReal = true
		}
	}
	if !foundDry {
		t.Errorf("expected %s (dry run) in ListDryRunDisks", dryID)
	}
	if foundReal {
		t.Errorf("expected %s (real) NOT in ListDryRunDisks", realID)
	}
}
