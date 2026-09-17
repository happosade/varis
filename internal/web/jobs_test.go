package web

import (
	"testing"

	"varis/internal/burn"
)

func TestJobViewFromJob_IndexClamp(t *testing.T) {
	plans := []burn.DiscPlan{
		{DiskID: "BD:0001"},
		{DiskID: "BD:0002"},
		{DiskID: "BD:0003"},
	}

	tests := []struct {
		name           string
		currentIndex   int
		wantIndex1     int
		wantTotal      int
		wantCurrentDsk string
	}{
		{
			name:           "in-progress index within bounds needs no clamping",
			currentIndex:   1,
			wantIndex1:     2,
			wantTotal:      3,
			wantCurrentDsk: "BD:0002",
		},
		{
			name:           "DONE state's CurrentIndex == len(Plans) clamps to the last disc",
			currentIndex:   3,
			wantIndex1:     4,
			wantTotal:      3,
			wantCurrentDsk: "BD:0003",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := &burn.Job{
				Plans:        plans,
				CurrentIndex: tt.currentIndex,
				State:        burn.StateDone,
			}
			got := jobViewFromJob(job)
			if got.CurrentIndex1 != tt.wantIndex1 {
				t.Errorf("CurrentIndex1 = %d, want %d", got.CurrentIndex1, tt.wantIndex1)
			}
			if got.TotalDiscs != tt.wantTotal {
				t.Errorf("TotalDiscs = %d, want %d", got.TotalDiscs, tt.wantTotal)
			}
			if got.CurrentDiskID != tt.wantCurrentDsk {
				t.Errorf("CurrentDiskID = %q, want %q", got.CurrentDiskID, tt.wantCurrentDsk)
			}
		})
	}
}
