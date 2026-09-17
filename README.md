# Varis

A self-hosted system for archiving large amounts of data onto optical discs (Blu-ray/DVD) with built-in redundancy, a searchable catalog, and a simple web UI.

You drop files into a WebDAV "To-Archive" folder, click Burn, and Varis packs them into disc-sized sets, adds parity, burns them, and records every file in a Postgres catalog. Later you search the catalog for a file, and Varis tells you which physical disc to insert.

There are two independent layers of redundancy:

- **Intra-disc par2 parity** — protects against partial bit-rot on a single disc. Always on; you pick the percentage per burn.
- **Cross-disc XOR parity (RAID5-style)** — protects against losing an *entire* disc. Opt-in per burn: every N data discs get their own parity disc, and if one disc in a group is lost, its contents are reconstructed from the other members of the group.

The full design rationale — data model, state machines, error handling, non-goals — lives in [`docs/superpowers/specs/2026-09-16-optical-disc-archive-design.md`](docs/superpowers/specs/2026-09-16-optical-disc-archive-design.md). Read that for the complete picture.

## Requirements

- **`archive-core` must run on a Linux host with real Docker**, because the optical drive is passed into the container as a device (`--device /dev/sr0`). Docker Desktop on macOS/Windows cannot do this.
- **macOS is a client only.** It mounts the WebDAV shares and opens the web UI over the network; it never touches the drive directly.
- **No authentication.** Varis assumes a trusted home LAN and a single user. Don't expose it to the internet.
- **One job at a time.** There is one physical drive, so a burn or a retrieval runs exclusively — never both, never two at once.

## Quick start

```bash
docker compose up --build -d
```

This starts two services:

| Service | What it is |
|---|---|
| `archive-db` | Postgres 16 — the catalog. The schema is applied automatically on startup. |
| `archive-core` | The Go binary: web UI, WebDAV server, and the burn/retrieve worker, all in one process. |

Then:

1. **Mount the WebDAV shares.**
   - `http://localhost:8080/webdav/staging/` — the "To-Archive" drop zone. Put files here to archive them.
   - `http://localhost:8080/webdav/retrieved/` — where restored files appear.

   On macOS: Finder → Go → Connect to Server → `http://localhost:8080/webdav/staging/` (same for the retrieved share, just change the path). On Linux: `davfs2`, or your file manager's "Connect to Server".

   Replace `localhost` with the Linux host's address when connecting from another machine.

2. **Open the dashboard** at `http://localhost:8080/`.

A `GET /healthz` endpoint returns `ok` if you want something to point a monitor at.

## Deployment configuration

The committed `docker-compose.yml` passes the optical drive straight through and stores data on the host filesystem:

```yaml
    devices:
      - "/dev/sr0:/dev/sr0"
    volumes:
      - ./data/staging:/data/staging      # WebDAV "To-Archive" drop zone
      - ./data/spool:/data/spool          # working ISOs, parity and TOC files
      - ./data/retrieved:/data/retrieved  # WebDAV output for restored files
```

The `devices:` passthrough only works on a Linux host — it's the reason `archive-core` can't run on Docker Desktop for macOS/Windows (see Requirements above). If you're testing on a machine with no optical drive, comment that block out and the rest of the stack (staging, burning up to the point of the actual disc write, retrieval) still runs; you just can't exercise a real burn or read. You can also switch the three bind mounts to named Docker volumes if you don't need the files visible on the host.

Port `8080` is published for the web UI and WebDAV. The spool directory holds a burn's in-progress ISOs, par2 files and TOC; it's cleaned up once a disc reaches `DONE`, so it needs roughly 2–3× a disc's capacity free while a burn is running.

### Environment variables

All are optional and defaulted in `internal/config/config.go`:

| Variable | Default | Purpose |
|---|---|---|
| `DATABASE_URL` | `postgres://varis:varis@archive-db:5432/varis?sslmode=disable` | Postgres connection string |
| `STAGING_DIR` | `/data/staging` | WebDAV "To-Archive" drop zone |
| `SPOOL_DIR` | `/data/spool` | Working ISOs, parity and TOC before a burn completes |
| `RETRIEVED_DIR` | `/data/retrieved` | WebDAV output for restored files |
| `HTTP_ADDR` | `:8080` | Address the web server listens on |
| `OPTICAL_DEVICE` | `/dev/sr0` | Optical drive device path |

## Burning discs

Stage your files in the To-Archive share. The dashboard lists every staged file, grouped by top-level folder, with a checkbox per file and per folder (a folder's checkbox selects everything currently under it). Check whatever you want to tag, enter comma-separated tags and/or a description, and click **Apply to selected** — this is optional and has no effect on burning itself, but it carries over to the permanent catalog record once a file is burned, so the Library's search can later match on it too (e.g. tag a folder "family videos 2019" and find it later by that tag, not just by filename).

Then, still on the dashboard:

1. **Media type** — pick from the dropdown. `BD-R` (25 GB) and `BD-R DL` (50 GB) are seeded by default; add more on the Config page.
2. **Parity %** — the intra-disc par2 redundancy, default 10% (accepted range 5–50). Use something lower like 5% for low-value bulk data, higher like 25%+ for anything critical. Varis sizes each disc so that `data + par2(data)` fits the media capacity, so a higher percentage means fewer files per disc.
3. **Compression** — optional, off by default.
4. **Cross-disc parity** — optional checkbox plus a group size (default 10). Every N data discs in the batch get their own extra parity disc, so any single disc lost from a group can be rebuilt. It costs one whole extra disc of media and burn time per group.

Click **Burn**. If the staged data needs more than one disc, all discs are planned upfront so the UI can tell you how many it'll take — but they're burned **one at a time**. The Jobs page prompts you to insert the next blank disc and click continue between each one.

Each disc runs through the state machine `PLANNING → PACKING → PARITY → ISO → BURNING → VERIFYING → DONE`. Staged files are never deleted or marked archived until a disc reaches `DONE`, so a failed burn just means retrying with a fresh blank — nothing is lost.

Once a disc is `DONE`, its printable cover (disk ID plus a QR code linking back to the catalog) is available from the Jobs page. Print it or save it as a PDF with the browser's own print function.

## Retrieving files

1. Search for the file on the **Library** page.
2. Click **Get**. The job goes to "awaiting media" and names the disc you need, e.g. `BD:0007`.
3. Insert that disc and click **Read Disk** — this is manual, there's no auto-detection.
4. Varis checks the disc's `TOC.json` matches the expected disk ID, runs `par2verify` (repairing minor bit-rot in place where it can), and copies the file to the Retrieved WebDAV share.

### If a disc fails

If the disc won't read or fails verification, and it belongs to a cross-disc-parity group, the Library page offers **Reconstruct from group**. That walks you through inserting every *other* disc in the group — the remaining data discs plus the group's parity disc — one at a time via the same "Read Disk" button. Once all the others have been supplied, the missing disc's image is XOR-reconstructed and your file is extracted from it.

Normal retrieval never touches this machinery. It only comes up when the specific disc you asked for is damaged or lost.

## Web UI routes

Defined in `internal/web/server.go`:

| Route | Purpose |
|---|---|
| `GET /` | Dashboard: staged file listing (grouped by folder, with tag/description checkboxes), media type, parity %, compression, cross-disc parity toggle + group size, Burn button |
| `POST /staging/metadata` | Apply tags/description to the selected staged files/folders |
| `GET /jobs` | HTMX-polled fragment showing burn state machine progress |
| `POST /burn` | Start a burn job |
| `POST /jobs/continue` | "Next blank disc is inserted, continue" |
| `POST /jobs/retry` | Retry the current disc after a failure |
| `GET /library` | Search page |
| `GET /search?q=` | Search results: filename, size, required disk ID, tags/description, Get button — matches path, tags, and description |
| `POST /retrieve` | Start a retrieval |
| `POST /retrieve/read` | "Disc is inserted, read it" |
| `POST /retrieve/reconstruct/start` | Begin group reconstruction |
| `POST /retrieve/reconstruct/read` | Read one group member during reconstruction |
| `GET /config`, `POST /config` | Manage the `media_types` list (name + capacity) |
| `GET /cover/{diskID}` | Printable HTML disc cover with QR code |

## Architecture

A single Go binary (`cmd/archive-core`) serving everything — the web UI, the WebDAV shares, and the burn/retrieve worker. They share one in-process state machine and one drive, so splitting them into separate services would buy nothing. Routing is stdlib `net/http` with Go 1.22+ method+wildcard patterns; the UI is `html/template` plus HTMX, with templates embedded in the binary.

```
cmd/archive-core/    main: config, DB connect, WebDAV mounts, routes, listen
internal/
  config/            environment-variable configuration
  db/                Postgres schema + data access (media_types, disks, files)
  binpack/           scan staging, size discs from parity %, bucket files
  toc/               TOC.json — the on-disc header, LTO-FS style
  burn/              the burn state machine: pack, parity, ISO, write, verify,
                     cross-disc parity computation and reconstruction
  retrieve/          the retrieval state machine and group-reconstruction flow
  xordisk/           XOR across disc images — computes a group's parity disc
                     and rebuilds a missing member, same operation both ways
  execx/             Executor interface over os/exec, plus a fake for tests
  webdav/            the two WebDAV shares, staging directory size
  web/               handlers and HTML templates
```

External binaries baked into the image: `par2` (parity), `xorriso` (ISO authoring), `wodim` (burning). Postgres is accessed with `pgx/v5`, no ORM.

## Running tests

```bash
go test ./...        # everything that doesn't need a live Postgres
make test            # the same, with -race
```

Most packages test against a fake `execx.Executor` and in-memory fakes rather than real hardware or a real database — real optical drives and `par2`/`xorriso`/`wodim` can't run in CI (see design spec §7). The state machines are tested for their *transitions*: a failing command moves the job to `FAILED`, a succeeding one advances the state.

Use `-race` for the concurrency-sensitive `burn` and `retrieve` state machines:

```bash
go test ./... -race
```

Some `internal/db` tests need a live Postgres. They skip gracefully when `DATABASE_URL` is unset, so `go test ./...` passes without one. To actually run them:

```bash
docker compose up -d archive-db
DATABASE_URL=postgres://varis:varis@localhost:5432/varis?sslmode=disable go test ./internal/db/...
```

**Note:** `docker-compose.yml` does **not** currently publish Postgres's port, so `localhost:5432` won't reach it as written. Either add a ports mapping to `archive-db` temporarily:

```yaml
    ports:
      - "5432:5432"
```

...or run the tests from inside the `archive-core` container, where the DB host is `archive-db` rather than `localhost`.

## Implementation plans

The work is broken into five plans, in order:

1. [`00-overview.md`](docs/superpowers/plans/2026-09-16-optical-disc-archive-00-overview.md) — project skeleton, Compose stack, Postgres schema and data layer, WebDAV mounts, command-executor abstraction
2. [`01-burn-pipeline.md`](docs/superpowers/plans/2026-09-16-optical-disc-archive-01-burn-pipeline.md) — bin-packing, par2, TOC, ISO, burn, verify
3. [`02-cross-disc-parity.md`](docs/superpowers/plans/2026-09-16-optical-disc-archive-02-cross-disc-parity.md) — XOR parity discs and reconstruction
4. [`03-web-ui-retrieval.md`](docs/superpowers/plans/2026-09-16-optical-disc-archive-03-web-ui-retrieval.md) — dashboard, jobs, library, search, retrieval flow
5. [`04-hardening-and-integration.md`](docs/superpowers/plans/2026-09-16-optical-disc-archive-04-hardening-and-integration.md) — hardening and end-to-end integration

Start with the [design spec](docs/superpowers/specs/2026-09-16-optical-disc-archive-design.md) if you're picking up implementation from here — it explains *why* each piece works the way it does, and lists what's explicitly out of scope.
