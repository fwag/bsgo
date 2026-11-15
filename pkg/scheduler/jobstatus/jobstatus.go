// Package jobstatus turns the bsgo.status messages of workers into build
// state rows, and integrates the binaries of successful jobs into the build
// repository before the success becomes visible. The checker owns every other
// state transition; this is the only writer of building, succeeded and failed.
package jobstatus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/scheduler/indexer"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// Message is the subset of a bsgo.status message the handler needs.
type Message struct {
	JobID        string             `json:"job_id"`
	Project      string             `json:"project"`
	Package      string             `json:"package"`
	Target       string             `json:"target"`
	Architecture string             `json:"architecture"`
	Phase        string             `json:"phase"`
	Reason       string             `json:"reason"`
	ExitCode     *int               `json:"exit_code"`
	SourceCommit string             `json:"source_commit"`
	Meta         string             `json:"meta"`
	Artifacts    []indexer.Artifact `json:"artifacts"`
}

// Store is the build state table; storage.Store satisfies it.
type Store interface {
	GetBuildState(ctx context.Context, project, repo, arch, pkg, flavor string) (*storage.PackageBuildState, error)
	UpsertBuildState(ctx context.Context, st *storage.PackageBuildState) error
}

// Integrator replaces a unit's binaries in the build repository.
type Integrator interface {
	Integrate(ctx context.Context, f indexer.Finished) (bool, error)
}

// Dirtier marks a prpa for a new checker pass.
type Dirtier interface {
	JobFinished(ctx context.Context, p buildrepo.Prpa) error
}

// Handler processes status messages.
type Handler struct {
	Store      Store
	Integrator Integrator
	Dirty      Dirtier
}

// Handle processes one raw status message. Messages that do not concern the
// build state (other phases, unknown units, superseded jobs) are ignored.
func (h *Handler) Handle(ctx context.Context, data []byte) error {
	var m Message
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("malformed status message: %w", err)
	}
	if !relevant(m.Phase) || m.Project == "" || m.Package == "" {
		return nil
	}
	if m.Target == "" || m.Architecture == "" {
		return fmt.Errorf("status of job %s (%s) lacks target or architecture", m.JobID, m.Phase)
	}
	p := buildrepo.Prpa{Project: m.Project, Repo: m.Target, Arch: m.Architecture}
	pkg, flavor := buildrepo.SplitUnit(m.Package)
	cur, err := h.Store.GetBuildState(ctx, p.Project, p.Repo, p.Arch, pkg, flavor)
	if errors.Is(err, storage.ErrNotFound) {
		return nil // not a job of the checker
	}
	if err != nil {
		return fmt.Errorf("read build state of %s/%s: %w", p, m.Package, err)
	}
	if cur.JobID != "" && cur.JobID != m.JobID {
		return nil // superseded job
	}
	row := h.nextRow(ctx, &m, p, pkg, flavor)
	if err := h.Store.UpsertBuildState(ctx, row); err != nil {
		return fmt.Errorf("store build state of %s/%s: %w", p, m.Package, err)
	}
	if row.State == storage.StateBuilding || row.State == storage.StateScheduled {
		return nil
	}
	return h.Dirty.JobFinished(ctx, p)
}

func relevant(phase string) bool {
	switch phase {
	case "BUILDING", "SUCCEEDED", "FAILED", "ABORTED", "REQUEUED":
		return true
	}
	return false
}

func (h *Handler) nextRow(ctx context.Context, m *Message, p buildrepo.Prpa, pkg, flavor string) *storage.PackageBuildState {
	row := &storage.PackageBuildState{
		Project: p.Project, Repo: p.Repo, Arch: p.Arch, Package: pkg, Flavor: flavor, JobID: m.JobID,
	}
	switch m.Phase {
	case "BUILDING":
		row.State = storage.StateBuilding
	case "REQUEUED":
		// The dispatcher put the job back into its queue after losing the
		// worker (OBS reschedules on worker loss); it is not a build failure.
		row.State = storage.StateScheduled
		row.Details = "requeued: " + m.Reason
	case "SUCCEEDED":
		row.State, row.Meta, row.SourceCommit = storage.StateSucceeded, m.Meta, m.SourceCommit
		fin := indexer.Finished{Prpa: p, Unit: m.Package, Artifacts: m.Artifacts}
		if _, err := h.Integrator.Integrate(ctx, fin); err != nil {
			row.State, row.Meta, row.SourceCommit = storage.StateFailed, "", ""
			row.Details = "indexing failed: " + err.Error()
		}
	case "ABORTED":
		row.State = storage.StateFailed
		row.Details = "aborted"
		if m.Reason != "" {
			row.Details = "aborted: " + m.Reason
		}
	default:
		row.State = storage.StateFailed
		row.Details = failureDetails(m)
	}
	return row
}

func failureDetails(m *Message) string {
	if m.Reason != "" {
		return m.Reason
	}
	if m.ExitCode != nil {
		return fmt.Sprintf("exit code %d", *m.ExitCode)
	}
	return ""
}
