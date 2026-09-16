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
