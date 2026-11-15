package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/openSUSE/bsgo/pkg/scheduler"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// errNoStateForPhase marks status messages that do not map to a persisted build state.
var errNoStateForPhase = errors.New("phase does not map to a build state")

// jobStatusMessage is the subset of a bsgo.status message needed to persist build state.
type jobStatusMessage struct {
	JobID        string `json:"job_id"`
	Project      string `json:"project"`
	Package      string `json:"package"`
	Target       string `json:"target"`
	Architecture string `json:"architecture"`
	Phase        string `json:"phase"`
	Reason       string `json:"reason"`
	ExitCode     *int   `json:"exit_code"`
}

var phaseToState = map[string]string{
	"PENDING":      storage.StateScheduled,
	"BUILDING":     storage.StateBuilding,
	"SUCCEEDED":    storage.StateSucceeded,
	"FAILED":       storage.StateFailed,
	"ABORTED":      storage.StateFailed,
	"UNRESOLVABLE": storage.StateUnresolvable,
}

// buildStateFromStatus converts a status message into a package_build_state row.
// It never invents a repo or arch: messages lacking them are rejected.
func buildStateFromStatus(m *jobStatusMessage) (*storage.PackageBuildState, error) {
	state, ok := phaseToState[m.Phase]
	if !ok || m.Project == "" || m.Package == "" {
		return nil, errNoStateForPhase
	}
	if m.Target == "" || m.Architecture == "" {
		return nil, fmt.Errorf("status for %s/%s (job %s, phase %s) lacks target or architecture",
			m.Project, m.Package, m.JobID, m.Phase)
	}
	return &storage.PackageBuildState{
		Project: m.Project,
		Repo:    m.Target,
		Arch:    m.Architecture,
		Package: m.Package,
		State:   state,
		Details: statusDetails(m),
		JobID:   m.JobID,
	}, nil
}

func statusDetails(m *jobStatusMessage) string {
	switch m.Phase {
	case "ABORTED":
		if m.Reason == "" {
			return "aborted"
		}
		return "aborted: " + m.Reason
	case "FAILED":
		if m.Reason != "" {
			return m.Reason
		}
		if m.ExitCode != nil {
			return fmt.Sprintf("exit code %d", *m.ExitCode)
		}
	case "UNRESOLVABLE":
		return m.Reason
	}
	return ""
}

// recordBuildState persists the state carried by a raw bsgo.status message.
func recordBuildState(ctx context.Context, store storage.Store, data []byte) {
	if store == nil {
		return
	}
	var msg jobStatusMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		fmt.Fprintf(os.Stderr, "[%s] [STATE] Ignoring malformed status message: %v\n", time.Now().Format("15:04:05"), err)
		return
	}
	row, err := buildStateFromStatus(&msg)
	if errors.Is(err, errNoStateForPhase) {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] [STATE] Skipping build state write: %v\n", time.Now().Format("15:04:05"), err)
		return
	}
	if err := store.UpsertBuildState(ctx, row); err != nil {
		fmt.Fprintf(os.Stderr, "[%s] [STATE] Failed to persist build state for %s/%s: %v\n",
			time.Now().Format("15:04:05"), row.Project, row.Package, err)
	}
}

// loadGatekeeperFromStore seeds the in-memory gatekeeper from persisted build states.
func loadGatekeeperFromStore(ctx context.Context, store storage.Store, gk *scheduler.MemoryGatekeeper) {
	if store == nil || gk == nil {
		return
	}
	prjs, err := store.ListProjects(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] [GATEKEEPER] Failed to list projects: %v\n", time.Now().Format("15:04:05"), err)
		return
	}
	count := 0
	for _, p := range prjs {
		states, err := store.ListBuildStates(ctx, p.Project.Name, storage.BuildStateFilter{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "[%s] [GATEKEEPER] Failed to load build states for %s: %v\n",
				time.Now().Format("15:04:05"), p.Project.Name, err)
			continue
		}
		for _, st := range states {
			applyStateToGatekeeper(gk, st)
			count++
		}
	}
	if count > 0 {
		fmt.Printf("[%s] [GATEKEEPER] Loaded %d package build states\n", time.Now().Format("15:04:05"), count)
	}
}

func applyStateToGatekeeper(gk *scheduler.MemoryGatekeeper, st *storage.PackageBuildState) {
	switch st.State {
	case storage.StateBuilding:
		gk.SetBuilding(st.Project, st.Package, true)
	case storage.StateUnresolvable:
		gk.SetUnresolvable(st.Project, st.Package, true)
	}
}
