package postgres_test

import (
	"context"
	"testing"

	"github.com/openSUSE/bsgo/pkg/storage"
)

func TestPostgresBlockedReasonRoundTrip(t *testing.T) {
	store, prj := newCheckerStore(t)
	ctx := context.Background()
	reason := &storage.BlockedReason{
		Producers: []storage.BlockedProducer{{Unit: "perl", State: "scheduled", Binaries: []string{"perl"}}},
		Cycles: []storage.BlockedCycle{{
			Held:     []storage.BlockedProducer{{Unit: "zlib", State: "succeeded"}},
			InFlight: []storage.BlockedProducer{{Unit: "gcc15", State: "building"}},
		}},
	}
	row := &storage.PackageBuildState{Project: prj, Repo: "std", Arch: "x86_64", Package: "x",
		State: storage.StateBlocked, Details: reason.Summary(), Blocked: reason}
	if err := store.UpsertBuildState(ctx, row); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetBuildState(ctx, prj, "std", "x86_64", "x", "")
	if err != nil || !got.Blocked.Equal(reason) {
		t.Fatalf("blocked reason lost: %+v, %v", got, err)
	}
	row.State, row.Details, row.Blocked = storage.StateScheduled, "new build", nil
	if err := store.UpsertBuildStates(ctx, []*storage.PackageBuildState{row}); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListBuildStates(ctx, prj, storage.BuildStateFilter{})
	if err != nil || len(list) != 1 || list[0].Blocked != nil {
		t.Fatalf("blocked reason not cleared: %+v, %v", list, err)
	}
}
