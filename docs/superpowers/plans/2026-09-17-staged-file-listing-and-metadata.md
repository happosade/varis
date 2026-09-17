# Staged File Listing & Metadata Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the dashboard's plain "Staged: N bytes" line with an actual listing of staged files, and let the user attach tags + a description to individual files, whole top-level folders, or an arbitrary selection of both — persisted so the Library's search can match on them too.

**Architecture:** A new `staged_metadata` Postgres table holds tags/description for not-yet-burned files, keyed by staging-relative path. `files` gains the same two columns so metadata survives permanently once a disc is burned — `burn.pipeline.commitDisc` copies it over (consuming the staged row) at the moment each file is committed. The dashboard groups `binpack.ScanStaging`'s file list by top-level folder and renders it with checkboxes; a single POST resolves the selection (individual paths and/or folder-prefix selections) and upserts metadata for all of them at once. The Library's existing search query is broadened to match tags/description in addition to path.

**Tech Stack:** Go 1.26+ stdlib, `pgx/v5`, `html/template` + HTMX (all already in use — no new dependencies).

**Depends on:** the existing `optical-disc-archive` implementation (plans 00-04); this is additive.

---

### Task 1: Schema — `staged_metadata` table and `files` columns

**Files:**
- Modify: `internal/db/schema.sql`
- Test: `internal/db/db_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/db/db_test.go`:

```go
func TestConnect_AppliesStagedMetadataSchema(t *testing.T) {
	pool := requirePool(t)
	var exists bool
	err := pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'staged_metadata')`).Scan(&exists)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !exists {
		t.Error("staged_metadata table was not created")
	}

	var hasTags, hasDescription bool
	err = pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'files' AND column_name = 'tags')`).Scan(&hasTags)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	err = pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'files' AND column_name = 'description')`).Scan(&hasDescription)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !hasTags || !hasDescription {
		t.Errorf("files.tags/files.description not found (hasTags=%v hasDescription=%v)", hasTags, hasDescription)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `docker compose up -d archive-db && DATABASE_URL=postgres://varis:varis@localhost:5432/varis?sslmode=disable go test ./internal/db/... -run TestConnect_AppliesStagedMetadataSchema -v`

(If `archive-db` doesn't publish port 5432 yet, temporarily add `ports: ["5432:5432"]` under `archive-db` in `docker-compose.yml` for local testing, or run this from inside the `archive-core` container instead.)

Expected: FAIL — `staged_metadata` doesn't exist yet, and `files` has no `tags`/`description` columns.

- [ ] **Step 3: Add the schema changes**

Append to `internal/db/schema.sql`:

```sql
CREATE TABLE IF NOT EXISTS staged_metadata (
    path        TEXT PRIMARY KEY,
    tags        TEXT[] NOT NULL DEFAULT '{}',
    description TEXT NOT NULL DEFAULT '',
    updated_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

ALTER TABLE files ADD COLUMN IF NOT EXISTS tags TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE files ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_files_tags ON files USING gin (tags);
```

`db.Connect` runs the whole file via `pool.Exec` on every connect (see `internal/db/db.go`), so every statement must be safe to re-run against an already-migrated database — `CREATE TABLE IF NOT EXISTS` and `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` (both supported by Postgres 13+, which this project already requires) keep that property.

- [ ] **Step 4: Run test to verify it passes**

Run: `DATABASE_URL=postgres://varis:varis@localhost:5432/varis?sslmode=disable go test ./internal/db/... -v`
Expected: PASS (all `internal/db` tests, including the new one)

- [ ] **Step 5: Commit**

```bash
git add internal/db/schema.sql internal/db/db_test.go
git commit -m "$(cat <<'EOF'
Add staged_metadata table and files.tags/files.description columns

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 2: DB layer — staged metadata queries

**Files:**
- Create: `internal/db/staged_metadata.go`
- Test: `internal/db/staged_metadata_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// internal/db/staged_metadata_test.go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `DATABASE_URL=postgres://varis:varis@localhost:5432/varis?sslmode=disable go test ./internal/db/... -run 'TestUpsertAndListStagedMetadata|TestConsumeStagedMetadata' -v`
Expected: FAIL with "undefined: UpsertStagedMetadata" (etc.) — the functions don't exist yet.

- [ ] **Step 3: Implement the queries**

```go
// internal/db/staged_metadata.go
package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StagedMetadata is the tags/description attached to one not-yet-burned
// staged file, keyed externally by its staging-relative path (the same
// path binpack.FileInfo.Path uses).
type StagedMetadata struct {
	Tags        []string
	Description string
}

// UpsertStagedMetadata replaces (not merges) the tags/description for
// path — applying metadata to a selection always sets the exact submitted
// values, regardless of what a path held before.
func UpsertStagedMetadata(ctx context.Context, pool *pgxpool.Pool, path string, tags []string, description string) error {
	if tags == nil {
		tags = []string{}
	}
	_, err := pool.Exec(ctx,
		`INSERT INTO staged_metadata (path, tags, description, updated_at)
		 VALUES ($1, $2, $3, CURRENT_TIMESTAMP)
		 ON CONFLICT (path) DO UPDATE SET tags = $2, description = $3, updated_at = CURRENT_TIMESTAMP`,
		path, tags, description)
	return err
}

// ListStagedMetadata returns every staged file's metadata, keyed by path,
// for joining against a fresh binpack.ScanStaging listing.
func ListStagedMetadata(ctx context.Context, pool *pgxpool.Pool) (map[string]StagedMetadata, error) {
	rows, err := pool.Query(ctx, `SELECT path, tags, description FROM staged_metadata`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]StagedMetadata{}
	for rows.Next() {
		var path string
		var m StagedMetadata
		if err := rows.Scan(&path, &m.Tags, &m.Description); err != nil {
			return nil, err
		}
		out[path] = m
	}
	return out, rows.Err()
}

// ConsumeStagedMetadata reads and deletes path's staged metadata in one
// round trip, called once per file at burn time (see burn.Cataloger).
// A path that was never tagged returns (nil, "", nil), not an error.
func ConsumeStagedMetadata(ctx context.Context, pool *pgxpool.Pool, path string) ([]string, string, error) {
	var tags []string
	var description string
	err := pool.QueryRow(ctx,
		`DELETE FROM staged_metadata WHERE path = $1 RETURNING tags, description`, path).
		Scan(&tags, &description)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", nil
	}
	return tags, description, err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `DATABASE_URL=postgres://varis:varis@localhost:5432/varis?sslmode=disable go test ./internal/db/... -v`
Expected: PASS (all `internal/db` tests)

- [ ] **Step 5: Commit**

```bash
git add internal/db/staged_metadata.go internal/db/staged_metadata_test.go
git commit -m "$(cat <<'EOF'
Add staged metadata queries: Upsert, List, Consume

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 3: DB layer — carry tags/description on `FileRecord`, broaden search

**Files:**
- Modify: `internal/db/files.go`
- Test: `internal/db/files_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/db/files_test.go` (check the existing file first for its helper pattern — it already has a test inserting a disk before a file, matching the `files.disk_id` foreign key; reuse that same setup):

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `DATABASE_URL=postgres://varis:varis@localhost:5432/varis?sslmode=disable go test ./internal/db/... -run 'TestInsertAndGetFile_CarriesTagsAndDescription|TestInsertFile_NilTagsDefaultToEmpty|TestSearchFiles_MatchesTagsAndDescription' -v`
Expected: FAIL — `FileRecord` has no `Tags`/`Description` fields yet, so this won't compile.

- [ ] **Step 3: Update `FileRecord`, `InsertFile`, `GetFile`, `SearchFiles`**

Replace the contents of `internal/db/files.go`:

```go
package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type FileRecord struct {
	ID           string
	DiskID       string
	OriginalPath string
	SizeBytes    int64
	FileHash     string
	Tags         []string
	Description  string
}

func InsertFile(ctx context.Context, pool *pgxpool.Pool, f FileRecord) error {
	tags := f.Tags
	if tags == nil {
		// files.tags is NOT NULL; a nil Go slice would otherwise send SQL
		// NULL and violate that constraint. The common case for this is a
		// file that was never given staged metadata before burning.
		tags = []string{}
	}
	_, err := pool.Exec(ctx,
		`INSERT INTO files (disk_id, original_path, size_bytes, file_hash, tags, description)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		f.DiskID, f.OriginalPath, f.SizeBytes, f.FileHash, tags, f.Description)
	return err
}

func GetFile(ctx context.Context, pool *pgxpool.Pool, id string) (FileRecord, error) {
	var f FileRecord
	err := pool.QueryRow(ctx,
		`SELECT id, disk_id, original_path, size_bytes, file_hash, tags, description FROM files WHERE id = $1`, id).
		Scan(&f.ID, &f.DiskID, &f.OriginalPath, &f.SizeBytes, &f.FileHash, &f.Tags, &f.Description)
	if errors.Is(err, pgx.ErrNoRows) {
		return FileRecord{}, fmt.Errorf("file %s not found: %w", id, ErrNotFound)
	}
	return f, err
}

func SearchFiles(ctx context.Context, pool *pgxpool.Pool, query string) ([]FileRecord, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, disk_id, original_path, size_bytes, file_hash, tags, description FROM files
		 WHERE original_path ILIKE '%' || $1 || '%'
		    OR description ILIKE '%' || $1 || '%'
		    OR EXISTS (SELECT 1 FROM unnest(tags) t WHERE t ILIKE '%' || $1 || '%')
		 ORDER BY similarity(original_path, $1) DESC LIMIT 50`, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileRecord
	for rows.Next() {
		var f FileRecord
		if err := rows.Scan(&f.ID, &f.DiskID, &f.OriginalPath, &f.SizeBytes, &f.FileHash, &f.Tags, &f.Description); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `DATABASE_URL=postgres://varis:varis@localhost:5432/varis?sslmode=disable go test ./internal/db/... -v`
Expected: PASS (all `internal/db` tests)

- [ ] **Step 5: Run the whole module to check nothing else broke**

Run: `go build ./...`
Expected: builds — nothing outside `internal/db` references `FileRecord{...}` with positional (unkeyed) struct literals, so adding two trailing fields is a compatible change. (This plan's later tasks add the two new fields explicitly wherever `FileRecord` is constructed.)

- [ ] **Step 6: Commit**

```bash
git add internal/db/files.go internal/db/files_test.go
git commit -m "$(cat <<'EOF'
Carry tags/description on FileRecord; broaden SearchFiles to match them

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 4: Burn carry-forward — `Cataloger.ConsumeStagedMetadata`

This task touches every fake that implements `burn.Cataloger`, in the same
commit, so the module keeps compiling throughout: `internal/burn/pipeline_test.go`'s
`FakeCataloger`, `internal/web/handlers_test.go`'s `stubCataloger`, and
`internal/retrieve/integration_test.go`'s `inMemoryStore` all implement this
interface today and will fail to compile the moment it grows a new method.

**Files:**
- Modify: `internal/burn/pipeline.go`
- Modify: `internal/burn/pipeline_test.go`
- Modify: `internal/web/handlers_test.go`
- Modify: `internal/retrieve/integration_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/burn/pipeline_test.go` (near the other `FakeCataloger`-based
`Manager` tests — check the file for the exact staging-dir/spool-dir/device
setup pattern an existing single-disc burn test already uses, and mirror
it):

```go
func TestManager_CommitDisc_FoldsStagedMetadataIntoFileRecord(t *testing.T) {
	staging := t.TempDir()
	if err := os.WriteFile(filepath.Join(staging, "a.bin"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	device := t.TempDir() + "/device"
	cat := newFakeCataloger()
	cat.StagedMetadata["a.bin"] = fakeStagedMeta{Tags: []string{"family"}, Description: "a note"}
	ex := fakeExecutorForHappyPath(device)

	mgr := NewManager(cat, ex, staging, t.TempDir(), device)
	if err := mgr.Start(context.Background(), Options{MediaType: "BD-R", CapacityBytes: 1_000_000, ParityPercent: 10}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := mgr.ContinueNextDisc(context.Background()); err != nil {
		t.Fatalf("ContinueNextDisc: %v", err)
	}

	if len(cat.Files) != 1 {
		t.Fatalf("Files = %v, want exactly 1", cat.Files)
	}
	f := cat.Files[0]
	if len(f.Tags) != 1 || f.Tags[0] != "family" || f.Description != "a note" {
		t.Errorf("committed FileRecord = %+v, want Tags=[family] Description=\"a note\"", f)
	}
	if _, stillStaged := cat.StagedMetadata["a.bin"]; stillStaged {
		t.Error("expected a.bin's staged metadata to be consumed (deleted) once burned")
	}
}

func TestManager_CommitDisc_UntaggedFileGetsEmptyMetadata(t *testing.T) {
	staging := t.TempDir()
	if err := os.WriteFile(filepath.Join(staging, "b.bin"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	device := t.TempDir() + "/device"
	cat := newFakeCataloger()
	ex := fakeExecutorForHappyPath(device)

	mgr := NewManager(cat, ex, staging, t.TempDir(), device)
	if err := mgr.Start(context.Background(), Options{MediaType: "BD-R", CapacityBytes: 1_000_000, ParityPercent: 10}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := mgr.ContinueNextDisc(context.Background()); err != nil {
		t.Fatalf("ContinueNextDisc: %v", err)
	}

	if len(cat.Files) != 1 {
		t.Fatalf("Files = %v, want exactly 1", cat.Files)
	}
	if len(cat.Files[0].Tags) != 0 || cat.Files[0].Description != "" {
		t.Errorf("committed FileRecord = %+v, want empty Tags/Description for an untagged file", cat.Files[0])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/burn/... -run 'TestManager_CommitDisc_FoldsStagedMetadataIntoFileRecord|TestManager_CommitDisc_UntaggedFileGetsEmptyMetadata' -v`
Expected: FAIL to compile — `cat.StagedMetadata` and `fakeStagedMeta` don't exist yet.

- [ ] **Step 3: Add `ConsumeStagedMetadata` to the `Cataloger` interface and `pgxCataloger`**

In `internal/burn/pipeline.go`, change the interface (shown here with its
existing methods for context — add only the last one):

```go
type Cataloger interface {
	NextDiskID(ctx context.Context, prefix string, alreadyAllocated int) (string, error)
	NewBurnJobID(ctx context.Context) (string, error)
	NextGroupID(ctx context.Context, burnJobID string, groupSize int) (string, error)
	InsertDisk(ctx context.Context, d db.Disk) error
	InsertFile(ctx context.Context, f db.FileRecord) error
	ConsumeStagedMetadata(ctx context.Context, path string) (tags []string, description string, err error)
}
```

Add the `pgxCataloger` implementation next to its other methods:

```go
func (c pgxCataloger) ConsumeStagedMetadata(ctx context.Context, path string) ([]string, string, error) {
	return db.ConsumeStagedMetadata(ctx, c.pool, path)
}
```

- [ ] **Step 4: Fold consumed metadata into `commitDisc`'s `FileRecord`**

In `internal/burn/pipeline.go`, `commitDisc`'s file loop currently reads:

```go
	for _, f := range plan.Bucket.Files {
		originalPath := filepath.Join(m.stagingDir, f.Path)
		hash, err := HashFile(originalPath)
		if err != nil {
			return err
		}
		if err := m.cat.InsertFile(ctx, db.FileRecord{
			DiskID:       plan.DiskID,
			OriginalPath: f.Path,
			SizeBytes:    f.Size,
			FileHash:     hash,
		}); err != nil {
			return err
		}
```

Change it to:

```go
	for _, f := range plan.Bucket.Files {
		originalPath := filepath.Join(m.stagingDir, f.Path)
		hash, err := HashFile(originalPath)
		if err != nil {
			return err
		}
		tags, description, err := m.cat.ConsumeStagedMetadata(ctx, f.Path)
		if err != nil {
			return err
		}
		if err := m.cat.InsertFile(ctx, db.FileRecord{
			DiskID:       plan.DiskID,
			OriginalPath: f.Path,
			SizeBytes:    f.Size,
			FileHash:     hash,
			Tags:         tags,
			Description:  description,
		}); err != nil {
			return err
		}
```

(The rest of the loop — removing the file from staging — is unchanged.)

- [ ] **Step 5: Update `FakeCataloger` in `internal/burn/pipeline_test.go`**

Add a `StagedMetadata` field and its type next to `FakeCataloger`'s other
fields, and initialize it in the constructor:

```go
type fakeStagedMeta struct {
	Tags        []string
	Description string
}

type FakeCataloger struct {
	mu             sync.Mutex
	seq            map[string]int
	burnJobSeq     int
	groupSeq       int
	Disks          []db.Disk
	Files          []db.FileRecord
	StagedMetadata map[string]fakeStagedMeta
}

func newFakeCataloger() *FakeCataloger {
	return &FakeCataloger{seq: map[string]int{}, StagedMetadata: map[string]fakeStagedMeta{}}
}
```

Add the method:

```go
func (f *FakeCataloger) ConsumeStagedMetadata(ctx context.Context, path string) ([]string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.StagedMetadata[path]
	if !ok {
		return nil, "", nil
	}
	delete(f.StagedMetadata, path)
	return m.Tags, m.Description, nil
}
```

- [ ] **Step 6: Update `stubCataloger` in `internal/web/handlers_test.go`**

Add, next to its other methods:

```go
func (c *stubCataloger) ConsumeStagedMetadata(ctx context.Context, path string) ([]string, string, error) {
	return nil, "", nil
}
```

- [ ] **Step 7: Update `inMemoryStore` in `internal/retrieve/integration_test.go`**

Add, next to its other methods:

```go
func (s *inMemoryStore) ConsumeStagedMetadata(ctx context.Context, path string) ([]string, string, error) {
	return nil, "", nil
}
```

- [ ] **Step 8: Run tests to verify everything passes and the module compiles**

Run: `go build ./... && go vet ./... && go test ./... -race`
Expected: PASS everywhere — `internal/burn`, `internal/web`, and `internal/retrieve` all still compile with the widened interface, and the two new burn tests pass.

- [ ] **Step 9: Commit**

```bash
git add internal/burn/pipeline.go internal/burn/pipeline_test.go internal/web/handlers_test.go internal/retrieve/integration_test.go
git commit -m "$(cat <<'EOF'
Fold consumed staged metadata into a burned file's FileRecord

Cataloger grows ConsumeStagedMetadata; every fake implementing the interface (burn, web, retrieve packages) gets a trivial no-op or configurable implementation in this same commit so the module keeps compiling.

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 5: Dashboard — staged file listing with bulk metadata apply

Files are grouped by top-level folder for display. A folder's header
checkbox is just another selection value ending in `/` (e.g. `vacation/`);
resolving the actual set of files to apply metadata to happens server-side
against a fresh `binpack.ScanStaging` — no client-side JavaScript needed to
expand a folder selection into its member files.

**Files:**
- Create: `internal/web/staging.go`
- Create: `internal/web/staging_test.go`
- Modify: `internal/web/server.go`
- Modify: `internal/web/dashboard.go`
- Modify: `internal/web/templates/dashboard.html`
- Modify: `internal/web/templates/layout.html`

- [ ] **Step 1: Write the failing tests**

```go
// internal/web/staging_test.go
package web

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"varis/internal/binpack"
	"varis/internal/db"
)

func TestGroupStagedFiles_RootFilesFirstThenAlphabeticalFolders(t *testing.T) {
	files := []binpack.FileInfo{
		{Path: "zzz-folder/x.txt", Size: 1},
		{Path: "root.txt", Size: 2},
		{Path: "afolder/a.txt", Size: 3},
		{Path: "afolder/b.txt", Size: 4},
	}
	meta := map[string]db.StagedMetadata{
		"root.txt": {Tags: []string{"tag1"}, Description: "desc"},
	}

	groups := groupStagedFiles(files, meta)

	if len(groups) != 3 {
		t.Fatalf("len(groups) = %d, want 3 (root, afolder, zzz-folder)", len(groups))
	}
	if groups[0].Name != "" || len(groups[0].Files) != 1 || groups[0].Files[0].Path != "root.txt" {
		t.Errorf("groups[0] = %+v, want the root group with root.txt", groups[0])
	}
	if groups[0].Files[0].Description != "desc" || !reflect.DeepEqual(groups[0].Files[0].Tags, []string{"tag1"}) {
		t.Errorf("groups[0].Files[0] = %+v, want metadata joined in", groups[0].Files[0])
	}
	if groups[1].Name != "afolder" || len(groups[1].Files) != 2 {
		t.Errorf("groups[1] = %+v, want afolder with 2 files", groups[1])
	}
	if groups[2].Name != "zzz-folder" || len(groups[2].Files) != 1 {
		t.Errorf("groups[2] = %+v, want zzz-folder with 1 file", groups[2])
	}
}

func TestGroupStagedFiles_UntaggedFileHasEmptyMetadata(t *testing.T) {
	files := []binpack.FileInfo{{Path: "a.txt", Size: 1}}
	groups := groupStagedFiles(files, map[string]db.StagedMetadata{})
	if len(groups) != 1 || len(groups[0].Files) != 1 {
		t.Fatalf("groups = %+v, want one group with one file", groups)
	}
	row := groups[0].Files[0]
	if row.Tags != nil || row.Description != "" {
		t.Errorf("row = %+v, want nil Tags and empty Description for an untagged file", row)
	}
}

func TestResolveSelectedPaths_DirectPathsAndFolderPrefixes(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel string) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("root.txt")
	mustWrite("vacation/a.jpg")
	mustWrite("vacation/b.jpg")
	mustWrite("work/report.pdf")

	got, err := resolveSelectedPaths(dir, []string{"root.txt", "vacation/"})
	if err != nil {
		t.Fatalf("resolveSelectedPaths: %v", err)
	}
	want := map[string]bool{"root.txt": true, "vacation/a.jpg": true, "vacation/b.jpg": true}
	if len(got) != len(want) {
		t.Fatalf("got = %v, want exactly %v", got, want)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected path %q in result", p)
		}
	}
}

func TestTemplates_ParseWithoutError(t *testing.T) {
	if _, err := templatesTestParse(); err != nil {
		t.Fatalf("parsing embedded templates: %v", err)
	}
}
```

Add this small helper at the bottom of the same file — it exists only so
the test above can reuse the exact `template.ParseFS` call `NewServer`
makes, without needing a live `*pgxpool.Pool` to construct a full `*Server`:

```go
func templatesTestParse() (*template.Template, error) {
	return template.ParseFS(templatesFS, "templates/*.html")
}
```

Add `"html/template"` to this file's imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/web/... -run 'TestGroupStagedFiles|TestResolveSelectedPaths|TestTemplates_ParseWithoutError' -v`
Expected: FAIL to compile — `groupStagedFiles` and `resolveSelectedPaths` don't exist yet. (`TestTemplates_ParseWithoutError` itself would currently pass against the existing templates, but the file won't compile until the other two functions exist.)

- [ ] **Step 3: Implement `internal/web/staging.go`**

```go
package web

import (
	"net/http"
	"sort"
	"strings"

	"varis/internal/binpack"
	"varis/internal/db"
)

// stagedFileRow is one staged file plus whatever metadata it currently
// carries in staged_metadata, for the dashboard listing.
type stagedFileRow struct {
	Path        string
	Size        int64
	Tags        []string
	Description string
}

// stagedFolderGroup is one top-level folder's worth of staged files (or,
// when Name is "", the files sitting directly in the staging root).
type stagedFolderGroup struct {
	Name  string
	Files []stagedFileRow
}

// groupStagedFiles groups files by their top-level folder (the first path
// segment), joining in tags/description from meta by path. The root group
// (files with no folder) always sorts first; folder groups after it sort
// alphabetically.
func groupStagedFiles(files []binpack.FileInfo, meta map[string]db.StagedMetadata) []stagedFolderGroup {
	groups := map[string]*stagedFolderGroup{}
	var order []string
	for _, f := range files {
		name := ""
		if idx := strings.IndexByte(f.Path, '/'); idx >= 0 {
			name = f.Path[:idx]
		}
		g, ok := groups[name]
		if !ok {
			g = &stagedFolderGroup{Name: name}
			groups[name] = g
			order = append(order, name)
		}
		m := meta[f.Path]
		g.Files = append(g.Files, stagedFileRow{Path: f.Path, Size: f.Size, Tags: m.Tags, Description: m.Description})
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i] == "" {
			return true
		}
		if order[j] == "" {
			return false
		}
		return order[i] < order[j]
	})
	out := make([]stagedFolderGroup, 0, len(order))
	for _, name := range order {
		out = append(out, *groups[name])
	}
	return out
}

// resolveSelectedPaths expands a form's checked selections into the exact
// set of staging-relative paths to apply metadata to. A selection ending
// in "/" is a folder header checkbox: it expands to every currently
// -staged file whose path starts with that prefix (a fresh ScanStaging, so
// a folder checked a moment ago before some of its files were burned just
// resolves to whatever's still there). Anything else is a single file's
// own path, taken as-is.
func resolveSelectedPaths(stagingDir string, selected []string) ([]string, error) {
	files, err := binpack.ScanStaging(stagingDir)
	if err != nil {
		return nil, err
	}
	var prefixes []string
	set := map[string]bool{}
	for _, sel := range selected {
		if strings.HasSuffix(sel, "/") {
			prefixes = append(prefixes, sel)
		} else {
			set[sel] = true
		}
	}
	for _, f := range files {
		for _, prefix := range prefixes {
			if strings.HasPrefix(f.Path, prefix) {
				set[f.Path] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	return out, nil
}

// stagedFileGroups scans staging and joins in current metadata, for both
// the dashboard's initial render and applyMetadata's re-rendered fragment.
func (s *Server) stagedFileGroups(r *http.Request) ([]stagedFolderGroup, error) {
	files, err := binpack.ScanStaging(s.stagingDir)
	if err != nil {
		return nil, err
	}
	meta, err := db.ListStagedMetadata(r.Context(), s.pool)
	if err != nil {
		return nil, err
	}
	return groupStagedFiles(files, meta), nil
}

func (s *Server) renderStagingFiles(w http.ResponseWriter, r *http.Request) {
	groups, err := s.stagedFileGroups(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "staging-files-fragment", groups)
}

// applyMetadata replaces tags/description for every selected staged file
// (individual paths and/or folder-prefix selections) with the submitted
// values, then re-renders the listing so the change is visible immediately.
func (s *Server) applyMetadata(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	paths, err := resolveSelectedPaths(s.stagingDir, r.Form["path"])
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tags := parseTags(r.FormValue("tags"))
	description := r.FormValue("description")
	for _, p := range paths {
		if err := db.UpsertStagedMetadata(r.Context(), s.pool, p, tags, description); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	s.renderStagingFiles(w, r)
}

// parseTags splits a comma-separated tags input into a trimmed,
// non-empty-entries-only slice — "family, ,2019" becomes [family 2019].
func parseTags(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
```

- [ ] **Step 4: Register the new route in `internal/web/server.go`**

Add, next to the other dashboard-area routes in `Routes`:

```go
	mux.HandleFunc("POST /staging/metadata", s.applyMetadata)
```

- [ ] **Step 5: Wire the listing into the dashboard's initial render**

In `internal/web/dashboard.go`, change `dashboardData` and `dashboard`:

```go
type dashboardData struct {
	StagedBytes int64
	StagedFiles []stagedFolderGroup
	MediaTypes  []db.MediaType
	Job         *jobView
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	staged, err := webdav.DirSize(s.stagingDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stagedFiles, err := s.stagedFileGroups(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	types, err := db.ListMediaTypes(r.Context(), s.pool)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := dashboardData{
		StagedBytes: staged,
		StagedFiles: stagedFiles,
		MediaTypes:  types,
		Job:         s.currentJobView(),
	}
	s.render(w, "dashboard", data)
}
```

(`render` itself is unchanged.)

- [ ] **Step 6: Add the fragment template and tag-chip styling**

In `internal/web/templates/dashboard.html`, replace:

```html
<p>Staged: {{.StagedBytes}} bytes.</p>
```

with:

```html
<p>Staged: {{.StagedBytes}} bytes.</p>
{{template "staging-files-fragment" .StagedFiles}}
```

Append this new define to the same file (`internal/web/templates/dashboard.html`):

```html
{{define "staging-files-fragment"}}
<div id="staging-files">
<form hx-post="/staging/metadata" hx-target="#staging-files" hx-swap="outerHTML">
{{range .}}
  <fieldset>
    {{if .Name}}
    <legend><label><input type="checkbox" name="path" value="{{.Name}}/"> {{.Name}}/</label></legend>
    {{else}}
    <legend>(root)</legend>
    {{end}}
    <table>
      <thead><tr><th></th><th>File</th><th>Size</th><th>Tags</th><th>Description</th></tr></thead>
      <tbody>
      {{range .Files}}
      <tr>
        <td><input type="checkbox" name="path" value="{{.Path}}"></td>
        <td>{{.Path}}</td>
        <td>{{.Size}}</td>
        <td>{{range .Tags}}<span class="tag">{{.}}</span>{{end}}</td>
        <td>{{.Description}}</td>
      </tr>
      {{end}}
      </tbody>
    </table>
  </fieldset>
{{end}}
  <label>Tags (comma-separated) <input type="text" name="tags"></label>
  <label>Description <input type="text" name="description"></label>
  <button type="submit">Apply to selected</button>
</form>
</div>
{{end}}
```

In `internal/web/templates/layout.html`, add a tag-chip style next to the
existing `.error` rule inside `{{define "head"}}`'s `<style>` block:

```css
  .tag { background: #eee; border-radius: 3px; padding: 0 4px; margin-right: 2px; font-size: 0.85em; }
```

- [ ] **Step 7: Run tests to verify everything passes**

Run: `go build ./... && go test ./... -v`
Expected: PASS — the three new `staging_test.go` tests pass, and
`TestTemplates_ParseWithoutError` confirms the new template markup parses
cleanly (this is also the only regression test in this project that
exercises `template.ParseFS` against the real embedded templates at all —
no earlier test did, so it now guards Task 6's template edit too).

- [ ] **Step 8: Commit**

```bash
git add internal/web/staging.go internal/web/staging_test.go internal/web/server.go internal/web/dashboard.go internal/web/templates/dashboard.html internal/web/templates/layout.html
git commit -m "$(cat <<'EOF'
Show staged files on the dashboard with bulk tag/description apply

Files are grouped by top-level folder; a folder's checkbox and individual file checkboxes both post as "path" values, resolved server-side against a fresh staging scan — no client-side JS needed to expand a folder selection.

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

### Task 6: Library — show tags/description in search results

**Files:**
- Modify: `internal/web/templates/library.html`

- [ ] **Step 1: Add a tags/description column**

In `internal/web/templates/library.html`, change the results table's header:

```html
<thead><tr><th>File</th><th>Size</th><th>Disc</th><th>Tags</th><th></th></tr></thead>
```

And the `search-rows` row template — replace:

```html
{{define "search-rows"}}
{{range .}}
<tr>
  <td>{{.OriginalPath}}</td>
  <td>{{.SizeBytes}}</td>
  <td>{{.DiskID}}</td>
  <td>
    <form hx-post="/retrieve" hx-target="#retrieve-status" hx-swap="innerHTML">
      <input type="hidden" name="file_id" value="{{.ID}}">
      <button type="submit">Get</button>
    </form>
  </td>
</tr>
{{end}}
{{end}}
```

with:

```html
{{define "search-rows"}}
{{range .}}
<tr>
  <td>{{.OriginalPath}}</td>
  <td>{{.SizeBytes}}</td>
  <td>{{.DiskID}}</td>
  <td>{{range .Tags}}<span class="tag">{{.}}</span>{{end}}{{if .Description}} <em>{{.Description}}</em>{{end}}</td>
  <td>
    <form hx-post="/retrieve" hx-target="#retrieve-status" hx-swap="innerHTML">
      <input type="hidden" name="file_id" value="{{.ID}}">
      <button type="submit">Get</button>
    </form>
  </td>
</tr>
{{end}}
{{end}}
```

No Go code changes are needed: `db.SearchFiles` (Task 3) already returns
`Tags`/`Description` on every `db.FileRecord`, and `library.go`'s handlers
already pass `[]db.FileRecord` through to this template untouched.

- [ ] **Step 2: Run tests to verify nothing broke**

Run: `go build ./... && go test ./... -race`
Expected: PASS — in particular, `TestTemplates_ParseWithoutError` (added in
Task 5) catches any template syntax mistake here too.

- [ ] **Step 3: Commit**

```bash
git add internal/web/templates/library.html
git commit -m "$(cat <<'EOF'
Show tags and description in Library search results

Claude-Session: https://claude.ai/code/session_013inRMp9YyQjW2XS1c6eedc
EOF
)"
```

---

## Plan Self-Review

**Spec coverage:**
- §2 (data model): Task 1 (schema), Task 2 (`staged_metadata` queries), Task 3 (`files` columns).
- §3 (dashboard listing, grouped by top-level folder, checkboxes, tags/description shown): Task 5.
- §4 (bulk apply, replace-not-merge semantics, stale-selection handling): Task 5 (`applyMetadata`/`resolveSelectedPaths`).
- §5 (carry-forward via `ConsumeStagedMetadata` at burn time): Task 4.
- §6 (Library search matches tags/description): Task 3 (query) + Task 6 (display).
- §7 (non-goals): nothing in this plan exceeds them — no freeform key/value metadata, no persistent cascading folder tags, no nested folder tree, no post-burn editing, no per-tag add/remove.
- §8 (testing): DB-backed tests for every new query (Tasks 1-3), a fake-Cataloger test for the burn-side fold (Task 4), and pure-logic tests for the grouping/selection-resolution helpers (Task 5) — matching this project's established convention (confirmed against `internal/web/handlers_test.go`'s existing comment) that DB-touching web handlers themselves aren't unit-tested at this project's scale; only their pure, extractable logic is.

**Placeholder scan:** no TBD/TODO or vague instructions remain — Task 4's tests call the actual existing helper (`fakeExecutorForHappyPath(device)`, confirmed against `internal/burn/pipeline_test.go`) by its real name and signature.

**Type consistency:** `db.StagedMetadata{Tags, Description}` (Task 2) is used identically in Task 5's `groupStagedFiles` (`meta[f.Path]` yields a `db.StagedMetadata`). `Cataloger.ConsumeStagedMetadata`'s signature (`(tags []string, description string, err error)`, Task 4) matches exactly how `commitDisc` calls it and how every fake implements it. `db.FileRecord.Tags`/`.Description` (Task 3) match the field names used in Task 4's `commitDisc` edit and Task 6's template. `stagedFolderGroup`/`stagedFileRow` (Task 5) are used consistently between `groupStagedFiles`, `stagedFileGroups`, and the template range expressions.
