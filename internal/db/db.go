package db

import (
	"context"
	_ "embed"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

// ErrNotFound is wrapped into the "not found" errors returned by lookups
// like GetFile, so callers can distinguish "doesn't exist" from other
// failures via errors.Is without matching on error message text.
var ErrNotFound = errors.New("not found")

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
