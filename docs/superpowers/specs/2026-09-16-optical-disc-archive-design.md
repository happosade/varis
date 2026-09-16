# Optical Disc Archive — Design Spec

## 1. Overview

A self-hosted system for archiving large amounts of data onto optical discs
(Blu-ray/DVD) with built-in redundancy, a searchable catalog, and a simple
web UI. Files are dropped into a WebDAV "to-archive" folder, burned to disc
in protected sets, cataloged in Postgres, and later retrieved by searching
the catalog and inserting the disc the system names.

**Goals:** simple, single-user, home-scale archival with strong protection
against both partial disc bit-rot and total disc loss.

**Non-goals:** see [Section 8](#8-non-goals).

## 2. Architecture & Deployment

- **Host requirement:** `archive-core` runs on a **Linux host** with real
  Docker (device passthrough for the optical drive via `--device /dev/sr0`).
  macOS is a **client only** — it mounts the WebDAV shares and opens the web
  UI over the network; it never touches the drive directly.
- **Docker Compose stack**, two services:
  - `archive-core` (Go): a single binary/container serving the web UI,
    the WebDAV server, and the burn/retrieve worker — these share one
    in-process state machine and disk access, so splitting them into
    separate services buys nothing.
  - `archive-db` (official Postgres image): metadata store.
- **Volumes:**
  - `/data/staging` — WebDAV "To-Archive" drop zone.
  - `/data/spool` — ISOs, parity files, TOC, printable covers (kept until
    a burn is verified DONE, then cleaned up).
  - `/data/retrieved` — WebDAV output for restored files.
  - `/dev/sr0` — passed through via `devices:` in compose (Linux only).
- **No authentication.** Trusted home LAN, single user.
- **Go version:** latest stable (1.26+).
- **Routing:** stdlib `net/http` using Go 1.22+ method+wildcard patterns
  (e.g. `mux.HandleFunc("POST /burn", ...)`). No chi/gorilla — the route
  count (~10) doesn't justify a router dependency.
- **System binaries baked into the image:** `par2cmdline` (parity),
  `xorriso` (ISO authoring), `wodim` (burn — preferred over `growisofs` as
  the more actively maintained cdrtools successor).

## 3. Data Model (PostgreSQL)

```sql
CREATE TABLE media_types (
    name VARCHAR(30) PRIMARY KEY,      -- 'BD-R', 'BD-R DL', ...
    capacity_bytes BIGINT NOT NULL
);
-- seeded: BD-R (25,025,314,816), BD-R DL (50,050,629,632)
-- user-editable via the Config page — more can be added later

CREATE TABLE disk_groups (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    burn_job_id UUID NOT NULL,          -- ties groups from one "burn" click together
    group_size INT NOT NULL,            -- default 10, configurable per burn
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE disks (
    id VARCHAR(50) PRIMARY KEY,         -- e.g. 'BD:0007'
    media_type VARCHAR(30) REFERENCES media_types(name),
    parity_percent SMALLINT NOT NULL,   -- intra-disc par2 redundancy chosen for this burn
    group_id UUID REFERENCES disk_groups(id),   -- NULL if cross-disc parity wasn't used
    role VARCHAR(10) NOT NULL DEFAULT 'data',   -- 'data' | 'parity'
    slot_index SMALLINT,                -- position within the group (XOR ordering)
    iso_hash VARCHAR(256),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE files (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    disk_id VARCHAR(50) REFERENCES disks(id),
    original_path TEXT,
    size_bytes BIGINT,
    file_hash VARCHAR(256),
    archived_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_files_path ON files USING gin (original_path gin_trgm_ops);
```

## 4. Burn Pipeline

**Trigger:** "Burn" button on the dashboard, with three inputs:
media type (dropdown), **parity percentage** (numeric, default 10%,
e.g. 5% for low-value data, 25%+ for critical data), and a compression
checkbox (off by default). A fourth control, "add cross-disc recovery
disc" (checkbox + group size, default 10), is described in
[Section 5](#5-cross-disc-parity-raid5-style).

**State machine**, one job at a time (single drive, no concurrency needed):

```
STAGED → PLANNING → PACKING → PARITY → ISO → BURNING → VERIFYING → DONE (per disc)
```

1. **PLANNING (bin-packing):** Scan `/data/staging` recursively. Compute
   the target data size per disc from the chosen parity%:
   `target ≈ capacity / (1 + parity%)`, so that `data + par2(data) ≈
   capacity`. Greedily bucket files into disc-sized sets against that
   target. If the staged data needs more than one disc, all buckets are
   planned upfront so the UI can say "this will take N discs" — but discs
   are burned **one at a time, sequentially**, prompting "insert next
   blank disc, click continue" between each (single drive).
2. **PACKING:** `tar` the bucket (preserving relative paths/mtimes). If
   compression is checked, pipe through `gzip` (stdlib `compress/gzip`)
   before parity — no external dependency needed.
3. **PARITY:** Run `par2create` via `os/exec` with the chosen redundancy
   percentage.
4. **TOC:** Write `TOC.json` (disk ID, media type, parity%, group info if
   any, file list with sizes/hashes, created-at) — the on-disk header,
   LTO-FS style.
5. **ISO:** `xorriso` wraps the tar/gzip payload + par2 files + `TOC.json`
   into an ISO in `/data/spool`.
6. **BURNING:** `wodim` writes the ISO to `/dev/sr0`.
7. **VERIFYING:** Re-read the burned disc, hash it, compare to the ISO's
   SHA256. Also run `par2verify` against the on-disc files — this catches
   partial corruption even where a gross hash mismatch wouldn't say what's
   wrong.
8. **DONE:** Commit disk + file rows to Postgres, generate the printable
   HTML cover (disk ID, short description, QR code linking to
   `archive://disk/<id>`), mark the step complete, move to the next disc
   in the batch (back to PACKING) or finish.

**Disk cover:** plain HTML page with print-friendly CSS (disk ID, short
description, QR code) — printed or saved-as-PDF via the browser's native
print function. No PDF generation library needed.

## 5. Cross-Disc Parity (RAID5-style)

An opt-in second redundancy layer, independent of intra-disc par2, that
protects against losing an **entire disc**: if any one disc in a group of
N is destroyed, its data is reconstructed from the other N-1 members
(data discs + one parity disc) — the same principle as RAID5.

- **Opt-in per burn** (checkbox), since it costs one whole extra disc of
  media and burn time per group.
- **Grouped, default group size 10** (configurable): every 10 data discs
  in a batch get their own parity disc, so protection scales with batch
  size. A 200GB/10-disc batch → 1 group → 1 parity disc. A 50-disc batch
  → 5 groups → 5 parity discs, keeping any single loss per group
  recoverable.
- **Computing the parity disc:** each data disc's ISO image in a group is
  zero-padded to the media's fixed capacity, then XOR'd byte-for-byte
  across all data discs in the group to produce the parity disc's image.
  The parity disc is wrapped in its own small ISO (TOC noting `group_id`
  and the slots it protects) and burned like any other disc, using the
  same PACKING→BURN→VERIFY steps — its "data" is the XOR result instead
  of a tar.
- **Reconstruction**, only invoked when a disc is confirmed lost or fails
  verification:
  1. On the Library page, a failed "Get" (or a disc you mark lost
     outright) offers "Reconstruct from group".
  2. Walks you through inserting every *other* disc in that group (data
     discs + the parity disc), one at a time via the manual "Read Disk"
     button — each is `par2verify`'d individually first.
  3. Once all N-1 others are read, XOR them together to reconstruct the
     missing disc's padded image.
  4. The requested file is extracted straight from the reconstructed
     image to `/data/retrieved`.

Normal retrieval (the common case) never touches this machinery unless
the specific disc named by a "Get" request fails to read or verify.

## 6. Web UI & Retrieval

**Pages** (Go `html/template` + HTMX):

- **Dashboard (`/`)** — staging folder size, media type dropdown,
  parity% input, compression checkbox, cross-disc-parity toggle + group
  size, "Burn" button.
- **Jobs (`/jobs`)** — HTMX-polled progress through the state machine per
  disc, "insert next disc, continue" prompts for multi-disc batches, link
  to each disc's printable HTML cover once DONE.
- **Library (`/library`)** — search box (`GET /search?q=`) against the
  trigram index; results show filename + required disk ID + "Get" button.
- **Config (`/config`)** — manage the `media_types` list (add/edit name +
  capacity).

**Normal retrieval flow:**

1. `GET /search?q=kotivideo` → rows: filename, size, `disk_id` (e.g.
   `BD:0007`), "Get" button.
2. Click "Get" → job state `AWAITING_MEDIA: BD:0007`, UI shows "insert
   disk BD:0007".
3. You insert it, click **"Read Disk"** (manual — no auto-detection).
4. Backend reads `TOC.json`, confirms the disk ID matches, runs
   `par2verify` (repairing minor bit-rot in place where possible), copies
   the requested file to `/data/retrieved`.
5. If verification fails outright, and the disc belongs to a group, offer
   the reconstruction flow from Section 5; otherwise report failure.
6. UI polls `/jobs` (or SSE) until "Ready in WebDAV".

## 7. Error Handling & Testing

**Error handling:**
- Any pipeline step failure (par2create, xorriso, burn write, verify
  mismatch) marks the job `FAILED` with the error shown on the Jobs page.
  Staged files are **never deleted or marked archived until a disc
  reaches DONE** — a failed burn just means retry with a fresh blank
  disc; nothing is lost.
- Before starting a job, check `/data/spool` has enough free space for
  the working set (roughly 2–3x one disc's capacity per in-flight disc).
- Single global job lock — only one burn *or* one retrieval/reconstruction
  runs at a time (matches the single-drive reality), so no job queue
  library is needed, just an in-memory state machine guarded by a mutex.

**Testing:** Real optical drives and `par2`/`xorriso`/`wodim` can't run in
CI, so:
- Unit-test the pure logic with normal Go tests: bin-packing math,
  parity%→bucket-size sizing, XOR reconstruction, TOC.json
  generation/parsing, search query building.
- Wrap `os/exec` calls behind a small interface so the state machine's
  *transitions* can be tested with a fake executor (failures move to
  FAILED, successes advance state), without touching real hardware.
- Real hardware behavior (actual burn/read) is verified manually on the
  Linux host — not automated.

## 8. Non-Goals

Explicitly out of scope for this spec: multi-drive/parallel burns, auto
disc-insert detection, PDF cover generation, encryption at rest,
multi-user/auth, Windows support, cloud mirroring, re-burning a
replacement disc after reconstruction.

## 9. TODO / Future Work

- **Scheduled/on-demand disc health verification:** periodically (or on
  demand from the UI) re-insert and re-read previously burned discs,
  running `par2verify` to catch bit-rot before it silently accumulates
  into total disc loss. Flag discs whose health check fails in the
  catalog.
- **Repair/replacement workflow:** when a disc fails a health check or is
  confirmed lost, reconstruct it (via Section 5, if it belongs to a
  group) and burn a fresh replacement disc, updating the catalog to point
  at the new physical media while preserving the history of the
  replacement.
