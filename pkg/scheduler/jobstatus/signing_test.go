package jobstatus_test

import (
	"context"
	"errors"
	"testing"

	"github.com/openSUSE/bsgo/pkg/scheduler/indexer"
	"github.com/openSUSE/bsgo/pkg/scheduler/jobstatus"
	"github.com/openSUSE/bsgo/pkg/signing"
	"github.com/openSUSE/bsgo/pkg/storage"
)

type fakeSigning struct {
	enabled    bool
	enabledErr error
	requestErr error
	requests   []signing.Job
}

func (f *fakeSigning) Enabled(context.Context, string) (bool, error) { return f.enabled, f.enabledErr }

func (f *fakeSigning) Request(_ context.Context, j signing.Job) error {
	f.requests = append(f.requests, j)
	return f.requestErr
}

const (
	succeededWithRPM = `{"job_id":"job-1","project":"p","package":"foo:mini","target":"std","architecture":"x86_64","phase":"SUCCEEDED","meta":"m1","source_commit":"c1","artifacts":[{"filename":"foo.rpm","s3_key":"k/foo.rpm","sha256":"aa"},{"filename":"bininfo.json","s3_key":"k/bininfo.json","sha256":"bb"}]}`
	succeededNoRPM   = `{"job_id":"job-1","project":"p","package":"foo:mini","target":"std","architecture":"x86_64","phase":"SUCCEEDED","meta":"m1","artifacts":[{"filename":"log.txt","s3_key":"k/log.txt"}]}`
)

func TestHandleRoutesSuccessThroughSigning(t *testing.T) {
	tests := []struct {
		name        string
		msg         string
		sign        fakeSigning
		wantState   string
		wantDetails string
		wantRequest int
		wantIntegr  int
		wantDirty   int
	}{
		{"signing project waits for the signer", succeededWithRPM, fakeSigning{enabled: true},
			storage.StateSigning, "", 1, 0, 0},
		{"project without signing integrates at once", succeededWithRPM, fakeSigning{enabled: false},
			storage.StateSucceeded, "", 0, 1, 1},
		{"job without RPMs is not signed", succeededNoRPM, fakeSigning{enabled: true},
			storage.StateSucceeded, "", 0, 1, 1},
		{"policy error fails the job", succeededWithRPM, fakeSigning{enabledErr: errors.New("db down")},
			storage.StateFailed, "signing failed: db down", 0, 0, 1},
		{"lost request fails the job", succeededWithRPM, fakeSigning{enabled: true, requestErr: errors.New("nats closed")},
			storage.StateFailed, "signing failed: nats closed", 1, 0, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, integ, dirty := newScheduledStore(t)
			sign := tc.sign
			h := &jobstatus.Handler{Store: store, Integrator: integ, Dirty: dirty, Signing: &sign}
			if err := h.Handle(context.Background(), []byte(tc.msg)); err != nil {
				t.Fatal(err)
			}
			got := rowOf(t, store)
			if got.State != tc.wantState || got.Details != tc.wantDetails {
				t.Errorf("row %s %q, want %s %q", got.State, got.Details, tc.wantState, tc.wantDetails)
			}
			if len(sign.requests) != tc.wantRequest || len(integ.calls) != tc.wantIntegr || len(dirty.prpas) != tc.wantDirty {
				t.Errorf("requests %d, integrations %d, dirty %d; want %d, %d, %d",
					len(sign.requests), len(integ.calls), len(dirty.prpas), tc.wantRequest, tc.wantIntegr, tc.wantDirty)
			}
			if len(sign.requests) == 1 {
				r := sign.requests[0]
				if r.JobID != "job-1" || r.Package != "foo:mini" || r.Target != "std" || len(r.Artifacts) != 2 || r.Meta != "m1" {
					t.Errorf("request %+v lacks the job identity or artifacts", r)
				}
			}
		})
	}
}

func signedMessage(t *testing.T, mutate func(*signing.Job)) []byte {
	t.Helper()
	j := signing.Job{
		JobID: "job-1", Project: "p", Package: "foo:mini", Target: "std", Architecture: "x86_64",
		Meta: "m1", SourceCommit: "c1",
		Artifacts: []indexer.Artifact{{Filename: "foo.rpm", S3Key: "k/foo.rpm", SHA256: "signed"}},
	}
	if mutate != nil {
		mutate(&j)
	}
	data, err := signing.Encode(j)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestHandleSigned(t *testing.T) {
	tests := []struct {
		name        string
		startState  string
		mutate      func(*signing.Job)
		integrErr   error
		wantState   string
		wantDetails string
		wantBuilt   string
		wantIntegr  int
		wantDirty   int
	}{
		{"signed job is integrated and succeeds", storage.StateSigning, nil, nil,
			storage.StateSucceeded, "", "m1", 1, 1},
		{"finished job (crash before integration) is retried", storage.StateFinished, nil, nil,
			storage.StateSucceeded, "", "m1", 1, 1},
		{"signing failure fails the job", storage.StateSigning,
			func(j *signing.Job) { j.Error = "rpm -K failed" }, nil,
			storage.StateFailed, "signing failed: rpm -K failed", "", 0, 1},
		{"integration failure fails the job", storage.StateSigning, nil, errors.New("disk full"),
			storage.StateFailed, "indexing failed: disk full", "", 1, 1},
		{"superseded job is ignored", storage.StateSigning,
			func(j *signing.Job) { j.JobID = "other" }, nil, storage.StateSigning, "", "", 0, 0},
		{"redelivery after success is ignored", storage.StateSucceeded, nil, nil,
			storage.StateSucceeded, "", "m1", 0, 0},
		{"answer for a rebuilding unit is ignored", storage.StateBuilding, nil, nil,
			storage.StateBuilding, "", "", 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, integ, dirty := newScheduledStore(t)
			integ.err = tc.integrErr
			row := rowOf(t, store)
			row.State = tc.startState
			if err := store.UpsertBuildState(context.Background(), row); err != nil {
				t.Fatal(err)
			}
			h := &jobstatus.Handler{Store: store, Integrator: integ, Dirty: dirty, Signing: &fakeSigning{}}
			if err := h.HandleSigned(context.Background(), signedMessage(t, tc.mutate)); err != nil {
				t.Fatal(err)
			}
			got := rowOf(t, store)
			if got.State != tc.wantState || got.Details != tc.wantDetails || got.BuiltMeta != tc.wantBuilt {
				t.Errorf("row %s %q built %q, want %s %q built %q", got.State, got.Details, got.BuiltMeta,
					tc.wantState, tc.wantDetails, tc.wantBuilt)
			}
			if len(integ.calls) != tc.wantIntegr || len(dirty.prpas) != tc.wantDirty {
				t.Errorf("integrations %d, dirty %d; want %d, %d", len(integ.calls), len(dirty.prpas), tc.wantIntegr, tc.wantDirty)
			}
			if len(integ.calls) == 1 && integ.calls[0].Artifacts[0].SHA256 != "signed" {
				t.Errorf("integrated %+v, want the signed artifacts of the answer", integ.calls[0].Artifacts)
			}
		})
	}
}

func TestHandleSignedRejectsMalformed(t *testing.T) {
	h := &jobstatus.Handler{Store: storage.NewMemoryStore(), Integrator: &fakeIntegrator{}, Dirty: &fakeDirty{}}
	for _, msg := range []string{"{", `{"job_id":"j"}`} {
		if err := h.HandleSigned(context.Background(), []byte(msg)); err == nil {
			t.Errorf("%q must be an error", msg)
		}
	}
}

func newScheduledStore(t *testing.T) (*storage.MemoryStore, *fakeIntegrator, *fakeDirty) {
	t.Helper()
	store := storage.NewMemoryStore()
	err := store.UpsertBuildState(context.Background(), &storage.PackageBuildState{
		Project: "p", Repo: "std", Arch: "x86_64", Package: "foo", Flavor: "mini",
		State: storage.StateScheduled, JobID: "job-1", SourceCommit: "c1", Meta: "m1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, &fakeIntegrator{}, &fakeDirty{}
}

func rowOf(t *testing.T, store *storage.MemoryStore) *storage.PackageBuildState {
	t.Helper()
	got, err := store.GetBuildState(context.Background(), "p", "std", "x86_64", "foo", "mini")
	if err != nil {
		t.Fatal(err)
	}
	return got
}
