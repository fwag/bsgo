package jobstatus_test

import (
	"context"
	"testing"

	"github.com/openSUSE/bsgo/pkg/scheduler/jobstatus"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// A late ABORTED or REQUEUED message must not drag a unit that the checker
// has already moved on back to scheduled.
func TestLostJobDoesNotOverwriteMovedOnUnit(t *testing.T) {
	phases := []string{"ABORTED", "REQUEUED"}
	states := []string{storage.StateBlocked, storage.StateFailed, storage.StateSucceeded}
	for _, phase := range phases {
		for _, state := range states {
			t.Run(phase+"/"+state, func(t *testing.T) {
				store := storage.NewMemoryStore()
				row := &storage.PackageBuildState{
					Project: "p", Repo: "std", Arch: "x86_64", Package: "foo",
					State: state, Details: "kept",
				}
				if err := store.UpsertBuildState(context.Background(), row); err != nil {
					t.Fatal(err)
				}
				h := &jobstatus.Handler{Store: store, Integrator: &fakeIntegrator{}, Dirty: &fakeDirty{}}
				msg := `{"job_id":"j","project":"p","package":"foo","target":"std","architecture":"x86_64","phase":"` + phase + `"}`
				if err := h.Handle(context.Background(), []byte(msg)); err != nil {
					t.Fatal(err)
				}
				got, err := store.GetBuildState(context.Background(), "p", "std", "x86_64", "foo", "")
				if err != nil {
					t.Fatal(err)
				}
				if got.State != state || got.Details != "kept" {
					t.Errorf("row %s %q, want %s kept", got.State, got.Details, state)
				}
			})
		}
	}
}
