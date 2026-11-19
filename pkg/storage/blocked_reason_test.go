package storage_test

import (
	"context"
	"testing"

	"github.com/openSUSE/bsgo/pkg/storage"
)

func producer(unit, state string) storage.BlockedProducer {
	return storage.BlockedProducer{Unit: unit, State: state}
}

func TestBlockedReasonSummary(t *testing.T) {
	cycle := storage.BlockedCycle{InFlight: []storage.BlockedProducer{producer("gcc15", "building")}}
	tests := []struct {
		name string
		r    storage.BlockedReason
		want string
	}{
		{"one producer", storage.BlockedReason{Producers: []storage.BlockedProducer{producer("perl", "scheduled")}},
			"blocked: perl (scheduled)"},
		{"cycle only", storage.BlockedReason{Cycles: []storage.BlockedCycle{cycle}},
			"blocked: dependency cycle not finished (gcc15 building)"},
		{"mixed", storage.BlockedReason{
			Producers: []storage.BlockedProducer{producer("libxml2", "building")},
			Cycles:    []storage.BlockedCycle{cycle}},
			"blocked: libxml2 (building), dependency cycle not finished (gcc15 building)"},
		{"cropped", storage.BlockedReason{Producers: []storage.BlockedProducer{
			producer("a", "building"), producer("b", "building"), producer("c", "scheduled"),
			producer("d", "scheduled"), producer("e", "blocked")}},
			"blocked: a (building), b (building), c (scheduled) +2 more"},
		{"cycle members cropped", storage.BlockedReason{Cycles: []storage.BlockedCycle{{InFlight: []storage.BlockedProducer{
			producer("a", "building"), producer("b", "building"), producer("c", "building"), producer("d", "scheduled")}}}},
			"blocked: dependency cycle not finished (a building, b building, c building, +1 more)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.r.Summary(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBlockedReasonEqual(t *testing.T) {
	a := &storage.BlockedReason{Producers: []storage.BlockedProducer{producer("a", "building")}}
	b := &storage.BlockedReason{Producers: []storage.BlockedProducer{producer("a", "scheduled")}}
	if !a.Equal(a) || a.Equal(b) || a.Equal(nil) {
		t.Error("Equal does not compare the reasons")
	}
}

func TestMemoryStoreKeepsBlockedReason(t *testing.T) {
	ctx := context.Background()
	s := storage.NewMemoryStore()
	reason := &storage.BlockedReason{Producers: []storage.BlockedProducer{producer("a", "building")}}
	row := &storage.PackageBuildState{Project: "p", Repo: "r", Arch: "a", Package: "x",
		State: storage.StateBlocked, Details: reason.Summary(), Blocked: reason}
	if err := s.UpsertBuildState(ctx, row); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetBuildState(ctx, "p", "r", "a", "x", "")
	if err != nil || !got.Blocked.Equal(reason) {
		t.Fatalf("blocked reason lost: %+v, %v", got, err)
	}
}
