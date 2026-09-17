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
