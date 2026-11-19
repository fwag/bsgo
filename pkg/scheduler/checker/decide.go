package checker

import (
	"sort"
	"strings"

	"github.com/openSUSE/bsgo/pkg/constraints"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// decision is the outcome for one unit.
type decision struct {
	unit    *unit
	state   string
	details string
	// meta and commit are set when this pass determined them; lines are the
	// lines of the meta of a newly scheduled build.
	meta   string
	commit string
	lines  []string
	// job is the job to emit for a newly scheduled unit.
	job *JobSpec
	// keep marks a job already in flight that stays valid.
	keep bool
	// cancel is the ID of an in-flight job that is no longer wanted.
	cancel string
	// reason is the structured cause of a blocked decision.
	reason *storage.BlockedReason
}

type outcomes struct {
	list     []*decision
	notready map[*unit]bool
	// held marks the units that are not ready only because a member of their
	// dependency cycle is, not by their own state.
	held map[*unit]bool
	// state is the decided state of every unit so far.
	state map[*unit]string
}

func newOutcomes() *outcomes {
	return &outcomes{notready: map[*unit]bool{}, held: map[*unit]bool{}, state: map[*unit]string{}}
}

func (o *outcomes) add(d *decision) {
	o.list = append(o.list, d)
	o.state[d.unit] = d.state
}

func inFlight(state string) bool {
	switch state {
	case storage.StateScheduled, storage.StateDispatching, storage.StateBuilding,
		storage.StateSigning, storage.StateFinished:
		return true
	}
	return false
}

// decideAll decides every unit: fixed verdicts first, then the dependency
// groups in build order.
func (ps *pass) decideAll() {
	for _, u := range ps.units {
		if u.fixed != nil {
			ps.fixedDecision(u)
		}
	}
	for _, g := range ps.groups {
		if g.cyclic {
			ps.decideCycle(g)
		} else {
			ps.decideUnit(g.members[0], passPlain)
		}
		ps.markCycleNotReady(g)
	}
}

// Passes over the members of a dependency cycle (BSSched::Checker handlecycle).
const (
	// passPlain is a unit outside of any cycle.
	passPlain = iota
	// passFirst only starts the members that need a build for their source or
	// that were never built. A member whose meta changed below the first
	// level is postponed: the change may well vanish when the members that
	// have a source change are rebuilt.
	passFirst
	// passSecond decides the postponed members, those with the shallowest
	// change first. A member waits for the members of its cycle that are not
	// ready and have a lower level.
	passSecond
)

// decideCycle decides the members of a cycle in two passes, like OBS. The
// members are built together, which OBS does one after another.
func (ps *pass) decideCycle(g *group) {
	var postponed []*unit
	for _, u := range g.members {
		if !ps.decideUnit(u, passFirst) {
			postponed = append(postponed, u)
		}
	}
	sort.SliceStable(postponed, func(i, j int) bool { return postponed[i].level < postponed[j].level })
	for _, u := range postponed {
		ps.decideUnit(u, passSecond)
	}
}

// markCycleNotReady marks the whole cycle as not ready when any member is, so
// that consumers wait for the cycle to finish (BSSched::Checker handlecycle,
// pass 3).
func (ps *pass) markCycleNotReady(g *group) {
	if !g.cyclic {
		return
	}
	for _, m := range g.members {
		if ps.out.notready[m] {
			for _, o := range g.members {
				if !ps.out.notready[o] {
					ps.out.held[o] = true
				}
				ps.out.notready[o] = true
			}
			return
		}
	}
}

func (ps *pass) row(u *unit) *storage.PackageBuildState { return ps.states[u.name] }

// withdrawJob returns the in-flight job ID of u that a non-scheduling verdict
// makes obsolete.
func (ps *pass) withdrawJob(u *unit) string {
	if r := ps.row(u); r != nil && inFlight(r.State) {
		return r.JobID
	}
	return ""
}

func (ps *pass) fixedDecision(u *unit) {
	d := &decision{unit: u, state: u.fixed.state, details: u.fixed.details, cancel: ps.withdrawJob(u)}
	if u.fixed.state == storage.StateBlocked {
		ps.out.notready[u] = true
	}
	ps.out.add(d)
}

func (ps *pass) setDecision(u *unit, state, details string) *decision {
	d := &decision{unit: u, state: state, details: details, cancel: ps.withdrawJob(u)}
	if state == storage.StateBlocked {
		ps.out.notready[u] = true
	}
	ps.out.add(d)
	return d
}

// setBlocked decides u is blocked for the structured reason.
func (ps *pass) setBlocked(u *unit, reason *storage.BlockedReason) {
	ps.setDecision(u, storage.StateBlocked, reason.Summary()).reason = reason
}

// decideUnit ports BSSched::BuildJob::Package::check for one unit. It reports
// false when the unit is postponed to the second pass of its cycle.
func (ps *pass) decideUnit(u *unit, pass int) bool {
	if ps.keepJob(u) {
		return true
	}
	if u.expErr != nil {
		state, details, reason := ps.expansionVerdict(u)
		if reason != nil {
			ps.setBlocked(u, reason)
		} else {
			ps.setDecision(u, state, details)
		}
		return true
	}
	row := ps.row(u)
	forced := ps.isForced(u)
	localOK := ps.target.Flags.EffectiveRebuild() == "local"
	if blocked := ps.blockedBy(u, pass); len(blocked) > 0 {
		m := ps.meta(u)
		if localOK && row != nil && row.BuiltMeta == m.hash && !forced {
			ps.settle(u, row, m, false)
			return true
		}
		ps.setBlocked(u, ps.blockReason(blocked))
		return true
	}
	m := ps.meta(u)
	if pass == passFirst && ps.postpone(u, row, m, forced) {
		return false
	}
	ps.settle(u, row, m, forced)
	return true
}

// keepJob keeps the job of a unit that is already scheduled or running for the
// current source, whatever its dependencies did meanwhile. OBS names a job
// after the source (BSSched/Checker.pm "already scheduled") and only kills it
// when the source changes or the package is excluded or disabled: a change of
// the meta alone is looked at again when the build has finished. Cancelling
// on every meta change would keep the producers of a dependency cycle from
// ever finishing, and their metas from ever settling. A forced rebuild
// request supersedes the job.
func (ps *pass) keepJob(u *unit) bool {
	row := ps.row(u)
	if row == nil || !inFlight(row.State) || row.SourceCommit != u.commit || ps.isForced(u) {
		return false
	}
	d := &decision{unit: u, state: row.State, details: row.Details, keep: true}
	if row.State == storage.StateScheduled {
		// A scheduled job lives in the dispatcher's memory only; the resend
		// restores it. When it cannot be recreated (a dependency vanished) the
		// unit must be evaluated normally instead of waiting for a job that
		// exists nowhere.
		var ok bool
		if d.job, d.meta, d.lines, ok = ps.resendJob(u, row); !ok {
			return false
		}
	}
	ps.out.add(d)
	ps.out.notready[u] = true
	return true
}

// postpone reports whether a member of a cycle only changed below the first
// level of its meta. It records the level on the unit.
func (ps *pass) postpone(u *unit, row *storage.PackageBuildState, m *metaResult, forced bool) bool {
	if forced || row == nil || row.BuiltMeta == "" || row.BuiltMeta == m.hash ||
		row.SourceCommit != u.commit || row.Meta == m.hash {
		return false
	}
	old := ps.trees[row.BuiltMeta]
	if old == nil {
		return false
	}
	u.level = diffDepth(m.text(), old)
	return u.level > 1
}

// blockedBy returns the dependency names whose producer in this prpa is not
// ready (scheduled, building or blocked). Producers in the same cycle do not
// block each other, except that a member postponed to the second pass waits
// for the members with a lower level.
func (ps *pass) blockedBy(u *unit, pass int) []string {
	if ps.target.Flags.EffectiveBlock() == "never" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, n := range u.edeps.Names() {
		p := ps.prod[n]
		if p == nil || p == u || seen[n] || !ps.out.notready[p] {
			continue
		}
		if u.group != nil && u.group.cyclic && u.group.has(p) && !ps.waitsFor(u, p, pass) {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// waitsFor reports whether member u of a cycle waits for its not ready
// member p (BSSched/BuildJob/Package.pm): only in the second pass, and only
// for members with a lower cycle level. A member without a recorded level has
// level 1: it was scheduled for its source.
func (ps *pass) waitsFor(u, p *unit, pass int) bool {
	return pass == passSecond && u.level > 0 && max(p.level, 1) < u.level
}

// expansionVerdict turns an expansion error into unresolvable with the OBS
// text, except when every missing package is the plain name of a package that
// this prpa has yet to build: then the unit is blocked on it.
func (ps *pass) expansionVerdict(u *unit) (state, details string, reason *storage.BlockedReason) {
	var waiting []string
	for _, m := range u.expErr.Messages {
		req, ok := strings.CutPrefix(m, "nothing provides ")
		if !ok {
			return storage.StateUnresolvable, u.expErr.Error(), nil
		}
		name := strings.Fields(strings.SplitN(req, " needed by ", 2)[0])
		if len(name) == 0 || !ps.pendingProducer(u, name[0]) {
			return storage.StateUnresolvable, u.expErr.Error(), nil
		}
		waiting = append(waiting, name[0])
	}
	reason = ps.blockReason(unique(waiting))
	return storage.StateBlocked, reason.Summary(), reason
}

// pendingProducer reports whether name is produced by another unit of the
// prpa that is making progress (scheduled, building or blocked on something
// that is). A producer that cannot build (unresolvable, failed, broken) or has
// no decision yet must not count: "blocked" is contagious (consumers and the
// whole dependency cycle become not ready), so blocking on a producer that
// never builds deadlocks the prpa. The consumer is then unresolvable, as OBS
// reports it.
func (ps *pass) pendingProducer(u *unit, name string) bool {
	p := ps.prod[name]
	if p == nil || p == u {
		return false
	}
	return ps.out.notready[p]
}

func unique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// settle compares the meta of u with its stored state and either keeps it or
// schedules a build (BuildJob.pm metacheck).
func (ps *pass) settle(u *unit, row *storage.PackageBuildState, m *metaResult, forced bool) {
	if !forced && row != nil {
		if ps.settleExisting(u, row, m.hash) {
			return
		}
	}
	explain := "new build"
	switch {
	case forced:
		explain = "forced rebuild"
	case row != nil && row.SourceCommit != "" && row.SourceCommit != u.commit:
		explain = "source change"
	case row != nil && (row.Meta != "" || row.BuiltMeta != ""):
		explain = "meta change"
	}
	ps.schedule(u, row, m, explain)
}

// resendJob rebuilds the job of a unit that is still scheduled so it can be
// emitted again. The dispatch queue is not persistent; re-emitting on every
// pass restores it after a server restart. The job carries the meta it was
// scheduled with, whose tree is stored, so the result matches the row. The job
// ID and the release counters are deterministic for the same meta and
// attempt, and the sink ignores job IDs it already has, so a resend changes
// nothing otherwise. The build dependencies are expanded again from the
// current repositories (OBS keeps the job file; bsgo does not persist them),
// so after a restart a resent job can build against newer binaries than the
// meta it carries describes; the next pass compares and rebuilds if that
// matters. It reports false when the job cannot be created any more.
func (ps *pass) resendJob(u *unit, row *storage.PackageBuildState) (job *JobSpec, meta string, lines []string, ok bool) {
	meta = row.Meta
	if meta == "" {
		// A row without a meta: schedule with the current one and store its tree.
		m := ps.meta(u)
		meta, lines = m.hash, m.text()
	}
	job, v := ps.newJob(u, meta, row.Details)
	if v != nil {
		return nil, "", nil, false
	}
	// The recomputed attempt can differ from the stored job's (a forced
	// rebuild request is consumed once its job was emitted). Keep the stored
	// job ID: the dispatcher dedupes it, a queued or running attempt is not
	// superseded, and the worker's result still matches the row.
	job.JobID = row.JobID
	return job, meta, lines, true
}

// settleExisting handles the cases where no new job is needed. It reports
// whether the unit was settled.
func (ps *pass) settleExisting(u *unit, row *storage.PackageBuildState, meta string) bool {
	switch {
	case row.BuiltMeta == meta:
		d := ps.setDecision(u, storage.StateSucceeded, "")
		d.meta, d.commit = meta, u.commit
		if row.Meta == meta {
			d.cancel = ""
		}
	case row.Meta == meta && row.State == storage.StateFailed:
		ps.out.add(&decision{unit: u, state: storage.StateFailed, details: row.Details})
	default:
		return false
	}
	return true
}

// schedule creates the job of u and records the scheduled decision. When the
// job cannot be created (for example a pre/vminstall package is missing) the
// unit becomes unresolvable.
func (ps *pass) schedule(u *unit, row *storage.PackageBuildState, m *metaResult, explain string) {
	job, verdict := ps.newJob(u, m.hash, explain)
	if verdict != nil {
		ps.setDecision(u, verdict.state, verdict.details)
		return
	}
	d := &decision{
		unit: u, state: storage.StateScheduled, details: scheduledDetails(u, explain),
		meta: m.hash, lines: m.text(), commit: u.commit, job: job,
	}
	if row != nil && inFlight(row.State) {
		d.cancel = row.JobID
	}
	ps.out.add(d)
	ps.out.notready[u] = true
}

// scheduledDetails is the status detail of a scheduled unit: the reason it is
// built, plus the sandbox it needs when its constraints restrict the worker.
func scheduledDetails(u *unit, explain string) string {
	if len(u.constraints.Sandbox) == 0 {
		return explain
	}
	return explain + ", needs sandbox " + constraints.DescribeSandbox(u.constraints.Sandbox)
}
