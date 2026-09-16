# Optical Disc Archive — 00: Overview, Containerization & Data Layer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up the project skeleton — Go module, Docker Compose stack, Postgres schema/data-access layer, the WebDAV mounts, and the command-executor abstraction everything else will build on — as one runnable, testable container.

**Architecture:** A single Go binary (`cmd/archive-core`) that on startup connects to Postgres, applies the schema, seeds default media types, mounts two WebDAV shares over local directories, and serves a health check. This plan produces no burn/retrieval logic yet — just the foundation later plans build on.

**Tech Stack:** Go 1.25+, `github.com/jackc/pgx/v5` (Postgres driver, no ORM), `golang.org/x/net/webdav`, Postgres 16, Docker Compose.

**Depends on:** nothing — this is the first plan. Later plans (01–04) depend on the package layout and types defined here.

---

## Package Layout (this plan creates)

```
varis/
  go.mod
  Dockerfile
  docker-compose.yml
  cmd/archive-core/main.go
  internal/config/config.go
  internal/db/
    schema.sql
    db.go
    media_types.go
    disks.go
    files.go
  internal/execx/
    exec.go
    fake.go
  internal/webdav/
    webdav.go
```

---

### Task 1: Go module, Dockerfile, Compose scaffold

**Files:**
- Create: `go.mod`
- Create: `Dockerfile`
- Create: `docker-compose.yml`
- Create: `.dockerignore`

- [ ] **Step 1: Initialize the module**

Run:
```bash
go mod init varis
```
Expected: creates `go.mod` with `module varis` and a `go 1.23` (or newer installed) directive.

- [ ] **Step 2: Write the Dockerfile**

```dockerfile
FROM golang:1.23-bookworm AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /archive-core ./cmd/archive-core

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    par2 xorriso wodim ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /archive-core /usr/local/bin/archive-core
ENTRYPOINT ["/usr/local/bin/archive-core"]
```

- [ ] **Step 3: Write docker-compose.yml**

```yaml
services:
  archive-db:
    image: postgres:16
    environment:
      POSTGRES_USER: varis
      POSTGRES_PASSWORD: varis
      POSTGRES_DB: varis
    volumes:
      - archive-db-data:/var/lib/postgresql/data

  archive-core:
    build: .
    depends_on:
      - archive-db
    environment:
      DATABASE_URL: postgres://varis:varis@archive-db:5432/varis?sslmode=disable
    ports:
      - "8080:8080"
    devices:
      - "/dev/sr0:/dev/sr0"
    volumes:
      - ./data/staging:/data/staging
      - ./data/spool:/data/spool
      - ./data/retrieved:/data/retrieved

volumes:
  archive-db-data:
```

- [ ] **Step 4: Write .dockerignore**

```
data/
*.md
.git/
```

- [ ] **Step 5: Commit**

```bash
git add go.mod Dockerfile docker-compose.yml .dockerignore
git commit -m "Scaffold Go module, Dockerfile, and Compose stack"
```

---

### Task 2: Config package

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

```go
package config

import "testing"

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("STAGING_DIR", "")
	t.Setenv("SPOOL_DIR", "")
	t.Setenv("RETRIEVED_DIR", "")
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("OPTICAL_DEVICE", "")

	cfg := Load()

	if cfg.StagingDir != "/data/staging" {
		t.Errorf("StagingDir = %q, want /data/staging", cfg.StagingDir)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
}

func TestLoad_Overrides(t *testing.T) {
	t.Setenv("HTTP_ADDR", ":9090")

	cfg := Load()

	if cfg.HTTPAddr != ":9090" {
		t.Errorf("HTTPAddr = %q, want :9090", cfg.HTTPAddr)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/...`
Expected: FAIL — `undefined: Load` (package doesn't exist yet)

- [ ] **Step 3: Write the implementation**

```go
package config

import (
	"cmp"
	"os"
)

type Config struct {
	DatabaseURL   string
	StagingDir    string
	SpoolDir      string
	RetrievedDir  string
	HTTPAddr      string
	OpticalDevice string
}

func Load() Config {
	return Config{
		DatabaseURL:   cmp.Or(os.Getenv("DATABASE_URL"), "postgres://varis:varis@archive-db:5432/varis?sslmode=disable"),
		StagingDir:    cmp.Or(os.Getenv("STAGING_DIR"), "/data/staging"),
		SpoolDir:      cmp.Or(os.Getenv("SPOOL_DIR"), "/data/spool"),
		RetrievedDir:  cmp.Or(os.Getenv("RETRIEVED_DIR"), "/data/retrieved"),
		HTTPAddr:      cmp.Or(os.Getenv("HTTP_ADDR"), ":8080"),
		OpticalDevice: cmp.Or(os.Getenv("OPTICAL_DEVICE"), "/dev/sr0"),
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -m "Add config package for env-driven settings"
```

---

### Task 3: Database schema and connection

**Files:**
- Create: `internal/db/schema.sql`
- Create: `internal/db/db.go`
- Test: `internal/db/db_test.go`

- [ ] **Step 1: Write the schema**

```sql
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE IF NOT EXISTS media_types (
    name VARCHAR(30) PRIMARY KEY,
    capacity_bytes BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS disk_groups (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    burn_job_id UUID NOT NULL,
    group_size INT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS disks (
    id VARCHAR(50) PRIMARY KEY,
    media_type VARCHAR(30) REFERENCES media_types(name),
    parity_percent SMALLINT NOT NULL,
    group_id UUID REFERENCES disk_groups(id),
    role VARCHAR(10) NOT NULL DEFAULT 'data',
    slot_index SMALLINT,
    iso_hash VARCHAR(256),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS files (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    disk_id VARCHAR(50) REFERENCES disks(id),
    original_path TEXT NOT NULL,
    size_bytes BIGINT NOT NULL,
    file_hash VARCHAR(256),
    archived_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_files_path ON files USING gin (original_path gin_trgm_ops);
```

Note: `gen_random_uuid()` requires Postgres 13+ built-in `pgcrypto`-free UUID generation (available by default in Postgres 16's `postgres` image — no extra extension needed).

- [ ] **Step 2: Add the pgx dependency**

Run:
```bash
go get github.com/jackc/pgx/v5
```

- [ ] **Step 3: Write db.go**

```go
package db

import (
	"context"
	_ "embed"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}
```

- [ ] **Step 4: Write a connection test that skips without a real database**

```go
package db

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// requirePool skips the test unless DATABASE_URL points at a real Postgres
// (e.g. `docker compose up -d archive-db` locally). No dockertest/testcontainers
// dependency — this project already ships a Postgres via Compose, so tests use it.
func requirePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set; start archive-db with `docker compose up -d archive-db` to run this test")
	}
	pool, err := Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestConnect_AppliesSchema(t *testing.T) {
	pool := requirePool(t)
	var exists bool
	err := pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'disks')`).Scan(&exists)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !exists {
		t.Error("disks table was not created")
	}
}
```

- [ ] **Step 5: Run the test**

Run:
```bash
docker compose up -d archive-db
DATABASE_URL="postgres://varis:varis@localhost:5432/varis?sslmode=disable" go test ./internal/db/... -run TestConnect_AppliesSchema -v
```
Expected: PASS (or SKIP if you don't have the DB running — both are fine at this point).

- [ ] **Step 6: Commit**

```bash
git add internal/db go.mod go.sum
git commit -m "Add Postgres schema and connection handling"
```

---

### Task 4: Media types, disks, and files data access

**Files:**
- Create: `internal/db/media_types.go`
- Create: `internal/db/disks.go`
- Create: `internal/db/files.go`
- Test: `internal/db/media_types_test.go`
- Test: `internal/db/disks_test.go`

- [ ] **Step 1: Write the failing test for media types**

```go
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
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/db/... -run TestSeedAndListMediaTypes -v`
Expected: FAIL — `undefined: SeedMediaTypes`

- [ ] **Step 3: Implement media_types.go**

```go
package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type MediaType struct {
	Name          string
	CapacityBytes int64
}

var defaultMediaTypes = []MediaType{
	{Name: "BD-R", CapacityBytes: 25_025_314_816},
	{Name: "BD-R DL", CapacityBytes: 50_050_629_632},
}

func SeedMediaTypes(ctx context.Context, pool *pgxpool.Pool) error {
	for _, mt := range defaultMediaTypes {
		if _, err := pool.Exec(ctx,
			`INSERT INTO media_types (name, capacity_bytes) VALUES ($1, $2) ON CONFLICT (name) DO NOTHING`,
			mt.Name, mt.CapacityBytes); err != nil {
			return err
		}
	}
	return nil
}

func ListMediaTypes(ctx context.Context, pool *pgxpool.Pool) ([]MediaType, error) {
	rows, err := pool.Query(ctx, `SELECT name, capacity_bytes FROM media_types ORDER BY capacity_bytes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MediaType
	for rows.Next() {
		var mt MediaType
		if err := rows.Scan(&mt.Name, &mt.CapacityBytes); err != nil {
			return nil, err
		}
		out = append(out, mt)
	}
	return out, rows.Err()
}

func AddMediaType(ctx context.Context, pool *pgxpool.Pool, mt MediaType) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO media_types (name, capacity_bytes) VALUES ($1, $2)`,
		mt.Name, mt.CapacityBytes)
	return err
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/db/... -run TestSeedAndListMediaTypes -v`
Expected: PASS (or SKIP without a live DB)

- [ ] **Step 5: Write the failing test for disks**

```go
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
```

- [ ] **Step 6: Run it to verify it fails**

Run: `go test ./internal/db/... -run TestDiskLifecycle -v`
Expected: FAIL — `undefined: NextDiskID`

- [ ] **Step 7: Implement disks.go**

```go
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Disk struct {
	ID            string
	MediaType     string
	ParityPercent int
	GroupID       *string
	Role          string // "data" | "parity"
	SlotIndex     *int
	ISOHash       string
	CreatedAt     time.Time
}

func InsertDiskGroup(ctx context.Context, pool *pgxpool.Pool, burnJobID string, groupSize int) (string, error) {
	var id string
	err := pool.QueryRow(ctx,
		`INSERT INTO disk_groups (burn_job_id, group_size) VALUES ($1, $2) RETURNING id`,
		burnJobID, groupSize).Scan(&id)
	return id, err
}

// NextDiskID returns the next sequential ID for a media-type prefix, e.g. "BD:0007".
// Safe without locking here because the burn pipeline (see plan 01) only ever
// runs one job at a time.
func NextDiskID(ctx context.Context, pool *pgxpool.Pool, prefix string) (string, error) {
	var count int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM disks WHERE id LIKE $1`,
		prefix+":%").Scan(&count)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s:%04d", prefix, count+1), nil
}

func InsertDisk(ctx context.Context, pool *pgxpool.Pool, d Disk) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO disks (id, media_type, parity_percent, group_id, role, slot_index, iso_hash)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		d.ID, d.MediaType, d.ParityPercent, d.GroupID, d.Role, d.SlotIndex, d.ISOHash)
	return err
}

func GetDisk(ctx context.Context, pool *pgxpool.Pool, id string) (Disk, error) {
	var d Disk
	err := pool.QueryRow(ctx,
		`SELECT id, media_type, parity_percent, group_id, role, slot_index, iso_hash, created_at
		 FROM disks WHERE id = $1`, id).
		Scan(&d.ID, &d.MediaType, &d.ParityPercent, &d.GroupID, &d.Role, &d.SlotIndex, &d.ISOHash, &d.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Disk{}, fmt.Errorf("disk %s not found", id)
	}
	return d, err
}

func GroupMembers(ctx context.Context, pool *pgxpool.Pool, groupID string) ([]Disk, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, media_type, parity_percent, group_id, role, slot_index, iso_hash, created_at
		 FROM disks WHERE group_id = $1 ORDER BY slot_index`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Disk
	for rows.Next() {
		var d Disk
		if err := rows.Scan(&d.ID, &d.MediaType, &d.ParityPercent, &d.GroupID, &d.Role, &d.SlotIndex, &d.ISOHash, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
```

- [ ] **Step 8: Run test to verify it passes**

Run: `go test ./internal/db/... -run TestDiskLifecycle -v`
Expected: PASS (or SKIP without a live DB). Note: re-running this test against a persistent DB will fail the second time since `BDTEST:0001` already exists — either run against a fresh `docker compose down -v && up -d archive-db` each time, or accept this as a documented limitation of testing against a shared DB (acceptable at this project's scale; no test-database-per-run infrastructure is being built for a single-user home tool).

- [ ] **Step 9: Implement files.go (no dedicated test — covered by plan 03's search handler test)**

```go
package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type FileRecord struct {
	ID           string
	DiskID       string
	OriginalPath string
	SizeBytes    int64
	FileHash     string
}

func InsertFile(ctx context.Context, pool *pgxpool.Pool, f FileRecord) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO files (disk_id, original_path, size_bytes, file_hash)
		 VALUES ($1, $2, $3, $4)`,
		f.DiskID, f.OriginalPath, f.SizeBytes, f.FileHash)
	return err
}

func SearchFiles(ctx context.Context, pool *pgxpool.Pool, query string) ([]FileRecord, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, disk_id, original_path, size_bytes, file_hash FROM files
		 WHERE original_path ILIKE '%' || $1 || '%'
		 ORDER BY similarity(original_path, $1) DESC LIMIT 50`, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileRecord
	for rows.Next() {
		var f FileRecord
		if err := rows.Scan(&f.ID, &f.DiskID, &f.OriginalPath, &f.SizeBytes, &f.FileHash); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
```

- [ ] **Step 10: Commit**

```bash
git add internal/db
git commit -m "Add media type, disk, and file data access"
```

---

### Task 5: Command executor abstraction

Every external binary call (`par2create`, `xorriso`, `wodim`) goes through this
interface so plan 01's state-machine tests can run without real hardware.

**Files:**
- Create: `internal/execx/exec.go`
- Create: `internal/execx/fake.go`
- Test: `internal/execx/fake_test.go`

- [ ] **Step 1: Write the failing test**

```go
package execx

import (
	"context"
	"errors"
	"testing"
)

func TestFakeExecutor_RecordsCallsAndReturnsConfiguredResult(t *testing.T) {
	fake := &FakeExecutor{
		Results: map[string]Result{
			"par2create": {Output: []byte("ok"), Err: nil},
			"xorriso":    {Output: nil, Err: errors.New("boom")},
		},
	}

	out, err := fake.Run(context.Background(), "par2create", "-r10", "set.par2")
	if err != nil || string(out) != "ok" {
		t.Fatalf("par2create call = %q, %v", out, err)
	}

	_, err = fake.Run(context.Background(), "xorriso", "-as", "mkisofs")
	if err == nil {
		t.Fatal("expected xorriso call to fail")
	}

	calls := fake.Calls()
	if len(calls) != 2 || calls[0].Name != "par2create" || calls[1].Name != "xorriso" {
		t.Errorf("Calls() = %+v", calls)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/execx/... -v`
Expected: FAIL — package doesn't exist yet

- [ ] **Step 3: Write exec.go**

```go
package execx

import (
	"context"
	"os/exec"
)

// Executor runs external commands. Production code uses RealExecutor;
// tests use FakeExecutor so the burn/retrieve state machines can be
// tested without par2/xorriso/wodim or real hardware.
type Executor interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type RealExecutor struct{}

func (RealExecutor) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
```

- [ ] **Step 4: Write fake.go**

```go
package execx

import (
	"context"
	"sync"
)

type Call struct {
	Name string
	Args []string
}

type Result struct {
	Output []byte
	Err    error
}

// FakeExecutor records every call and returns the Result configured for
// that command name (zero value: nil output, nil error).
type FakeExecutor struct {
	Results map[string]Result

	mu    sync.Mutex
	calls []Call
}

func (f *FakeExecutor) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, Call{Name: name, Args: args})
	f.mu.Unlock()

	r := f.Results[name]
	return r.Output, r.Err
}

func (f *FakeExecutor) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/execx/... -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/execx
git commit -m "Add Executor abstraction with a fake for hardware-free tests"
```

---

### Task 6: WebDAV mounts

**Files:**
- Create: `internal/webdav/webdav.go`
- Test: `internal/webdav/webdav_test.go`

- [ ] **Step 1: Add the dependency**

Run:
```bash
go get golang.org/x/net/webdav
```

- [ ] **Step 2: Write the failing test**

```go
package webdav

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandler_PutAndGet(t *testing.T) {
	dir := t.TempDir()
	h := Handler("/", dir)

	req := httptest.NewRequest(http.MethodPut, "/hello.txt", strings.NewReader("hi"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("PUT status = %d, want 201", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/hello.txt", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "hi" {
		t.Fatalf("GET = %d %q, want 200 \"hi\"", w.Code, w.Body.String())
	}
}

func TestDirSize(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("1234"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("12345678"), 0o644); err != nil {
		t.Fatal(err)
	}

	size, err := DirSize(dir)
	if err != nil {
		t.Fatalf("DirSize: %v", err)
	}
	if size != 12 {
		t.Errorf("DirSize = %d, want 12", size)
	}
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test ./internal/webdav/... -v`
Expected: FAIL — package doesn't exist yet

- [ ] **Step 4: Write webdav.go**

```go
package webdav

import (
	"io/fs"
	"net/http"
	"path/filepath"

	"golang.org/x/net/webdav"
)

// Handler serves dir as a WebDAV share.
func Handler(prefix, dir string) http.Handler {
	return &webdav.Handler{
		Prefix:     prefix,
		FileSystem: webdav.Dir(dir),
		LockSystem: webdav.NewMemLS(),
	}
}

// DirSize returns the total size in bytes of all regular files under dir.
func DirSize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/webdav/... -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/webdav go.mod go.sum
git commit -m "Add WebDAV share handler and directory size helper"
```

---

### Task 7: Wire main.go

**Files:**
- Create: `cmd/archive-core/main.go`

- [ ] **Step 1: Write main.go**

```go
package main

import (
	"context"
	"log"
	"net/http"

	"varis/internal/config"
	"varis/internal/db"
	"varis/internal/webdav"
)

func main() {
	cfg := config.Load()
	ctx := context.Background()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db connect: %v", err)
	}
	defer pool.Close()

	if err := db.SeedMediaTypes(ctx, pool); err != nil {
		log.Fatalf("seed media types: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.Handle("/webdav/staging/", http.StripPrefix("/webdav/staging", webdav.Handler("/", cfg.StagingDir)))
	mux.Handle("/webdav/retrieved/", http.StripPrefix("/webdav/retrieved", webdav.Handler("/", cfg.RetrievedDir)))

	log.Printf("listening on %s", cfg.HTTPAddr)
	log.Fatal(http.ListenAndServe(cfg.HTTPAddr, mux))
}
```

- [ ] **Step 2: Build it**

Run: `go build ./...`
Expected: builds with no errors

- [ ] **Step 3: Commit**

```bash
git add cmd
git commit -m "Wire main.go: DB connect, media type seeding, WebDAV mounts, health check"
```

- [ ] **Step 4: Manual smoke test**

```bash
mkdir -p data/staging data/spool data/retrieved
docker compose up --build -d
curl http://localhost:8080/healthz
# Expected: ok

curl -T README.md http://localhost:8080/webdav/staging/README.md
curl http://localhost:8080/webdav/staging/README.md
# Expected: your file's contents echoed back
```

This is the point where you can also mount `http://localhost:8080/webdav/staging/` as a Finder/Nautilus network drive and drag files into it by hand.

---

## Plan Self-Review

**Spec coverage:** Architecture/containerization (spec §2) ✓, data model (spec §3) ✓, WebDAV mounts (spec §2/§6) ✓. Burn pipeline, cross-disc parity, web UI, and error-handling hardening are intentionally deferred to plans 01–04.

**Placeholder scan:** none — every step has runnable code.

**Type consistency:** `Disk`, `DiskGroup` (via `InsertDiskGroup`'s return), `MediaType`, and `FileRecord` structs defined here are the exact types plans 01–04 reference; the `execx.Executor`/`execx.FakeExecutor` interface is likewise the shared contract for all `os/exec` calls in plan 01 onward.
