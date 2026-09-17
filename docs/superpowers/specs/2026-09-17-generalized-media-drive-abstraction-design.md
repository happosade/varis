# Generalized Media/Drive Abstraction — Design Spec

## 1. Overview

Replace the burn/retrieve pipeline's hardcoded assumption that all media
is optical and burned via `wodim` against one fixed device path, with a
small, general model: each media type declares its own disk-ID prefix and
"write kind" (optical burn, or plain filesystem copy — covering RDX
cartridges and USB drives, both of which mount as an ordinary filesystem),
and the target device/mount path is supplied per burn and per retrieval
instead of fixed at startup. This replaces `mediaPrefix`'s hardcoded Go
switch (`internal/burn/pipeline.go`) entirely, and builds directly on the
dry-run burn mode plan's `copyFile` helper and its precedent of an
abstract, opaque `src`/target path.

Depends on: the dry-run burn mode plan (`2026-09-17-dry-run-burn-mode.md`)
— execute that one first; this plan's burn/retrieve signature changes
land on top of the ones it already made.

## 2. Data Model

```sql
ALTER TABLE media_types ADD COLUMN IF NOT EXISTS id_prefix TEXT NOT NULL DEFAULT 'DISC';
ALTER TABLE media_types ADD COLUMN IF NOT EXISTS write_kind TEXT NOT NULL DEFAULT 'optical';
```

`write_kind` is `"optical"` or `"filesystem"`, matching this codebase's
existing string-enum convention (`disks.role` is `"data"`/`"parity"` the
same way — no Postgres `CHECK` constraint, validated in Go at the point
media types are added). The two seeded defaults (`BD-R`, `BD-R DL`) get
`id_prefix` values matching today's hardcoded switch output (`"BD"`,
`"BDDL"`) and `write_kind = "optical"`, so existing behavior for them is
unchanged after migration.

The Config page's "Add media type" form gains two fields: ID prefix
(text) and write kind (a `<select>` of `optical`/`filesystem`).

## 3. Removing `mediaPrefix`

`planJob` currently calls `mediaPrefix(opts.MediaType)`, a hardcoded
switch with no database access. It's replaced by a new `Options.IDPrefix
string` field, resolved once by the web layer (already the layer that
resolves `CapacityBytes` via a media-type lookup before calling
`burnMgr.Start`) and passed straight through — `planJob` needs no
database access of its own for this. `mediaPrefix` and its switch are
deleted.

`Options` also gains `WriteKind string` and `TargetPath string`,
resolved/supplied the same way (see Section 4).

## 4. Burn Pipeline: Write-Kind Dispatch and Per-Burn Target Path

`burn.Manager` drops its constructor-time `device` field/parameter
entirely — a fixed device made sense when there was exactly one kind of
media and one drive; once the target varies per burn (a different USB
mount point, a different media type entirely), fixing it at `Manager`
construction time doesn't. `NewManager` becomes
`NewManager(cat, ex, stagingDir, spoolDir, dryRunDir string) *Manager`.

`runDisc`'s burn/verify block, which the dry-run plan already turned into
a `job.Options.DryRun` branch, gains a second dimension — `WriteKind`,
checked only in the non-dry-run branch:

- **`DryRun` (checked first, same as before):** copy the ISO to
  `dryRunDir`, as already implemented. Unaffected by `WriteKind`.
- **`WriteKind == "optical"`:** `BurnISO`/`VerifyBurn` against
  `job.Options.TargetPath` (previously `m.device`) — identical logic,
  just reading the target from the job instead of the Manager.
- **`WriteKind == "filesystem"`:** copy the ISO to `job.Options.TargetPath`
  via the same `copyFile` helper the dry-run plan added, then call
  `VerifyBurn` **unchanged** — it already just does `HashFile` on
  whatever path it's given and runs `par2verify` against the same local
  spool tar copy either way, so it works identically against a plain
  filesystem path with zero new code. Additionally write a
  `<diskID>.sha256` file next to the copied ISO, formatted as standard
  `sha256sum -c` input (`<hex-hash>  <diskID>.iso\n`), so the copy can be
  independently verified later without trusting the app.

The dashboard's Burn form gains a "Device / target path" text input.
It's pre-filled with `cfg.OpticalDevice` when the selected media type's
`write_kind` is `"optical"` (matching today's single-drive default) and
left blank for `"filesystem"` media types, since a USB/RDX mount point
can't be defaulted sensibly — but it's always editable, since even the
"optical" default can be wrong (a second drive, a non-standard device
node).

## 5. Retrieval: Per-Retrieval Target Path

`retrieve.Manager` drops its constructor-time `device` field the same
way. `NewManager` becomes
`NewManager(cat, ex, retrievedDir, scratchDir, dryRunDir string) *Manager`.

`ReadDisk` gains a `targetPath string` parameter:
`ReadDisk(ctx context.Context, targetPath string) error`. Its existing
dry-run check (from the dry-run plan) takes priority — if
`disk.IsDryRun`, `targetPath` is ignored and the dry-run path is used, as
already implemented, since nothing needs inserting for a dry run either
way. Otherwise `targetPath` is used directly as `readFrom`'s `src` — it
was already an opaque path as far as `readFrom` is concerned (a physical
device, a reconstructed image, or a dry-run ISO, per the dry-run plan),
and a filesystem-copy media type's target is just another plain file, so
`ExtractFromDisc`'s `xorriso -indev` call needs no new branching at all
to support it.

`ReadReconstructionDisc` gains the identical `targetPath string`
parameter for the same reason — a group's surviving members could
individually be on different physical media (unlikely in practice for
one group, but the mechanism doesn't assume otherwise), so each "insert
this disc" step in the reconstruction flow asks for its own target path
the same way `ReadDisk` does; its existing `IsDryRun` check (from the
dry-run plan) still takes priority per member.

The Library's "insert disc, Read Disk" button and the reconstruction
flow's per-member "insert X, Read" buttons each gain a target-path input,
pre-filled the same way the Burn form's is (from the disc's own recorded
`media_type`'s usual device, via a lookup already available at that
point).

## 6. Web UI

- **Config page:** "Add media type" form gains ID-prefix and write-kind
  fields; the media type list table shows both new columns.
- **Dashboard:** Burn form gains the target-path input (Section 4).
- **Library:** the retrieval status fragment's "insert disc, Read Disk"
  and reconstruction "insert X, Read" forms each gain a target-path
  input (Section 5).

## 7. Error Handling & Testing

- An invalid/unreachable `TargetPath` fails exactly like a bad
  `/dev/sr0` fails today — `BurnISO`/`copyFile` returns an error,
  `runDisc` returns it, the job goes `StateFailed`, staging is untouched,
  retry is available. No new error-handling code needed; this already
  falls out of `TargetPath` simply replacing what `m.device` used to be
  at the one place it's read.
- `write_kind` validated at the Config page (`addMediaType`) to be one of
  the two known values, returning 400 otherwise — mirroring the existing
  `capacity_bytes > 0` validation already there.
- Burn-pipeline tests: a `write_kind = "filesystem"` test proves
  `copyFile` (not `BurnISO`) is used, the target file exists, and a
  `<diskID>.sha256` file exists next to it with content matching
  `HashFile`'s own output format.
- Retrieve-pipeline tests: a fake-catalog test proves `ReadDisk`/
  `ReadReconstructionDisc` read from the caller-supplied `targetPath`
  rather than any fixed path, for a non-dry-run disc; the existing
  dry-run tests (from the dry-run plan) continue to prove the
  `IsDryRun`-takes-priority behavior unchanged.
- Every `burn.NewManager`/`retrieve.NewManager` call site across the
  module (both packages' own tests, `internal/web/handlers_test.go`,
  `internal/retrieve/integration_test.go`, `cmd/archive-core/main.go`)
  needs its `device` argument dropped, mirroring the sweep the dry-run
  plan already had to do for its own signature change.

## 8. Non-Goals

- Tape, network-share, or any write mechanism beyond optical burn and
  plain filesystem copy.
- Persisting each disc's actual target path in the catalog, or
  auto-detecting a plugged-in drive's mount point — the operator
  supplies the path each time, informed by the UI's pre-filled default
  and their own knowledge of what's plugged in.
- Any change to reconstruction's XOR logic or group semantics — only
  *where* each member is read from changes, per Section 5, identical in
  spirit to the dry-run plan's own non-goal there.
