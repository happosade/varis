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

func GetFile(ctx context.Context, pool *pgxpool.Pool, id string) (FileRecord, error) {
	var f FileRecord
	err := pool.QueryRow(ctx,
		`SELECT id, disk_id, original_path, size_bytes, file_hash FROM files WHERE id = $1`, id).
		Scan(&f.ID, &f.DiskID, &f.OriginalPath, &f.SizeBytes, &f.FileHash)
	return f, err
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
