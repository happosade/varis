package toc

import (
	"testing"
	"time"
)

func TestMarshalAndParse_RoundTrip(t *testing.T) {
	orig := TOC{
		DiskID:        "BD:0001",
		MediaType:     "BD-R",
		ParityPercent: 10,
		Role:          "data",
		CreatedAt:     time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC),
		Files: []FileEntry{
			{Path: "photos/a.jpg", SizeBytes: 1024, SHA256: "abc123"},
		},
	}
	orig.Compressed = true

	data, err := orig.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.DiskID != orig.DiskID || len(got.Files) != 1 || got.Files[0].Path != "photos/a.jpg" {
		t.Errorf("Parse round-trip = %+v, want match of %+v", got, orig)
	}
	if !got.Compressed {
		t.Error("Parse round-trip lost Compressed=true")
	}
}
