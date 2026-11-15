package checker_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/storage"
)

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

// buildAll finishes every emitted job with a binary named like the unit.
func buildAll(w *world) {
	for _, j := range w.sink.take() {
		w.finish(j, binary(j.Package, "1"))
	}
}

func TestCycleBuildsOncePerChange(t *testing.T) {
	w := cycleWorld(t)
	w.run()
	jobs := w.sink.take()
	var names []string
	for _, j := range jobs {
		names = append(names, j.Package)
	}
	// x and y are a cycle and both build; z waits for x.
	if got := strings.Join(names, ","); got != "x,y" {
		t.Fatalf("first pass emitted %s, want x,y", got)
	}
	w.wantState("z", storage.StateBlocked, "blocked: x")
	for _, j := range jobs {
		w.finish(j, binary(j.Package, "1"))
	}

	// Both finished against each other's old binaries; the new binaries of the
	// cycle members must not retrigger them.
	w.run()
	jobs = w.sink.take()
	if len(jobs) != 1 || jobs[0].Package != "z" {
		t.Fatalf("second pass emitted %d jobs, want only z", len(jobs))
	}
	w.finish(jobs[0], binary("z", "1"))
	w.run()
	w.wantJobs()

	// A source change of x rebuilds the cycle once: x for its source, y because
	// its cycle partner changed.
	w.addPackage("x", map[string]string{"x.spec": spec("x", []string{"y", "ext"}) + "# v2\n"})
	w.run()
	names = nil
	for _, j := range w.sink.take() {
		names = append(names, j.Package+":"+j.Explain)
		w.finish(j, binary(j.Package, "2"))
	}
	if got := strings.Join(names, ","); got != "x:source change,y:meta change" {
		t.Fatalf("after source change emitted %s", got)
	}
	w.run()
	got := w.sink.take()
	if len(got) != 1 || got[0].Package != "z" {
		t.Errorf("after the cycle rebuilt only z should follow, got %d jobs", len(got))
	}
	buildAll(w)
	w.run()
	w.wantJobs()
}

func TestCycleSettlesAfterBuild(t *testing.T) {
	w := cycleWorld(t)
	w.run()
	buildAll(w)
	w.run()
	buildAll(w)
	w.run()
	w.wantJobs()
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
	w.wantJobs()
	w.wantState("z", storage.StateBlocked, "blocked: x")
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
