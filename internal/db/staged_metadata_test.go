package db

import (
	"context"
	"testing"
)

func TestUpsertAndListStagedMetadata(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM staged_metadata WHERE path LIKE 'test-upsert/%'`)
	})

	if err := UpsertStagedMetadata(ctx, pool, "test-upsert/a.jpg", []string{"family", "2019"}, "vacation photos"); err != nil {
		t.Fatalf("UpsertStagedMetadata: %v", err)
	}

	list, err := ListStagedMetadata(ctx, pool)
	if err != nil {
		t.Fatalf("ListStagedMetadata: %v", err)
	}
	got, ok := list["test-upsert/a.jpg"]
	if !ok {
		t.Fatal("expected test-upsert/a.jpg in ListStagedMetadata result")
	}
	if got.Description != "vacation photos" || len(got.Tags) != 2 {
		t.Errorf("got = %+v, want Tags=[family 2019] Description=\"vacation photos\"", got)
	}

	// Upserting again with different values replaces, not merges.
	if err := UpsertStagedMetadata(ctx, pool, "test-upsert/a.jpg", []string{"work"}, ""); err != nil {
		t.Fatalf("UpsertStagedMetadata (replace): %v", err)
	}
	list, err = ListStagedMetadata(ctx, pool)
	if err != nil {
		t.Fatalf("ListStagedMetadata: %v", err)
	}
	got = list["test-upsert/a.jpg"]
	if len(got.Tags) != 1 || got.Tags[0] != "work" || got.Description != "" {
		t.Errorf("after replace, got = %+v, want Tags=[work] Description=\"\"", got)
	}
}

func TestConsumeStagedMetadata(t *testing.T) {
	pool := requirePool(t)
	ctx := context.Background()

	if err := UpsertStagedMetadata(ctx, pool, "test-consume/b.jpg", []string{"tag1"}, "desc"); err != nil {
		t.Fatalf("UpsertStagedMetadata: %v", err)
	}

	tags, description, err := ConsumeStagedMetadata(ctx, pool, "test-consume/b.jpg")
	if err != nil {
		t.Fatalf("ConsumeStagedMetadata: %v", err)
	}
	if len(tags) != 1 || tags[0] != "tag1" || description != "desc" {
		t.Errorf("ConsumeStagedMetadata = (%v, %q), want ([tag1], \"desc\")", tags, description)
	}

	// The row is gone: consuming again returns the "never tagged" zero
	// value, not an error.
	tags, description, err = ConsumeStagedMetadata(ctx, pool, "test-consume/b.jpg")
	if err != nil {
		t.Fatalf("ConsumeStagedMetadata (second time): %v", err)
	}
	if tags != nil || description != "" {
		t.Errorf("second ConsumeStagedMetadata = (%v, %q), want (nil, \"\")", tags, description)
	}
}

func TestConsumeStagedMetadata_NeverTagged(t *testing.T) {
	pool := requirePool(t)
	tags, description, err := ConsumeStagedMetadata(context.Background(), pool, "test-never-tagged/c.jpg")
	if err != nil {
		t.Fatalf("ConsumeStagedMetadata: %v", err)
	}
	if tags != nil || description != "" {
		t.Errorf("got (%v, %q), want (nil, \"\") for a path with no staged_metadata row", tags, description)
	}
}
