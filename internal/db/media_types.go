package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type MediaType struct {
	Name          string
	CapacityBytes int64
	IDPrefix      string
	WriteKind     string // "optical" | "filesystem"
}

var defaultMediaTypes = []MediaType{
	{Name: "BD-R", CapacityBytes: 25_025_314_816, IDPrefix: "BD", WriteKind: "optical"},
	{Name: "BD-R DL", CapacityBytes: 50_050_629_632, IDPrefix: "BDDL", WriteKind: "optical"},
}

func SeedMediaTypes(ctx context.Context, pool *pgxpool.Pool) error {
	for _, mt := range defaultMediaTypes {
		if _, err := pool.Exec(ctx,
			`INSERT INTO media_types (name, capacity_bytes, id_prefix, write_kind) VALUES ($1, $2, $3, $4) ON CONFLICT (name) DO NOTHING`,
			mt.Name, mt.CapacityBytes, mt.IDPrefix, mt.WriteKind); err != nil {
			return err
		}
	}
	return nil
}

func ListMediaTypes(ctx context.Context, pool *pgxpool.Pool) ([]MediaType, error) {
	rows, err := pool.Query(ctx, `SELECT name, capacity_bytes, id_prefix, write_kind FROM media_types ORDER BY capacity_bytes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MediaType
	for rows.Next() {
		var mt MediaType
		if err := rows.Scan(&mt.Name, &mt.CapacityBytes, &mt.IDPrefix, &mt.WriteKind); err != nil {
			return nil, err
		}
		out = append(out, mt)
	}
	return out, rows.Err()
}

func AddMediaType(ctx context.Context, pool *pgxpool.Pool, mt MediaType) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO media_types (name, capacity_bytes, id_prefix, write_kind) VALUES ($1, $2, $3, $4)`,
		mt.Name, mt.CapacityBytes, mt.IDPrefix, mt.WriteKind)
	return err
}

// GetMediaTypeCapacity is kept separate from GetMediaType (below) because
// internal/retrieve's Catalog.MediaCapacity only ever needs the capacity,
// to resolve a cross-disc-parity group's fixed XOR length — it has no use
// for IDPrefix/WriteKind, both of which are burn-time-only concerns.
func GetMediaTypeCapacity(ctx context.Context, pool *pgxpool.Pool, name string) (int64, error) {
	var capacity int64
	err := pool.QueryRow(ctx,
		`SELECT capacity_bytes FROM media_types WHERE name = $1`, name).Scan(&capacity)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("media type %q not found", name)
	}
	return capacity, err
}

// GetMediaType returns the full row, for callers (the web layer's
// startBurn) that need IDPrefix/WriteKind alongside capacity.
func GetMediaType(ctx context.Context, pool *pgxpool.Pool, name string) (MediaType, error) {
	var mt MediaType
	err := pool.QueryRow(ctx,
		`SELECT name, capacity_bytes, id_prefix, write_kind FROM media_types WHERE name = $1`, name).
		Scan(&mt.Name, &mt.CapacityBytes, &mt.IDPrefix, &mt.WriteKind)
	if errors.Is(err, pgx.ErrNoRows) {
		return MediaType{}, fmt.Errorf("media type %q not found", name)
	}
	return mt, err
}
