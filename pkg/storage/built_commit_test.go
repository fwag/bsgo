package storage_test

import (
	"context"
	"testing"

	"github.com/openSUSE/bsgo/pkg/storage"
)

// TestBuiltCommitFollowsSuccessOnly: the commit of the last build is only
// recorded when a build succeeded; scheduling a newer commit, or failing it,
// leaves it alone.
func TestBuiltCommitFollowsSuccessOnly(t *testing.T) {
	ctx := context.Background()
	s := storage.NewMemoryStore()
	steps := []struct {
		state, commit, meta string
		wantBuilt           string
	}{
		{storage.StateScheduled, "c1", "m1", ""},
		{storage.StateSucceeded, "c1", "m1", "c1"},
		{storage.StateScheduled, "c2", "m2", "c1"},
		{storage.StateFailed, "c2", "m2", "c1"},
		{storage.StateBlocked, "", "", "c1"},
		{storage.StateSucceeded, "c2", "m2", "c2"},
	}
	for i, st := range steps {
		row := &storage.PackageBuildState{
			Project: "p", Repo: "r", Arch: "a", Package: "x",
			State: st.state, SourceCommit: st.commit, Meta: st.meta,
		}
		if err := s.UpsertBuildState(ctx, row); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetBuildState(ctx, "p", "r", "a", "x", "")
		if err != nil {
			t.Fatal(err)
		}
		if got.BuiltCommit != st.wantBuilt {
			t.Errorf("step %d (%s %s): built commit %q, want %q", i, st.state, st.commit, got.BuiltCommit, st.wantBuilt)
		}
	}
}
