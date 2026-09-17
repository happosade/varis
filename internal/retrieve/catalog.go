package retrieve

import (
	"context"

	"varis/internal/db"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Catalog is the subset of DB access the retrieve package needs.
type Catalog interface {
	GetFile(ctx context.Context, fileID string) (db.FileRecord, error)
	GetDisk(ctx context.Context, diskID string) (db.Disk, error)
	GroupMembers(ctx context.Context, groupID string) ([]db.Disk, error)
	MediaCapacity(ctx context.Context, mediaType string) (int64, error)
}

type dbCatalog struct{ pool *pgxpool.Pool }

func NewDBCatalog(pool *pgxpool.Pool) Catalog { return dbCatalog{pool: pool} }

func (c dbCatalog) GetFile(ctx context.Context, fileID string) (db.FileRecord, error) {
	return db.GetFile(ctx, c.pool, fileID)
}
func (c dbCatalog) GetDisk(ctx context.Context, diskID string) (db.Disk, error) {
	return db.GetDisk(ctx, c.pool, diskID)
}
func (c dbCatalog) GroupMembers(ctx context.Context, groupID string) ([]db.Disk, error) {
	return db.GroupMembers(ctx, c.pool, groupID)
}
func (c dbCatalog) MediaCapacity(ctx context.Context, mediaType string) (int64, error) {
	return db.GetMediaTypeCapacity(ctx, c.pool, mediaType)
}
