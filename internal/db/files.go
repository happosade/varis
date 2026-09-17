package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type FileRecord struct {
	ID           string
	DiskID       string
	OriginalPath string
	SizeBytes    int64
	FileHash     string
	Tags         []string
	Description  string
}

func InsertFile(ctx context.Context, pool *pgxpool.Pool, f FileRecord) error {
	tags := f.Tags
	if tags == nil {
		// files.tags is NOT NULL; a nil Go slice would otherwise send SQL
		// NULL and violate that constraint. The common case for this is a
		// file that was never given staged metadata before burning.
		tags = []string{}
	}
	_, err := pool.Exec(ctx,
		`INSERT INTO files (disk_id, original_path, size_bytes, file_hash, tags, description)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		f.DiskID, f.OriginalPath, f.SizeBytes, f.FileHash, tags, f.Description)
	return err
}

func GetFile(ctx context.Context, pool *pgxpool.Pool, id string) (FileRecord, error) {
	var f FileRecord
	err := pool.QueryRow(ctx,
		`SELECT id, disk_id, original_path, size_bytes, file_hash, tags, description FROM files WHERE id = $1`, id).
		Scan(&f.ID, &f.DiskID, &f.OriginalPath, &f.SizeBytes, &f.FileHash, &f.Tags, &f.Description)
	if errors.Is(err, pgx.ErrNoRows) {
		return FileRecord{}, fmt.Errorf("file %s not found: %w", id, ErrNotFound)
	}
	return f, err
}

func SearchFiles(ctx context.Context, pool *pgxpool.Pool, query string) ([]FileRecord, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, disk_id, original_path, size_bytes, file_hash, tags, description FROM files
		 WHERE original_path ILIKE '%' || $1 || '%'
		    OR description ILIKE '%' || $1 || '%'
		    OR EXISTS (SELECT 1 FROM unnest(tags) t WHERE t ILIKE '%' || $1 || '%')
		 ORDER BY similarity(original_path, $1) DESC LIMIT 50`, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileRecord
	for rows.Next() {
		var f FileRecord
		if err := rows.Scan(&f.ID, &f.DiskID, &f.OriginalPath, &f.SizeBytes, &f.FileHash, &f.Tags, &f.Description); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
