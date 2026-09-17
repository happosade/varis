CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE IF NOT EXISTS media_types (
    name VARCHAR(30) PRIMARY KEY,
    capacity_bytes BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS disk_groups (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    burn_job_id UUID NOT NULL,
    group_size INT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS disks (
    id VARCHAR(50) PRIMARY KEY,
    media_type VARCHAR(30) REFERENCES media_types(name),
    parity_percent SMALLINT NOT NULL,
    group_id UUID REFERENCES disk_groups(id),
    role VARCHAR(10) NOT NULL DEFAULT 'data',
    slot_index SMALLINT,
    iso_hash VARCHAR(256),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS files (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    disk_id VARCHAR(50) REFERENCES disks(id),
    original_path TEXT NOT NULL,
    size_bytes BIGINT NOT NULL,
    file_hash VARCHAR(256),
    archived_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_files_path ON files USING gin (original_path gin_trgm_ops);

CREATE TABLE IF NOT EXISTS staged_metadata (
    path        TEXT PRIMARY KEY,
    tags        TEXT[] NOT NULL DEFAULT '{}',
    description TEXT NOT NULL DEFAULT '',
    updated_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

ALTER TABLE files ADD COLUMN IF NOT EXISTS tags TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE files ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_files_tags ON files USING gin (tags);

ALTER TABLE disks ADD COLUMN IF NOT EXISTS is_dry_run BOOLEAN NOT NULL DEFAULT false;
