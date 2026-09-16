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

	id, err := NextDiskID(ctx, pool, "BDTEST")
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

	id2, err := NextDiskID(ctx, pool, "BDTEST")
	if err != nil {
		t.Fatalf("NextDiskID: %v", err)
	}
	if id2 != "BDTEST:0002" {
		t.Fatalf("NextDiskID = %q, want BDTEST:0002", id2)
	}
}
