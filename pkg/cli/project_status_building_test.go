package cli

import (
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/storage"
)

func TestBuildingSeconds(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		state string
		since time.Duration
		want  int64
	}{
		{"building for 47m", storage.StateBuilding, 47 * time.Minute, 47 * 60},
		{"succeeded has no duration", storage.StateSucceeded, time.Hour, 0},
		{"clock skew is clamped", storage.StateBuilding, -time.Minute, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &storage.PackageBuildState{State: tt.state, UpdatedAt: now.Add(-tt.since)}
			if got := buildingSeconds(st, now); got != tt.want {
				t.Errorf("buildingSeconds = %d, want %d", got, tt.want)
			}
		})
	}
}
