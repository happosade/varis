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

func GetMediaTypeCapacity(ctx context.Context, pool *pgxpool.Pool, name string) (int64, error) {
	var capacity int64
	err := pool.QueryRow(ctx,
		`SELECT capacity_bytes FROM media_types WHERE name = $1`, name).Scan(&capacity)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("media type %q not found", name)
	}
	return capacity, err
}
