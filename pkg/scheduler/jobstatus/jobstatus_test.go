package jobstatus_test

import (
	"context"
	"errors"
	"testing"

	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/scheduler/indexer"
	"github.com/openSUSE/bsgo/pkg/scheduler/jobstatus"
	"github.com/openSUSE/bsgo/pkg/storage"
)

type fakeIntegrator struct {
	err   error
	calls []indexer.Finished
}

func (f *fakeIntegrator) Integrate(_ context.Context, fin indexer.Finished) (bool, error) {
	f.calls = append(f.calls, fin)
	return true, f.err
}

type fakeDirty struct{ prpas []buildrepo.Prpa }

func (f *fakeDirty) JobFinished(_ context.Context, p buildrepo.Prpa) error {
	f.prpas = append(f.prpas, p)
	return nil
}

func TestHandle(t *testing.T) {
	scheduled := &storage.PackageBuildState{
		Project: "p", Repo: "std", Arch: "x86_64", Package: "foo", Flavor: "mini",
		State: storage.StateScheduled, JobID: "job-1", SourceCommit: "c1", Meta: "m1",
	}
	tests := []struct {
		name        string
		msg         string
		integrErr   string
		wantState   string
		wantDetails string
		wantBuilt   string
		wantIntegr  int
		wantDirty   int
	}{
		{"building", `{"job_id":"job-1","project":"p","package":"foo:mini","target":"std","architecture":"x86_64","phase":"BUILDING"}`,
			"", storage.StateBuilding, "", "", 0, 0},
		{"success integrates then succeeds", `{"job_id":"job-1","project":"p","package":"foo:mini","target":"std","architecture":"x86_64","phase":"SUCCEEDED","meta":"m1","source_commit":"c1","artifacts":[{"filename":"bininfo.json","s3_key":"k"}]}`,
			"", storage.StateSucceeded, "", "m1", 1, 1},
		{"failed integration fails the job", `{"job_id":"job-1","project":"p","package":"foo:mini","target":"std","architecture":"x86_64","phase":"SUCCEEDED","meta":"m1"}`,
			"disk full", storage.StateFailed, "indexing failed: disk full", "", 1, 1},
		{"failure with reason", `{"job_id":"job-1","project":"p","package":"foo:mini","target":"std","architecture":"x86_64","phase":"FAILED","reason":"build failed","exit_code":1}`,
			"", storage.StateFailed, "build failed", "", 0, 1},
		{"failure with exit code only", `{"job_id":"job-1","project":"p","package":"foo:mini","target":"std","architecture":"x86_64","phase":"FAILED","exit_code":2}`,
			"", storage.StateFailed, "exit code 2", "", 0, 1},
		{"aborted", `{"job_id":"job-1","project":"p","package":"foo:mini","target":"std","architecture":"x86_64","phase":"ABORTED","reason":"timeout"}`,
			"", storage.StateFailed, "aborted: timeout", "", 0, 1},
		{"requeued after worker loss is scheduled again", `{"job_id":"job-1","project":"p","package":"foo:mini","target":"std","architecture":"x86_64","phase":"REQUEUED","reason":"WORKER_LEASE_EXPIRED"}`,
			"", storage.StateScheduled, "requeued: WORKER_LEASE_EXPIRED", "", 0, 0},
		{"superseded job is ignored", `{"job_id":"old-job","project":"p","package":"foo:mini","target":"std","architecture":"x86_64","phase":"FAILED","reason":"x"}`,
			"", storage.StateScheduled, "", "", 0, 0},
		{"pending is owned by the checker", `{"job_id":"job-1","project":"p","package":"foo:mini","target":"std","architecture":"x86_64","phase":"PENDING"}`,
			"", storage.StateScheduled, "", "", 0, 0},
		{"unknown unit is ignored", `{"job_id":"job-1","project":"p","package":"other","target":"std","architecture":"x86_64","phase":"FAILED"}`,
			"", storage.StateScheduled, "", "", 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := storage.NewMemoryStore()
			if err := store.UpsertBuildState(context.Background(), scheduled); err != nil {
				t.Fatal(err)
			}
			integ := &fakeIntegrator{}
			if tc.integrErr != "" {
				integ.err = errors.New(tc.integrErr)
			}
			dirty := &fakeDirty{}
			h := &jobstatus.Handler{Store: store, Integrator: integ, Dirty: dirty}
			if err := h.Handle(context.Background(), []byte(tc.msg)); err != nil {
				t.Fatal(err)
			}
			got, err := store.GetBuildState(context.Background(), "p", "std", "x86_64", "foo", "mini")
			if err != nil {
				t.Fatal(err)
			}
			if got.State != tc.wantState || got.Details != tc.wantDetails || got.BuiltMeta != tc.wantBuilt {
				t.Errorf("row %s %q built %q, want %s %q built %q", got.State, got.Details, got.BuiltMeta,
					tc.wantState, tc.wantDetails, tc.wantBuilt)
			}
			if len(integ.calls) != tc.wantIntegr || len(dirty.prpas) != tc.wantDirty {
				t.Errorf("integrate calls %d, dirty marks %d; want %d and %d",
					len(integ.calls), len(dirty.prpas), tc.wantIntegr, tc.wantDirty)
			}
			if len(integ.calls) == 1 && integ.calls[0].Unit != "foo:mini" {
				t.Errorf("integrated unit %q, want foo:mini", integ.calls[0].Unit)
			}
		})
	}
}

func TestHandleRejectsMalformedAndIncomplete(t *testing.T) {
	h := &jobstatus.Handler{Store: storage.NewMemoryStore(), Integrator: &fakeIntegrator{}, Dirty: &fakeDirty{}}
	if err := h.Handle(context.Background(), []byte("{")); err == nil {
		t.Error("malformed json must be an error")
	}
	msg := `{"job_id":"j","project":"p","package":"x","phase":"FAILED"}`
	if err := h.Handle(context.Background(), []byte(msg)); err == nil {
		t.Error("a status without target and architecture must be an error")
	}
}
