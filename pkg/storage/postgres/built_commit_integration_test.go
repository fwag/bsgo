package postgres_test

import (
	"context"
	"testing"

	"github.com/openSUSE/bsgo/pkg/storage"
)

func TestPostgresBuiltCommitFollowsSuccessOnly(t *testing.T) {
	store, prj := newCheckerStore(t)
	ctx := context.Background()
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
			Project: prj, Repo: "std", Arch: "x86_64", Package: "x",
			State: st.state, SourceCommit: st.commit, Meta: st.meta,
		}
		if err := store.UpsertBuildState(ctx, row); err != nil {
			t.Fatal(err)
		}
		got, err := store.GetBuildState(ctx, prj, "std", "x86_64", "x", "")
		if err != nil {
			t.Fatal(err)
		}
		if got.BuiltCommit != st.wantBuilt {
			t.Errorf("step %d (%s %s): built commit %q, want %q", i, st.state, st.commit, got.BuiltCommit, st.wantBuilt)
		}
	}
}
