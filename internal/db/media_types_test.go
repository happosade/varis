package db

import (
	"context"
	"testing"
)

func TestSeedAndListMediaTypes(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()

	if err := SeedMediaTypes(ctx, pool); err != nil {
		t.Fatalf("SeedMediaTypes: %v", err)
	}

	types, err := ListMediaTypes(ctx, pool)
	if err != nil {
		t.Fatalf("ListMediaTypes: %v", err)
	}

	var foundBDR bool
	for _, mt := range types {
		if mt.Name == "BD-R" && mt.CapacityBytes == 25_025_314_816 {
			foundBDR = true
		}
	}
	if !foundBDR {
		t.Errorf("expected seeded BD-R media type, got %+v", types)
	}
}
