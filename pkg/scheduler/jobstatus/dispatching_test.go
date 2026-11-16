package jobstatus_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/openSUSE/bsgo/pkg/scheduler/jobstatus"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func TestDispatchingToBuilding(t *testing.T) {
	msg := func(job, phase string) string {
		return fmt.Sprintf(`{"job_id":%q,"project":"p","package":"foo","target":"std","architecture":"x86_64","phase":%q}`, job, phase)
	}
	tests := []struct {
		name  string
		steps []string
		want  string
	}{
		{"dispatch then worker start", []string{msg("j1", "DISPATCHING"), msg("j1", "BUILDING")}, storage.StateBuilding},
		{"dispatch alone stays dispatching", []string{msg("j1", "DISPATCHING")}, storage.StateDispatching},
		{"late dispatch message does not undo the start", []string{msg("j1", "BUILDING"), msg("j1", "DISPATCHING")}, storage.StateBuilding},
		{"dispatch of a superseded job is ignored", []string{msg("old", "DISPATCHING")}, storage.StateScheduled},
		{"requeue of a dispatching job", []string{msg("j1", "DISPATCHING"), msg("j1", "REQUEUED")}, storage.StateScheduled},
		{"failure of a dispatching job", []string{msg("j1", "DISPATCHING"), msg("j1", "FAILED")}, storage.StateFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := storage.NewMemoryStore()
			row := &storage.PackageBuildState{Project: "p", Repo: "std", Arch: "x86_64", Package: "foo",
				State: storage.StateScheduled, JobID: "j1"}
			if err := store.UpsertBuildState(ctx, row); err != nil {
				t.Fatal(err)
			}
			h := &jobstatus.Handler{Store: store, Integrator: &fakeIntegrator{}, Dirty: &fakeDirty{}}
			for _, s := range tc.steps {
				if err := h.Handle(ctx, []byte(s)); err != nil {
					t.Fatal(err)
				}
			}
			got, err := store.GetBuildState(ctx, "p", "std", "x86_64", "foo", "")
			if err != nil {
				t.Fatal(err)
			}
			if got.State != tc.want {
				t.Errorf("state %s, want %s", got.State, tc.want)
			}
		})
	}
}
