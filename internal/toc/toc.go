package toc

import (
	"encoding/json"
	"time"
)

type FileEntry struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256,omitempty"`
}

// TOC is the on-disc header written alongside every burned disc's payload,
// mirroring an LTO-FS style table of contents.
type TOC struct {
	DiskID        string      `json:"disk_id"`
	MediaType     string      `json:"media_type"`
	ParityPercent int         `json:"parity_percent"`
	GroupID       string      `json:"group_id,omitempty"`
	Role          string      `json:"role"`
	SlotIndex     int         `json:"slot_index,omitempty"`
	CreatedAt     time.Time   `json:"created_at"`
	Files         []FileEntry `json:"files"`
}

func (t TOC) Marshal() ([]byte, error) {
	return json.MarshalIndent(t, "", "  ")
}

func Parse(data []byte) (TOC, error) {
	var t TOC
	err := json.Unmarshal(data, &t)
	return t, err
}
