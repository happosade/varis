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
	IsDryRun      bool
}

func InsertDiskGroup(ctx context.Context, pool *pgxpool.Pool, burnJobID string, groupSize int) (string, error) {
	var id string
	err := pool.QueryRow(ctx,
		`INSERT INTO disk_groups (burn_job_id, group_size) VALUES ($1, $2) RETURNING id`,
		burnJobID, groupSize).Scan(&id)
	return id, err
}

// NextDiskID returns the next sequential ID for a media-type prefix, e.g. "BD:0007".
// alreadyAllocated is the number of IDs already handed out for this prefix
// earlier in the same planning pass but not yet inserted into the DB (see
// planJob, which calls this once per disc in a job before any of them are
// burned/inserted) — without it, every call within one pass would see the
// same DB count and return the same ID. Safe without locking here because
// the burn pipeline (see plan 01) only ever runs one job at a time.
func NextDiskID(ctx context.Context, pool *pgxpool.Pool, prefix string, alreadyAllocated int) (string, error) {
	var count int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM disks WHERE id LIKE $1`,
		prefix+":%").Scan(&count)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s:%04d", prefix, count+alreadyAllocated+1), nil
}

func InsertDisk(ctx context.Context, pool *pgxpool.Pool, d Disk) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO disks (id, media_type, parity_percent, group_id, role, slot_index, iso_hash, is_dry_run)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		d.ID, d.MediaType, d.ParityPercent, d.GroupID, d.Role, d.SlotIndex, d.ISOHash, d.IsDryRun)
	return err
}

func GetDisk(ctx context.Context, pool *pgxpool.Pool, id string) (Disk, error) {
	var d Disk
	err := pool.QueryRow(ctx,
		`SELECT id, media_type, parity_percent, group_id, role, slot_index, iso_hash, created_at, is_dry_run
		 FROM disks WHERE id = $1`, id).
		Scan(&d.ID, &d.MediaType, &d.ParityPercent, &d.GroupID, &d.Role, &d.SlotIndex, &d.ISOHash, &d.CreatedAt, &d.IsDryRun)
	if errors.Is(err, pgx.ErrNoRows) {
		return Disk{}, fmt.Errorf("disk %s not found", id)
	}
	return d, err
}

func GroupMembers(ctx context.Context, pool *pgxpool.Pool, groupID string) ([]Disk, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, media_type, parity_percent, group_id, role, slot_index, iso_hash, created_at, is_dry_run
		 FROM disks WHERE group_id = $1 ORDER BY slot_index`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Disk
	for rows.Next() {
		var d Disk
		if err := rows.Scan(&d.ID, &d.MediaType, &d.ParityPercent, &d.GroupID, &d.Role, &d.SlotIndex, &d.ISOHash, &d.CreatedAt, &d.IsDryRun); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeleteDryRunDisc removes a dry-run disc's file and disk rows together.
// Scoped to is_dry_run = true throughout, so calling it with a real disc's
// ID is a safe no-op, never data loss. Files are deleted before the disk
// row (not wrapped in a transaction — this project doesn't use them
// elsewhere): if the process dies between the two statements, the worst
// case is an orphaned disk row with no files, which a second call to
// DeleteDryRunDisc cleans up (its own file-delete is a no-op, and the
// disk row still matches is_dry_run = true).
func DeleteDryRunDisc(ctx context.Context, pool *pgxpool.Pool, diskID string) error {
	_, err := pool.Exec(ctx,
		`DELETE FROM files WHERE disk_id = $1 AND EXISTS (
		     SELECT 1 FROM disks WHERE id = $1 AND is_dry_run
		 )`, diskID)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `DELETE FROM disks WHERE id = $1 AND is_dry_run`, diskID)
	return err
}

// ListDryRunDisks returns every disc flagged is_dry_run, newest first, for
// the /dryruns management page.
func ListDryRunDisks(ctx context.Context, pool *pgxpool.Pool) ([]Disk, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, media_type, parity_percent, group_id, role, slot_index, iso_hash, created_at, is_dry_run
		 FROM disks WHERE is_dry_run ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Disk
	for rows.Next() {
		var d Disk
		if err := rows.Scan(&d.ID, &d.MediaType, &d.ParityPercent, &d.GroupID, &d.Role, &d.SlotIndex, &d.ISOHash, &d.CreatedAt, &d.IsDryRun); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
