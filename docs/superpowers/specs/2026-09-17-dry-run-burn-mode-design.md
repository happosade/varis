# Dry-Run Burn Mode — Design Spec

## 1. Overview

A "dry run" burn: instead of writing an ISO to real optical media, the
pipeline copies the finished ISO to a dedicated directory (mounted over
WebDAV for inspection) and catalogs the disc exactly as it would a real
burn — same disk ID scheme, same file records, searchable in the Library
the same way — but flagged so it's never mistaken for a physical disc,
and deletable (ISO + catalog rows together) once you're done with it.

This is scoped independently of the separate, larger "generalized
media/drive abstraction" idea (RDX cartridges, USB drives, per-media-type
write mechanisms) raised in the same conversation — that's tracked in
[Section 9](#9-todo--future-work) as explicit future work, not part of
this spec. Dry-run mode needs no schema changes to `media_types` and
works with today's optical-only pipeline.

## 2. Data Model

```sql
ALTER TABLE disks ADD COLUMN IF NOT EXISTS is_dry_run BOOLEAN NOT NULL DEFAULT false;
```

No other schema changes. A dry-run disc's row looks exactly like a real
one (same `id`, `media_type`, `parity_percent`, `group_id`/`slot_index`
for cross-disc-parity groups, `iso_hash`), with `is_dry_run = true`.

## 3. Burn Pipeline

`burn.Options` gains `DryRun bool`. `runDisc`'s existing per-disc sequence
(`PLANNING → PACKING → PARITY → ISO → BURNING → VERIFYING → DONE`) changes
only at the BURNING/VERIFYING step:

- **Normal burn:** `BurnISO` (writes to the physical device via `wodim`)
  then `VerifyBurn` (re-reads the device, hashes, compares, `par2verify`s),
  as today.
- **Dry run:** skip both. Copy the already-built, already-hashed ISO file
  from spool to `<dryRunDir>/<diskID>.iso`. Nothing to verify against —
  the file just written is the file that gets cataloged, with no physical
  round trip to introduce a discrepancy.

Everything before this point (bin-packing, tar, par2, TOC, ISO
authoring, and — for cross-disc-parity groups — `buildParityPayload`
reading sibling data discs' ISOs from spool) is completely unaffected.
A dry-run cross-disc-parity group works the same way a real one does:
one parity disc's dry-run ISO per group, computed by XOR-ing the group's
data discs' ISOs exactly as `buildParityPayload` already does.

`commitDisc` runs unchanged for a dry-run disc except for setting
`db.Disk.IsDryRun = job.Options.DryRun`: it inserts the disk row, inserts
each file's record (with staged metadata folded in, same as any burn —
see the staged-file-metadata feature), and removes the files from
staging. A dry-run "burn" is, from the catalog's point of view, exactly
as final as a real one — the only thing that didn't happen is the actual
write to physical media.

`burn.NewManager` gains a `dryRunDir string` parameter (alongside the
existing `stagingDir`/`spoolDir`/`device`), used only for this copy step.

## 4. Retrieval

`retrieve.Manager.readFrom(ctx, job, src)` already reads from an abstract
`src` — today either the physical device (`ReadDisk`) or a reconstructed
image file (`ReadReconstructionDisc`). A dry-run disc is a third kind of
`src`: its ISO's path under the dry-run directory.

`ReadDisk` looks up the target disc via `m.cat.GetDisk` before reading;
if `disk.IsDryRun`, it calls `readFrom` with
`filepath.Join(m.dryRunDir, job.DiskID+".iso")` instead of `m.device` —
no "insert disc" step needed, since nothing needs inserting. Everything
downstream of `readFrom` (TOC check, tar/par2 extraction, `par2verify`,
file extraction into `RetrievedDir`) is unchanged; it already treats its
`src` opaquely.

`retrieve.NewManager` gains a `dryRunDir string` parameter, matching
`burn.NewManager`'s.

`ReadReconstructionDisc` needs the identical treatment, for a case easy
to overlook: a dry-run disc can be one of a group's *surviving* members
during reconstruction of some other (real) missing disc, not only the
disc actually being recovered. It already calls `m.cat.GetDisk` per
member to decide *how* to read it (`Role == "parity"` extracts the
payload; otherwise it does a raw capacity-bounded read) — it needs the
same `IsDryRun` check to decide *where* to read it from, resolving `src`
once (device or dry-run path) and using it in both of those existing
branches instead of the hardcoded `m.device`.

If a dry-run disc's ISO has been deleted (see [Section 5](#5-inspection--cleanup))
but its catalog rows somehow still exist — which shouldn't happen given
Section 5's all-or-nothing delete, but is worth being explicit about —
`ReadDisk` simply fails with a normal "file not found" error reading the
missing ISO, routed through the existing failure handling like any other
unreadable disc. No special-casing needed.

## 5. Inspection & Cleanup

- **New config:** `DRYRUN_DIR` env var (default `/data/dryrun`), added to
  `internal/config/config.go` alongside the existing directory vars.
- **New WebDAV mount:** `/webdav/dryrun/` → `DRYRUN_DIR`, registered in
  `cmd/archive-core/main.go` next to the staging/retrieved mounts. This is
  a separate directory from `SpoolDir`, not a new view onto it — spool
  already accumulates internal working files (tar, par2, TOC) that aren't
  meant to be user-facing; the dry-run directory holds only finished,
  inspectable ISOs.
- **New page, `GET /dryruns`:** lists every disc with `is_dry_run = true`
  (disk ID, media type, created-at, a direct link to its file under
  `/webdav/dryrun/`), each with a **Delete** button.
- **`POST /dryruns/delete`** (disk ID in the form body): deletes the ISO
  file from `DRYRUN_DIR`, then the disc's `files` rows, then its `disks`
  row (in that order, so a failure partway through never leaves a
  `files` row pointing at a deleted `disks` row) — all three, together,
  per your explicit answer during brainstorming. A new
  `db.DeleteDryRunDisc(ctx, pool, diskID) error` handles the two DB
  deletes in one function; the handler deletes the file itself first
  (the DB rows are cheap to reconstruct-by-re-running the dry run if the
  file delete fails and this leaves them stale, whereas losing the file
  first and failing the DB delete would leave a dangling reference —
  so file-then-DB is the safer order here, the opposite of the general
  "commit only after every step below it succeeds" pattern used
  elsewhere, because deletion's failure mode is inverted from creation's).
- `db.DeleteDryRunDisc` only ever targets rows where `is_dry_run = true`
  — it will not delete a real disc's catalog rows even if called with a
  real disc's ID by mistake (e.g. a stale link).

## 6. UI

- **Dashboard's Burn form** gets a "Dry run" checkbox next to the
  existing parity/compression/cross-disc-parity controls.
- **Jobs page:** a job with `Options.DryRun` shows a visible "DRY RUN"
  badge, and the "insert next blank disc, continue" prompt between
  multi-disc discs is reworded to "Continue (dry run — no disc needed)".
- **Library search results and the disk-cover page** (`GET /cover/{diskID}`)
  show the dry-run flag on any row/cover belonging to such a disc, so
  it's never ambiguous whether a given disc is real.
- **New nav link** to `/dryruns` alongside Dashboard/Library/Config.

## 7. Error Handling & Testing

Following this project's existing conventions:

- The dry-run copy step can fail (disk full, permissions) exactly like
  `BurnISO` can today — same `StateFailed` handling, same "staged files
  are never touched until the disc reaches DONE" guarantee, so a failed
  dry-run copy is retriable exactly like a failed real burn.
- Unit tests use the existing `execx.FakeExecutor`/`FakeCataloger`
  patterns: a dry-run job's fake executor never sees a `wodim` call, the
  ISO ends up at the expected dry-run path, and the committed `db.Disk`
  has `IsDryRun = true`.
- `retrieve` package tests extend the existing `fakeCatalog`/fake-executor
  patterns with a dry-run disc fixture, proving `ReadDisk` reads from the
  dry-run path rather than the fake "physical device" file.
- `db.DeleteDryRunDisc` gets a DB-backed test (skips without
  `DATABASE_URL`, matching every other `internal/db` test): deletes a
  dry-run disc's rows, confirms a real disc's rows are untouched, and
  confirms it refuses (no-ops, not errors — matches `ConsumeStagedMetadata`'s
  "never existed" pattern) when given a real disc's ID.

## 8. Non-Goals

- The generalized media/drive abstraction (RDX, USB, per-media-type
  write mechanisms, DB-driven ID prefixes) — a separate spec, see
  Section 9.
- Auto-converting a dry-run disc into a real one later (e.g. "burn this
  dry-run ISO for real now") — out of scope; re-run a real burn from the
  same staged files if the staged files are still present, or accept
  that a dry run whose files were already committed/removed from staging
  needs a fresh burn planned from the dry-run ISO's contents manually.
- Any change to reconstruction's *logic* (how members are supplied, when
  reconstruction becomes possible, how the missing disc's image is
  rebuilt) — dry-run discs participate in groups identically to real
  ones there. The one place reconstruction *does* need to be dry-run
  -aware is mechanical, not logical: `ReadReconstructionDisc` already
  resolves *how* to read a member disc based on its `Role` (extract vs.
  raw read); it needs the identical `IsDryRun`-based resolution of
  *where* to read it from that `ReadDisk` gets, since a dry-run disc
  can be a surviving group member just as easily as it can be the disc
  being recovered. See Section 4.

## 9. TODO / Future Work

- **Generalized media/drive abstraction:** replace `mediaPrefix`'s
  hardcoded Go switch (`internal/burn/pipeline.go`) with a real
  per-media-type ID prefix stored in `media_types`, and generalize the
  single global `OPTICAL_DEVICE` + `wodim`-only write path into a
  pluggable "how do we write to this media type" concept, so genuinely
  different removable media (RDX cartridges, USB drives — a plain
  filesystem copy, not an optical burn) can be added as media types
  without new Go code, with disk IDs reflecting the media type (e.g.
  `RDX:0010`, `USB:0002`) via that stored prefix rather than a hardcoded
  switch. Scoped as its own spec/plan, to follow this one.
