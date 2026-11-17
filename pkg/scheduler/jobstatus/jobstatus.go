// Package jobstatus turns the bsgo.status messages of workers into build
// state rows, and integrates the binaries of successful jobs into the build
// repository before the success becomes visible. The checker owns every other
// state transition; this is the only writer of dispatching, building, signing, finished,
// succeeded and failed.
//
// Projects that sign their RPMs take a detour: a successful job goes to state
// signing and is sent to the signer; the signer's answer (job-signed) makes it
// finished, integrates the signed binaries and then marks it succeeded.
package jobstatus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/scheduler/indexer"
	"github.com/openSUSE/bsgo/pkg/signing"
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

// Signing routes successful jobs through the signing stage.
type Signing interface {
	// Enabled reports whether the RPMs of project are signed.
	Enabled(ctx context.Context, project string) (bool, error)
	// Request sends the job to the signer (job-finished message).
	Request(ctx context.Context, j signing.Job) error
}

// Handler processes status messages.
type Handler struct {
	Store      Store
	Integrator Integrator
	Dirty      Dirtier
	// Signing is nil when no signing stage runs: successful jobs are then
	// integrated directly.
	Signing Signing
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
	if lostJob(m.Phase) && !inFlight(cur.State) {
		return nil // the checker already moved the unit on
	}
	if m.Phase == "DISPATCHING" && cur.JobID == m.JobID && cur.State == storage.StateBuilding {
		return nil // the worker's start overtook the dispatcher's message
	}
	row := h.nextRow(ctx, &m, p, pkg, flavor)
	if err := h.Store.UpsertBuildState(ctx, row); err != nil {
		return fmt.Errorf("store build state of %s/%s: %w", p, m.Package, err)
	}
	switch row.State {
	case storage.StateSigning:
		return h.requestSigning(ctx, &m, p, pkg, flavor)
	case storage.StateScheduled:
		if m.Phase == "ABORTED" {
			// The dispatch queue forgot the job: a pass sends it again.
			return h.Dirty.JobFinished(ctx, p)
		}
		return nil
	case storage.StateDispatching, storage.StateBuilding:
		return nil
	}
	return h.Dirty.JobFinished(ctx, p)
}

// lostJob reports whether the phase says the job did not run to an end.
func lostJob(phase string) bool { return phase == "ABORTED" || phase == "REQUEUED" }

// inFlight reports whether the dispatcher or a worker may still hold the job.
func inFlight(state string) bool {
	switch state {
	case storage.StateScheduled, storage.StateDispatching, storage.StateBuilding:
		return true
	}
	return false
}

func relevant(phase string) bool {
	switch phase {
	case "DISPATCHING", "BUILDING", "SUCCEEDED", "FAILED", "ABORTED", "REQUEUED":
		return true
	}
	return false
}

func (h *Handler) nextRow(ctx context.Context, m *Message, p buildrepo.Prpa, pkg, flavor string) *storage.PackageBuildState {
	row := rowOf(p, pkg, flavor, m.JobID)
	switch m.Phase {
	case "DISPATCHING":
		// The dispatcher handed the job to a worker that has not started it.
		row.State = storage.StateDispatching
	case "BUILDING":
		// The worker started the job; updated_at is the build start.
		row.State = storage.StateBuilding
	case "REQUEUED":
		// The dispatcher put the job back into its queue after losing the
		// worker (OBS reschedules on worker loss); it is not a build failure.
		row.State = storage.StateScheduled
		row.Details = "requeued: " + m.Reason
	case "SUCCEEDED":
		h.succeeded(ctx, row, m, p)
	case "ABORTED":
		// A cancelled or killed job is not a build failure (OBS puts the
		// package back to scheduled and builds it again). Superseded and
		// withdrawn jobs never get here: their row carries another job ID or
		// no longer is in flight.
		row.State = storage.StateScheduled
		row.Details = "requeued: aborted"
		if m.Reason != "" {
			row.Details = "requeued: aborted: " + m.Reason
		}
	default:
		row.State = storage.StateFailed
		row.Details = failureDetails(m)
	}
	return row
}

func rowOf(p buildrepo.Prpa, pkg, flavor, jobID string) *storage.PackageBuildState {
	return &storage.PackageBuildState{
		Project: p.Project, Repo: p.Repo, Arch: p.Arch, Package: pkg, Flavor: flavor, JobID: jobID,
	}
}

// succeeded integrates the binaries at once, or leaves the row in state
// signing when the project signs its RPMs.
func (h *Handler) succeeded(ctx context.Context, row *storage.PackageBuildState, m *Message, p buildrepo.Prpa) {
	if h.Signing != nil && signing.HasRPMs(m.Artifacts) {
		on, err := h.Signing.Enabled(ctx, p.Project)
		if err != nil {
			row.State, row.Details = storage.StateFailed, "signing failed: "+err.Error()
			return
		}
		if on {
			row.State = storage.StateSigning
			return
		}
	}
	j := signing.Job{Package: m.Package, Meta: m.Meta, SourceCommit: m.SourceCommit, Artifacts: m.Artifacts}
	h.integrate(ctx, row, p, j)
}

// integrate puts the binaries of j into the build repository and sets the
// final state of row.
func (h *Handler) integrate(ctx context.Context, row *storage.PackageBuildState, p buildrepo.Prpa, j signing.Job) {
	row.State, row.Meta, row.SourceCommit = storage.StateSucceeded, j.Meta, j.SourceCommit
	fin := indexer.Finished{Prpa: p, Unit: j.Package, Artifacts: j.Artifacts}
	if _, err := h.Integrator.Integrate(ctx, fin); err != nil {
		row.State, row.Meta, row.SourceCommit = storage.StateFailed, "", ""
		row.Details = "indexing failed: " + err.Error()
	}
}

// requestSigning sends the built job to the signer. When that fails the job
// cannot be signed and fails.
func (h *Handler) requestSigning(ctx context.Context, m *Message, p buildrepo.Prpa, pkg, flavor string) error {
	j := signing.Job{
		JobID: m.JobID, Project: p.Project, Package: m.Package, Target: p.Repo, Architecture: p.Arch,
		Meta: m.Meta, SourceCommit: m.SourceCommit, Artifacts: m.Artifacts,
	}
	err := h.Signing.Request(ctx, j)
	if err == nil {
		return nil
	}
	row := rowOf(p, pkg, flavor, m.JobID)
	row.State, row.Details = storage.StateFailed, "signing failed: "+err.Error()
	if uerr := h.Store.UpsertBuildState(ctx, row); uerr != nil {
		return fmt.Errorf("store build state of %s/%s: %w", p, m.Package, uerr)
	}
	return h.Dirty.JobFinished(ctx, p)
}

// HandleSigned processes one job-signed message: the signer's answer moves a
// job in state signing to finished, integrates the signed binaries and then
// marks the job succeeded, or fails the job when signing failed. Answers for
// superseded jobs and for jobs that are already done are ignored, so a
// redelivered message does no harm.
func (h *Handler) HandleSigned(ctx context.Context, data []byte) error {
	j, err := signing.Decode(data)
	if err != nil {
		return err
	}
	p := buildrepo.Prpa{Project: j.Project, Repo: j.Target, Arch: j.Architecture}
	pkg, flavor := buildrepo.SplitUnit(j.Package)
	cur, err := h.Store.GetBuildState(ctx, p.Project, p.Repo, p.Arch, pkg, flavor)
	if errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read build state of %s/%s: %w", p, j.Package, err)
	}
	if cur.JobID != j.JobID || (cur.State != storage.StateSigning && cur.State != storage.StateFinished) {
		return nil
	}
	row, err := h.finish(ctx, p, pkg, flavor, j)
	if err != nil {
		return err
	}
	if err := h.Store.UpsertBuildState(ctx, row); err != nil {
		return fmt.Errorf("store build state of %s/%s: %w", p, j.Package, err)
	}
	return h.Dirty.JobFinished(ctx, p)
}

// finish returns the final row of a signed job: failed when signing failed,
// otherwise the result of the integration after the intermediate finished
// state was stored.
func (h *Handler) finish(ctx context.Context, p buildrepo.Prpa, pkg, flavor string, j signing.Job) (*storage.PackageBuildState, error) {
	row := rowOf(p, pkg, flavor, j.JobID)
	if j.Error != "" {
		row.State, row.Details = storage.StateFailed, "signing failed: "+j.Error
		return row, nil
	}
	row.State = storage.StateFinished
	if err := h.Store.UpsertBuildState(ctx, row); err != nil {
		return nil, fmt.Errorf("store finished state of %s/%s: %w", p, j.Package, err)
	}
	final := rowOf(p, pkg, flavor, j.JobID)
	h.integrate(ctx, final, p, j)
	return final, nil
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
