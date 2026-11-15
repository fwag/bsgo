package checker

import (
	"strings"

	"github.com/openSUSE/bsgo/pkg/storage"
)

// maxBlockedList bounds the names listed in a "blocked" detail, like OBS.
const maxBlockedList = 10

// decision is the outcome for one unit.
type decision struct {
	unit    *unit
	state   string
	details string
	// meta and commit are set when this pass determined them.
	meta   string
	commit string
	// job is the job to emit for a newly scheduled unit.
	job *JobSpec
	// keep marks a job already in flight that stays valid.
	keep bool
	// cancel is the ID of an in-flight job that is no longer wanted.
	cancel string
}

type outcomes struct {
	list     []*decision
	notready map[*unit]bool
}

func newOutcomes() *outcomes { return &outcomes{notready: map[*unit]bool{}} }

func (o *outcomes) add(d *decision) { o.list = append(o.list, d) }

func inFlight(state string) bool {
	return state == storage.StateScheduled || state == storage.StateBuilding
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
		for _, u := range g.members {
			ps.decideUnit(u)
		}
		ps.markCycleNotReady(g)
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

// decideUnit ports BSSched::BuildJob::Package::check for one unit.
func (ps *pass) decideUnit(u *unit) {
	if u.expErr != nil {
		state, details := ps.expansionVerdict(u)
		ps.setDecision(u, state, details)
		return
	}
	row := ps.row(u)
	forced := ps.isForced(u)
	localOK := ps.target.Flags.EffectiveRebuild() == "local"
	if blocked := ps.blockedBy(u); len(blocked) > 0 {
		meta := ps.meta(u)
		if localOK && row != nil && row.BuiltMeta == meta && !forced {
			ps.settle(u, row, meta, false)
			return
		}
		ps.setDecision(u, storage.StateBlocked, "blocked: "+listNames(blocked))
		return
	}
	ps.settle(u, row, ps.meta(u), forced)
}

// listNames joins names, abbreviating long lists with "...".
func listNames(names []string) string {
	if len(names) > maxBlockedList {
		names = append(append([]string(nil), names[:maxBlockedList]...), "...")
	}
	return strings.Join(names, ", ")
}

// blockedBy returns the dependency names whose producer in this prpa is not
// ready (scheduled, building or blocked). Producers in the same cycle never
// block each other.
func (ps *pass) blockedBy(u *unit) []string {
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
		if u.group != nil && u.group.cyclic && u.group.has(p) {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// expansionVerdict turns an expansion error into unresolvable with the OBS
// text, except when every missing package is the plain name of a package that
// this prpa has yet to build: then the unit is blocked on it.
func (ps *pass) expansionVerdict(u *unit) (state, details string) {
	var waiting []string
	for _, m := range u.expErr.Messages {
		req, ok := strings.CutPrefix(m, "nothing provides ")
		if !ok {
			return storage.StateUnresolvable, u.expErr.Error()
		}
		name := strings.Fields(strings.SplitN(req, " needed by ", 2)[0])
		if len(name) == 0 || !ps.pendingProducer(u, name[0]) {
			return storage.StateUnresolvable, u.expErr.Error()
		}
		waiting = append(waiting, name[0])
	}
	return storage.StateBlocked, "blocked: " + listNames(unique(waiting))
}

// pendingProducer reports whether name is produced by another unit of the
// prpa that has no build result yet.
func (ps *pass) pendingProducer(u *unit, name string) bool {
	p := ps.prod[name]
	if p == nil || p == u {
		return false
	}
	return ps.out.notready[p] || len(ps.own[p.name]) == 0
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
func (ps *pass) settle(u *unit, row *storage.PackageBuildState, meta string, forced bool) {
	if !forced && row != nil {
		if ps.settleExisting(u, row, meta) {
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
	ps.schedule(u, row, meta, explain)
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
	case row.Meta == meta && inFlight(row.State):
		d := &decision{unit: u, state: row.State, details: row.Details, keep: true}
		ps.out.add(d)
		ps.out.notready[u] = true
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
func (ps *pass) schedule(u *unit, row *storage.PackageBuildState, meta, explain string) {
	job, verdict := ps.newJob(u, meta, explain)
	if verdict != nil {
		ps.setDecision(u, verdict.state, verdict.details)
		return
	}
	d := &decision{
		unit: u, state: storage.StateScheduled, details: explain,
		meta: meta, commit: u.commit, job: job,
	}
	if row != nil && inFlight(row.State) {
		d.cancel = row.JobID
	}
	ps.out.add(d)
	ps.out.notready[u] = true
}
