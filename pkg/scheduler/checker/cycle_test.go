package checker_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// rounds runs passes, finishing every emitted job like a worker would, until
// a pass emits nothing. Each element is the sorted "unit:reason" list of the
// jobs of one pass. It fails when the repository does not settle.
func (w *world) rounds(limit int) [][]string {
	w.t.Helper()
	return w.roundsWith(limit, func(unit, tag string) []bininfo.BinInfo {
		return []bininfo.BinInfo{binary(unit, tag)}
	})
}

// roundsWith is rounds with the binaries a finished job produces chosen by
// the caller.
func (w *world) roundsWith(limit int, outputs func(unit, tag string) []bininfo.BinInfo) [][]string {
	w.t.Helper()
	var out [][]string
	for i := range limit {
		w.run()
		jobs := w.sink.take()
		if len(jobs) == 0 {
			return out
		}
		var names []string
		for _, j := range jobs {
			names = append(names, j.Package+":"+j.Explain)
			w.finish(j, outputs(j.Package, fmt.Sprintf("%d.%d", len(out), i))...)
		}
		sort.Strings(names)
		out = append(out, names)
	}
	w.t.Fatalf("no convergence after %d passes: %v", limit, out)
	return nil
}

func joinRounds(r [][]string) string {
	parts := make([]string, len(r))
	for i, names := range r {
		parts[i] = strings.Join(names, ",")
	}
	return strings.Join(parts, " | ")
}

// cycleWorld has x and y requiring each other plus a consumer z of x. The
// bootstrap repository has old binaries of all three.
func cycleWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t, []bininfo.BinInfo{binary("x", "0"), binary("y", "0"), binary("ext", "0")})
	w.pkg("x", []string{"y", "ext"})
	w.pkg("y", []string{"x"})
	w.pkg("z", []string{"x"})
	return w
}

func TestCycleSettles(t *testing.T) {
	w := cycleWorld(t)
	w.run()
	jobs := w.sink.take()
	if len(jobs) != 2 || jobs[0].Package != "x" || jobs[1].Package != "y" {
		t.Fatalf("first pass emitted %d jobs, want x and y", len(jobs))
	}
	// z waits for the cycle.
	w.wantState("z", storage.StateBlocked, "blocked: x")
	for _, j := range jobs {
		w.finish(j, binary(j.Package, "1"))
	}
	// The members were built against bootstrap binaries; now that they exist
	// as local builds the lines of their metas change once. The consumer
	// stays blocked until the cycle is quiet.
	want := "x:meta change,y:meta change | z:new build"
	if got := joinRounds(w.rounds(10)); got != want {
		t.Errorf("rounds %q, want %q", got, want)
	}
	for _, u := range []string{"x", "y", "z"} {
		w.wantState(u, storage.StateSucceeded, "")
	}
}

func TestCycleConsumersBlockWhileCycleBuilds(t *testing.T) {
	w := cycleWorld(t)
	w.run()
	jobs := w.sink.take()
	w.finish(jobs[0], binary(jobs[0].Package, "1")) // only x of the cycle is done
	w.run()
	w.wantState("z", storage.StateBlocked, "blocked: x")
}

// abcWorld is a cycle of a and b, a dependency c outside of it and a
// consumer d of the cycle. The bootstrap repository has old binaries.
func abcWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t, []bininfo.BinInfo{binary("a", "0"), binary("b", "0"), binary("c", "0")})
	w.pkg("a", []string{"b", "c"})
	w.pkg("b", []string{"a"})
	w.pkg("c", nil)
	w.pkg("d", []string{"a"})
	return w
}

func (w *world) touch(name string, requires []string) {
	w.addPackage(name, map[string]string{name + ".spec": spec(name, requires) + "# v2\n"})
}

func (w *world) force(name string) {
	w.t.Helper()
	err := w.store.RequestForcedRebuild(context.Background(), storage.ForcedRebuild{Project: testProject, Package: name})
	if err != nil {
		w.t.Fatal(err)
	}
}

// TestCycleDoesNotRebuildForever mirrors the OBS gen_meta scenario: a and b
// depend on each other's binaries, c is outside of the cycle. A rebuild of a
// because of c must not make a and b rebuild each other for ever, while a
// real change below the cycle still reaches every member.
func TestCycleDoesNotRebuildForever(t *testing.T) {
	tests := []struct {
		name   string
		change func(w *world)
		want   string
	}{
		{
			name:   "source change of a dependency outside the cycle",
			change: func(w *world) { w.touch("c", nil) },
			want:   "c:source change | a:meta change | b:meta change | d:meta change",
		},
		{
			name:   "source change of a cycle member",
			change: func(w *world) { w.touch("a", []string{"b", "c"}) },
			want:   "a:source change | b:meta change | d:meta change",
		},
		{
			name:   "forced rebuild of a dependency outside the cycle",
			change: func(w *world) { w.force("c") },
			want:   "c:forced rebuild",
		},
		{
			name:   "forced rebuild of a cycle member",
			change: func(w *world) { w.force("a") },
			want:   "a:forced rebuild",
		},
		{
			name:   "forced rebuild of the other cycle member",
			change: func(w *world) { w.force("b") },
			want:   "b:forced rebuild",
		},
		{
			name:   "no change",
			change: func(*world) {},
			want:   "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := abcWorld(t)
			// Bootstrap: build everything until quiet.
			w.rounds(10)
			tc.change(w)
			if got := joinRounds(w.rounds(10)); got != tc.want {
				t.Errorf("rounds %q, want %q", got, tc.want)
			}
			// Quiet means quiet: nothing is left to rebuild.
			w.run()
			w.wantJobs()
		})
	}
}

// TestCycleMetaIgnoresOwnBinaries checks that the meta of a cycle member does
// not depend on the files its partner was built with, only on what the
// partner's meta records about the rest of the world.
func TestCycleMetaIgnoresOwnBinaries(t *testing.T) {
	w := abcWorld(t)
	w.rounds(10)
	before := map[string]string{}
	for _, u := range []string{"a", "b", "c", "d"} {
		before[u] = w.state(u).BuiltMeta
	}
	// A new binary of b, built from the same inputs.
	w.force("b")
	w.rounds(10)
	for _, u := range []string{"a", "c", "d"} {
		if got := w.state(u).BuiltMeta; got != before[u] {
			t.Errorf("%s: meta changed after an unchanged rebuild of b", u)
		}
	}
}

// TestCycleCutsGeneratedSubpackages: the binaries a package produces need not
// match the subpackages its recipe declares (python singlespec, lua). The cycle
// must be cut at the real binary names, taken from the build repository.
func TestCycleCutsGeneratedSubpackages(t *testing.T) {
	w := newWorld(t, []bininfo.BinInfo{binary("a-gen", "0"), binary("b-gen", "0")})
	w.pkg("a", []string{"b-gen"})
	w.pkg("b", []string{"a-gen"})
	outputs := func(unit, tag string) []bininfo.BinInfo {
		return []bininfo.BinInfo{binary(unit+"-gen", tag)}
	}
	w.roundsWith(10, outputs)
	w.touch("a", []string{"b-gen"})
	got := joinRounds(w.roundsWith(10, outputs))
	if want := "a:source change | b:meta change"; got != want {
		t.Errorf("rounds %q, want %q", got, want)
	}
	w.run()
	w.wantJobs()
}

// TestMetaSurvivesRestart checks that the metas are read back from the store:
// a checker with empty caches finds nothing to rebuild, and a change still
// reaches the cycle.
func TestMetaSurvivesRestart(t *testing.T) {
	w := abcWorld(t)
	w.rounds(10)
	w.restart()
	w.run()
	w.wantJobs()
	w.touch("c", nil)
	w.restart()
	want := "c:source change | a:meta change | b:meta change | d:meta change"
	if got := joinRounds(w.rounds(10)); got != want {
		t.Errorf("rounds %q, want %q", got, want)
	}
}

// TestCycleMembershipChangeIsQuiet covers the loop seen on SLFO: a member of a
// large cycle becomes unresolvable and drops out of the dependency graph. The
// metas of the other members describe their own dependencies only, not the
// shape of the cycle, so nothing is rebuilt.
func TestCycleMembershipChangeIsQuiet(t *testing.T) {
	w := newWorld(t, []bininfo.BinInfo{binary("x", "0"), binary("y", "0"), binary("w", "0")})
	w.pkg("x", []string{"y", "w"})
	w.pkg("y", []string{"x"})
	w.pkg("w", []string{"x"})
	w.rounds(10)
	w.touch("w", []string{"x", "missing"})
	if got := joinRounds(w.rounds(10)); got != "" {
		t.Errorf("rounds %q, want none", got)
	}
	if st := w.state("w"); st.State != storage.StateUnresolvable {
		t.Errorf("w is %s %q, want unresolvable", st.State, st.Details)
	}
	w.wantState("x", storage.StateSucceeded, "")
	w.wantState("y", storage.StateSucceeded, "")
}

// TestMetaChangeKeepsRunningJob: like OBS, a job that is scheduled or running
// for the current source is not cancelled because the meta changed meanwhile.
// Cancelling would keep the producers of a cycle from ever finishing.
func TestMetaChangeKeepsRunningJob(t *testing.T) {
	w := cycleWorld(t)
	w.run()
	jobs := w.sink.take()
	yJob := jobs[1]
	w.finish(jobs[0], binary("x", "1")) // x is done: the meta of y changes
	w.run()
	w.wantJobs()
	if len(w.sink.cancelled) != 0 {
		t.Errorf("cancelled %v on a meta change", w.sink.cancelled)
	}
	if st := w.state("y"); st.State != storage.StateScheduled || st.JobID != yJob.JobID {
		t.Errorf("y is %s with job %s, want scheduled with %s", st.State, st.JobID, yJob.JobID)
	}
	// When it has finished, the meta is compared again and settles.
	w.finish(yJob, binary("y", "1"))
	w.rounds(10)
	if len(w.sink.cancelled) != 0 {
		t.Errorf("cancelled %v", w.sink.cancelled)
	}
	for _, u := range []string{"x", "y", "z"} {
		w.wantState(u, storage.StateSucceeded, "")
	}
}

// TestSourceChangeCancelsRunningJob: a new source makes the running job
// obsolete.
func TestSourceChangeCancelsRunningJob(t *testing.T) {
	w := newWorld(t, nil)
	w.pkg("p", nil)
	w.run()
	old := w.wantJobs("p")[0]
	w.touch("p", nil)
	w.run()
	w.wantJobs("p")
	if len(w.sink.cancelled) != 1 || w.sink.cancelled[0] != old.JobID {
		t.Errorf("cancelled %v, want the job %s", w.sink.cancelled, old.JobID)
	}
}

// TestMetaIDsAreCanonical: every id in a stored meta has the same width, for
// binaries from the path (sha256) and for local producers (source commit),
// whether or not the producer's tree is available.
func TestMetaIDsAreCanonical(t *testing.T) {
	check := func(w *world) {
		t.Helper()
		var hashes []string
		for _, u := range []string{"a", "b", "c", "d"} {
			hashes = append(hashes, w.state(u).BuiltMeta)
		}
		metas, err := w.store.GetBuildMetas(context.Background(), testProject, testRepo, testArch, hashes)
		if err != nil || len(metas) == 0 {
			t.Fatalf("metas %v, %v", metas, err)
		}
		for _, m := range metas {
			for _, l := range m.Lines {
				if id, _, _ := strings.Cut(l, "  "); len(id) != 32 {
					t.Errorf("line %q has a %d digit id", l, len(id))
				}
			}
		}
	}
	w := abcWorld(t)
	w.rounds(10)
	check(w)
	// Without the trees of the producers the binaries count by their sha256.
	err := w.store.PruneBuildMetas(context.Background(), testProject, testRepo, testArch, []string{"none"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	w.restart()
	w.rounds(10)
	if metas, _ := w.store.GetBuildMetas(context.Background(), testProject, testRepo, testArch, []string{w.state("d").BuiltMeta}); len(metas) == 0 {
		t.Fatal("no meta saved for the rebuilt d")
	}
	for _, m := range mustMetas(t, w, w.state("d").BuiltMeta) {
		for _, l := range m.Lines {
			if id, _, _ := strings.Cut(l, "  "); len(id) != 32 {
				t.Errorf("fallback line %q has a %d digit id", l, len(id))
			}
		}
	}
}

func mustMetas(t *testing.T, w *world, hash string) []storage.BuildMeta {
	t.Helper()
	metas, err := w.store.GetBuildMetas(context.Background(), testProject, testRepo, testArch, []string{hash})
	if err != nil {
		t.Fatal(err)
	}
	return metas
}

func TestBlockedDetailListIsAbbreviated(t *testing.T) {
	deps := make([]string, 0, 12)
	boot := []bininfo.BinInfo{}
	for i := range 12 {
		d := fmt.Sprintf("dep%02d", i)
		deps = append(deps, d)
		boot = append(boot, binary(d, "0"))
	}
	w := newWorld(t, boot)
	for _, d := range deps {
		w.pkg(d, nil)
	}
	w.pkg("app", deps)
	w.run()
	st := w.state("app")
	want := "blocked: " + strings.Join(deps[:10], ", ") + ", ..."
	if st.State != storage.StateBlocked || st.Details != want {
		t.Errorf("app is %s %q, want blocked %q", st.State, st.Details, want)
	}
}

func TestReleaseCountersFollowCommitsAndRebuilds(t *testing.T) {
	w := newWorld(t, nil)
	w.pkg("p", nil)
	w.run()
	j1 := w.wantJobs("p")[0]
	if j1.Release != "1.1" || j1.CICnt != 1 || j1.BCnt != 1 {
		t.Errorf("first build %q (%d,%d), want 1.1", j1.Release, j1.CICnt, j1.BCnt)
	}
	w.finish(j1, binary("p", "1"))

	w.addPackage("p", map[string]string{"p.spec": spec("p", nil) + "# v2\n"})
	w.run()
	j2 := w.wantJobs("p")[0]
	if j2.Release != "2.1" {
		t.Errorf("new commit gets release %q, want 2.1", j2.Release)
	}
}
