package main

import (
	"context"
	"errors"
	"testing"

	"github.com/openSUSE/bsgo/pkg/scheduler"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func TestBuildStateFromStatus(t *testing.T) {
	exit2 := 2
	base := jobStatusMessage{JobID: "j1", Project: "p", Package: "pkg", Target: "std", Architecture: "a1"}
	with := func(phase, reason string, exit *int) *jobStatusMessage {
		m := base
		m.Phase, m.Reason, m.ExitCode = phase, reason, exit
		return &m
	}
	noArch := base
	noArch.Phase, noArch.Architecture = "BUILDING", ""
	noTarget := base
	noTarget.Phase, noTarget.Target = "BUILDING", ""

	tests := []struct {
		name        string
		msg         *jobStatusMessage
		wantState   string
		wantDetails string
		wantErr     bool
		wantSkip    bool
	}{
		{"pending", with("PENDING", "", nil), storage.StateScheduled, "", false, false},
		{"building", with("BUILDING", "", nil), storage.StateBuilding, "", false, false},
		{"succeeded", with("SUCCEEDED", "", nil), storage.StateSucceeded, "", false, false},
		{"failed with exit code", with("FAILED", "", &exit2), storage.StateFailed, "exit code 2", false, false},
		{"failed with reason", with("FAILED", "boom", &exit2), storage.StateFailed, "boom", false, false},
		{"aborted", with("ABORTED", "WORKER_LEASE_EXPIRED", nil), storage.StateFailed, "aborted: WORKER_LEASE_EXPIRED", false, false},
		{"unresolvable", with("UNRESOLVABLE", "no provider", nil), storage.StateUnresolvable, "no provider", false, false},
		{"unknown phase", with("WEIRD", "", nil), "", "", false, true},
		{"missing arch", &noArch, "", "", true, false},
		{"missing target", &noTarget, "", "", true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row, err := buildStateFromStatus(tc.msg)
			switch {
			case tc.wantSkip:
				if !errors.Is(err, errNoStateForPhase) {
					t.Fatalf("expected skip, got %v", err)
				}
			case tc.wantErr:
				if err == nil || errors.Is(err, errNoStateForPhase) {
					t.Fatalf("expected validation error, got %v", err)
				}
			default:
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if row.State != tc.wantState || row.Details != tc.wantDetails || row.Repo != "std" || row.Arch != "a1" {
					t.Errorf("unexpected row: %+v", row)
				}
			}
		})
	}
}

func TestRecordBuildStateAndGatekeeper(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore()

	messages := []string{
		`{"job_id":"j","project":"p","package":"a","target":"std","architecture":"x","phase":"SUCCEEDED"}`,
		`{"job_id":"j","project":"p","package":"a","target":"std","architecture":"x","phase":"PENDING"}`,
		`{"job_id":"k","project":"p","package":"b","target":"std","architecture":"x","phase":"UNRESOLVABLE","reason":"nope"}`,
		`{"job_id":"m","project":"p","package":"c","phase":"BUILDING"}`,
		`not json`,
	}
	for _, m := range messages {
		recordBuildState(ctx, store, []byte(m))
	}

	counts, err := store.CountBuildStates(ctx, "p", storage.BuildStateFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if counts[storage.StateScheduled] != 1 || counts[storage.StateUnresolvable] != 1 || len(counts) != 2 {
		t.Fatalf("unexpected counts: %v", counts)
	}

	gk := scheduler.NewMemoryGatekeeper()
	applyStateToGatekeeper(gk, &storage.PackageBuildState{Project: "p", Package: "b", State: storage.StateUnresolvable})
	applyStateToGatekeeper(gk, &storage.PackageBuildState{Project: "p", Package: "a", State: storage.StateBuilding})
	if !gk.IsUnresolvable("p", "b") || !gk.IsBuilding("p", "a") {
		t.Errorf("gatekeeper not seeded from states")
	}
}
