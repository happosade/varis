# Staged File Listing & Metadata — Design Spec

## 1. Overview

Two related additions to the dashboard's To-Archive section, tracked as
TODO items in [the main design spec](2026-09-16-optical-disc-archive-design.md#9-todo--future-work):

1. Replace the plain "Staged: N bytes" line with an actual listing of
   staged files (path, size), so the user can review what's about to be
   burned.
2. Let the user attach tags and a description to individual files,
   whole top-level folders, or an arbitrary selection of both, before
   burning — persisted so the Library's search can match on them too,
   not just original path.

Both build on `binpack.ScanStaging`, which already walks the staging
directory and returns every file's relative path and size — the listing
is mostly a matter of rendering that, and the metadata feature hangs off
the same listing as its selection UI.

## 2. Data Model

```sql
CREATE TABLE staged_metadata (
    path        TEXT PRIMARY KEY,   -- relative to the staging root, same
                                    -- as binpack.FileInfo.Path
    tags        TEXT[] NOT NULL DEFAULT '{}',
    description TEXT NOT NULL DEFAULT '',
    updated_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

ALTER TABLE files ADD COLUMN tags TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE files ADD COLUMN description TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_files_tags ON files USING gin (tags);
```

`staged_metadata` holds metadata only for files currently sitting in
staging. A row is created or replaced when the user applies tags/a
description to a path, and consumed (read + deleted) the moment that
file is actually burned — see §5. It is keyed by path alone, so it
naturally survives a file being removed from the WebDAV mount and
re-added at the same path (rare, but harmless either way: the old
metadata just applies again).

`files.tags`/`files.description` are the permanent, searchable home for
metadata once a file is archived — the same shape as `staged_metadata`,
copied over at burn time, never modified afterward (editing metadata on
an already-burned file is out of scope — see §7).

## 3. Dashboard: Staged File Listing

`GET /` (and a refreshable fragment, `GET /staging/files`, in the same
HTMX-polling style already used for `/jobs`) renders the staging
directory as a table instead of a byte count:

- Grouped by **top-level folder only** (one level of nesting in the
  *display*, not a recursive tree) — a header row per top-level
  directory name (the first path segment), with its own checkbox that
  selects/deselects every file whose path starts with that segment,
  however deeply nested underneath it. Files directly in the staging
  root (no folder) are listed ungrouped above any folder sections.
- Each file row: a checkbox, its path, its size, and its current tags
  (rendered as small chips) and description (truncated) if any are set
  in `staged_metadata`.
- A running total byte count stays at the top of the section (the one
  piece of the current UI that's kept as-is).

This uses `binpack.ScanStaging(stagingDir)` for the file list and a new
`db.ListStagedMetadata(ctx, pool) (map[string]StagedMetadata, error)`
(one query, not N+1) to join in tags/description per path for display.

## 4. Applying Metadata

Below the listing, a small form: a text input for tags (comma
-separated, e.g. `family, videos, 2019`) and a textarea for description,
plus an "Apply to selected" button. It's disabled (or a no-op) when
nothing is checked.

Submitting `POST /staging/metadata` with the checked paths (repeated
`path` form values) and the tag/description fields **replaces** —
not merges — `staged_metadata` for every selected path: each path's row
is upserted with exactly the submitted tags and description, regardless
of what it held before. This is the simplest predictable behavior when a
selection mixes files that may already carry different tags; there's no
per-file "add a tag without touching the rest" affordance in this
iteration (see §7). Submitting with empty tags and an empty description
is a valid way to clear a file's metadata.

The handler re-renders the `/staging/files` fragment on success, same as
every other HTMX POST in this codebase (`startBurn`, `startRetrieve`,
etc.), so the listing reflects the new tags immediately.

**Stale selection:** a path checked in the browser might no longer exist
in staging by the time the form posts (e.g. burned by a concurrent
request, or removed via WebDAV) — `staged_metadata` has no foreign key
to any "staged files" table (there isn't one; staging is a directory,
not a DB-tracked entity), so upserting metadata for a path that's since
disappeared just creates a `staged_metadata` row that never gets
consumed. This is harmless (it's cleaned up the next time anything is
ever staged and burned at that exact same relative path, or simply never
referenced again) and not worth guarding against explicitly.

## 5. Carrying Metadata Into the Permanent Record

`burn.Cataloger` gains one new method:

```go
ConsumeStagedMetadata(ctx context.Context, path string) (tags []string, description string, err error)
```

Implemented as a single round trip:

```sql
DELETE FROM staged_metadata WHERE path = $1 RETURNING tags, description
```

If no row exists for that path (never tagged), this returns empty
values, not an error — `pgx.ErrNoRows` is treated as the empty case, not
propagated.

`burn.pipeline.commitDisc` calls this for each file in the bucket, right
before building the `db.FileRecord` to insert, folding the result into
`FileRecord.Tags`/`FileRecord.Description`. This keeps the metadata
lookup exactly where every other per-file piece of burn-time state
(the hash) is already computed, in the same loop, one file at a time.

## 6. Library Search

`db.SearchFiles`'s query extends from matching only `original_path` to
also matching `tags` and `description`:

```sql
SELECT id, disk_id, original_path, size_bytes, file_hash, tags, description
FROM files
WHERE original_path ILIKE '%' || $1 || '%'
   OR description ILIKE '%' || $1 || '%'
   OR EXISTS (SELECT 1 FROM unnest(tags) t WHERE t ILIKE '%' || $1 || '%')
ORDER BY similarity(original_path, $1) DESC
LIMIT 50
```

Same search box, same endpoint (`GET /search?q=`) — searching "family
videos 2019" now matches a file tagged that way even if the tag never
appears in its filename, per the original motivating example. The
Library results table gains a tags/description column so a match is
visibly explained.

## 7. Non-Goals

Explicitly out of scope for this iteration:

- Arbitrary freeform key/value metadata — tags + description only.
- Persistent, cascading folder-level tags (tagging a folder is a
  one-time bulk-apply to whatever's in it right now, not a standing rule
  that applies to files added later).
- Nested folder trees beyond one level of grouping in the listing UI.
- Editing tags/description on a file that's already been burned.
- Per-tag add/remove on an existing selection (apply always replaces).

## 8. Testing

Following this project's existing conventions throughout:

- `internal/db`: DB-backed tests (skip gracefully without
  `DATABASE_URL`, matching every other `internal/db` test) for
  `ListStagedMetadata`, upserting `staged_metadata`,
  `ConsumeStagedMetadata` (including the never-tagged/no-row case), and
  `SearchFiles` matching on tags/description.
- `internal/burn`: a fake-`Cataloger` test proving `commitDisc` folds
  `ConsumeStagedMetadata`'s result into the inserted `FileRecord`, and
  that a file with no staged metadata still commits cleanly with empty
  tags/description.
- `internal/web`: handler tests for the `/staging/files` fragment
  (rendering grouped-by-top-level-folder listing data) and
  `POST /staging/metadata` (selected paths → upserted rows → re-rendered
  fragment; empty selection is a no-op).
