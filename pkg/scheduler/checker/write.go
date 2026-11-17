package checker

import (
	"fmt"

	"github.com/openSUSE/bsgo/pkg/storage"
)

// emitAndStore carries out the decisions: it emits the jobs of newly
// scheduled units, writes all changed state rows in one transaction, cancels
// jobs that are no longer wanted and consumes the forced rebuild requests that
// led to a job. Emitting comes before storing: a crash in between repeats the
// pass, which re-emits the same job IDs.
func (ps *pass) emitAndStore() error {
	if ps.err != nil {
		return ps.err
	}
	if err := ps.saveMetas(); err != nil {
		return err
	}
	sink := ps.c.d.Sink
	for _, d := range ps.out.list {
		if d.job == nil {
			continue
		}
		if err := sink.Emit(ps.ctx, d.job); err != nil {
			return fmt.Errorf("emit job %s of %s: %w", d.job.JobID, d.unit.name, err)
		}
	}
	if err := ps.c.d.Store.UpsertBuildStates(ps.ctx, ps.changedRows()); err != nil {
		return fmt.Errorf("store build states: %w", err)
	}
	for _, d := range ps.out.list {
		if d.cancel == "" {
			continue
		}
		if err := sink.Cancel(ps.ctx, d.cancel, "superseded"); err != nil {
			return fmt.Errorf("cancel job %s of %s: %w", d.cancel, d.unit.name, err)
		}
	}
	return ps.clearForced()
}

// changedRows returns the rows that differ from the stored ones. Unchanged
// rows are left alone so that concurrent job status updates are not
// overwritten.
func (ps *pass) changedRows() []*storage.PackageBuildState {
	var rows []*storage.PackageBuildState
	for _, d := range ps.out.list {
		if d.keep {
			continue
		}
		row := &storage.PackageBuildState{
			Project: ps.prpa.Project, Repo: ps.prpa.Repo, Arch: ps.prpa.Arch,
			Package: d.unit.pkg, Flavor: d.unit.flavor,
			State: d.state, Details: d.details,
			SourceCommit: d.commit, Meta: d.meta,
		}
		if d.job != nil {
			row.JobID, row.Bcnt = d.job.JobID, d.job.BCnt
		}
		if !sameRow(ps.row(d.unit), row) {
			rows = append(rows, row)
		}
	}
	return rows
}

func sameRow(old, next *storage.PackageBuildState) bool {
	if old == nil || old.State != next.State || old.Details != next.Details {
		return false
	}
	return next.JobID == "" || (old.JobID == next.JobID && old.Meta == next.Meta)
}

// clearForced consumes the forced rebuild requests of units that now have a
// job.
func (ps *pass) clearForced() error {
	var units []storage.UnitRef
	for _, d := range ps.out.list {
		if d.job != nil && ps.isForced(d.unit) {
			units = append(units, storage.UnitRef{Package: d.unit.pkg, Flavor: d.unit.flavor})
		}
	}
	if len(units) == 0 {
		return nil
	}
	if err := ps.c.d.Store.ClearForcedRebuilds(ps.ctx, ps.prpa.Project, ps.prpa.Repo, ps.prpa.Arch, units); err != nil {
		return fmt.Errorf("clear forced rebuilds: %w", err)
	}
	return nil
}

func (ps *pass) result() *Result {
	res := &Result{Counts: map[string]int{}, RetryAfter: ps.retry}
	for _, d := range ps.out.list {
		res.Counts[d.state]++
		if d.job != nil && !d.keep {
			res.Scheduled = append(res.Scheduled, d.unit.name)
		}
		if d.cancel != "" {
			res.Cancelled = append(res.Cancelled, d.cancel)
		}
	}
	return res
}
