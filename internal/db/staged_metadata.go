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
