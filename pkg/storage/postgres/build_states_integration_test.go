package postgres_test

import (
	"context"
	"testing"

	"github.com/openSUSE/bsgo/pkg/storage"
)

// Every state in ValidBuildStates must pass the CHECK constraint of migration
// 00009, an unknown one must not.
func TestPostgresAcceptsEveryBuildState(t *testing.T) {
	store, prj := newCheckerStore(t)
	ctx := context.Background()
	for _, state := range storage.ValidBuildStates {
		row := &storage.PackageBuildState{Project: prj, Repo: "std", Arch: "x86_64", Package: "p", State: state}
		if err := store.UpsertBuildState(ctx, row); err != nil {
			t.Errorf("state %q rejected: %v", state, err)
		}
	}
	bad := &storage.PackageBuildState{Project: prj, Repo: "std", Arch: "x86_64", Package: "p", State: "bogus"}
	if err := store.UpsertBuildState(ctx, bad); err == nil {
		t.Error("unknown state must violate the CHECK constraint")
	}
}
