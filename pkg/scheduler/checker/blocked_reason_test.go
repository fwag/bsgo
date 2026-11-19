package checker_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func wantReason(t *testing.T, w *world, unit string, want *storage.BlockedReason) {
	t.Helper()
	st := w.state(unit)
	if st.State != storage.StateBlocked || !st.Blocked.Equal(want) {
		t.Errorf("%s: %s %q reason %+v, want %+v", unit, st.State, st.Details, st.Blocked, want)
	}
	if st.Details != want.Summary() {
		t.Errorf("%s: details %q, want %q", unit, st.Details, want.Summary())
	}
}

func prod(unit, state string, binaries ...string) storage.BlockedProducer {
	return storage.BlockedProducer{Unit: unit, State: state, Binaries: binaries}
}

// TestBlockedByGenuineProducer: the reason names the source package that
// produces the binary, not the binary.
func TestBlockedByGenuineProducer(t *testing.T) {
	w := newWorld(t, nil)
	w.pkg("lib", nil, "lib-devel")
	w.pkg("app", []string{"lib-devel"})
	w.run()
	wantReason(t, w, "app", &storage.BlockedReason{Producers: []storage.BlockedProducer{prod("lib", "scheduled", "lib-devel")}})
}

// TestBlockedByCycleOnly: z waits for x, which is built; x only counts as not
// ready because y of its cycle is still scheduled.
func TestBlockedByCycleOnly(t *testing.T) {
	w := cycleWorld(t)
	w.run()
	jobs := w.sink.take()
	w.finish(jobs[0], binary(jobs[0].Package, "1"))
	w.run()
	wantReason(t, w, "z", &storage.BlockedReason{Cycles: []storage.BlockedCycle{{
		Held:     []storage.BlockedProducer{prod("x", "succeeded", "x")},
		InFlight: []storage.BlockedProducer{prod("y", "scheduled")}, Members: 2,
	}}})
	if got, want := w.state("z").Details, "blocked: dependency cycle not finished (y scheduled)"; got != want {
		t.Errorf("details %q, want %q", got, want)
	}
}

// TestBlockedByGenuineAndCycle: a plain producer is listed before the cycle.
func TestBlockedByGenuineAndCycle(t *testing.T) {
	w := newWorld(t, []bininfo.BinInfo{binary("x", "0"), binary("y", "0"), binary("q", "0")})
	w.pkg("x", []string{"y"})
	w.pkg("y", []string{"x"})
	w.pkg("q", nil)
	w.pkg("z", []string{"x", "q"})
	w.run()
	jobs := w.sink.take()
	for _, j := range jobs {
		if j.Package == "x" {
			w.finish(j, binary("x", "1"))
		}
	}
	w.run()
	got := w.state("z")
	want := "blocked: q (scheduled), dependency cycle not finished (y scheduled)"
	if got.Details != want {
		t.Errorf("details %q, want %q", got.Details, want)
	}
	if n := len(got.Blocked.Producers); n != 1 || len(got.Blocked.Cycles) != 1 {
		t.Errorf("reason %+v, want one producer and one cycle", got.Blocked)
	}
}

// TestBlockedReasonRewritesRow: a change of the reason that the cropped
// details text does not show still rewrites the row; an identical reason does
// not.
func TestBlockedReasonRewritesRow(t *testing.T) {
	w := newWorld(t, nil)
	deps := make([]string, 5)
	for i := range deps {
		deps[i] = fmt.Sprintf("dep%d", i)
		w.pkg(deps[i], nil)
	}
	w.pkg("app", deps)
	w.run()
	first := w.state("app")
	if first.Details != "blocked: dep0 (scheduled), dep1 (scheduled), dep2 (scheduled) +2 more" {
		t.Fatalf("details %q", first.Details)
	}
	w.run()
	if again := w.state("app"); !again.UpdatedAt.Equal(first.UpdatedAt) {
		t.Error("unchanged reason rewrote the row")
	}
	hidden := w.state("dep4")
	hidden.State = storage.StateBuilding
	if err := w.store.UpsertBuildState(context.Background(), hidden); err != nil {
		t.Fatal(err)
	}
	w.run()
	got := w.state("app")
	if got.Details != first.Details {
		t.Fatalf("details %q changed, the test needs them to stay", got.Details)
	}
	if reflect.DeepEqual(got.Blocked, first.Blocked) || got.Blocked.Producers[4].State != storage.StateBuilding {
		t.Errorf("reason %+v was not rewritten", got.Blocked)
	}
}

// TestBlockedByUnresolvedBinaryKeepsOBSText: a missing dependency nobody
// builds stays "nothing provides" and has no structured reason.
func TestBlockedByUnresolvedBinaryKeepsOBSText(t *testing.T) {
	w := newWorld(t, nil)
	w.pkg("app", []string{"missing"})
	w.run()
	st := w.state("app")
	if st.State != storage.StateUnresolvable || st.Blocked != nil {
		t.Errorf("app is %s %q reason %+v", st.State, st.Details, st.Blocked)
	}
}

// TestCycleReasonCountsBlockedMembers: a member of the cycle that is blocked
// itself is no cause, only counted; building members come before scheduled.
func TestCycleReasonCountsBlockedMembers(t *testing.T) {
	w := newWorld(t, []bininfo.BinInfo{binary("x", "0"), binary("y", "0"), binary("q", "0")})
	w.pkg("x", []string{"y"})
	w.pkg("y", []string{"x"})
	w.pkg("z", []string{"x"})
	w.run()
	jobs := w.sink.take()
	for _, j := range jobs {
		if j.Package == "x" {
			w.finish(j, binary("x", "1"))
		}
	}
	// y is building, not just scheduled.
	y := w.state("y")
	y.State = storage.StateBuilding
	if err := w.store.UpsertBuildState(context.Background(), y); err != nil {
		t.Fatal(err)
	}
	w.run()
	got := w.state("z")
	want := "blocked: dependency cycle not finished (y building)"
	if got.Details != want {
		t.Fatalf("details %q, want %q", got.Details, want)
	}
	if c := got.Blocked.Cycles[0]; c.Members != 2 || c.BlockedMembers != 0 {
		t.Errorf("cycle %+v", c)
	}
}

// TestCycleWithOnlyBlockedMembers: y is blocked by a plain producer, so the
// cycle has nothing in flight but one blocked member.
func TestCycleWithOnlyBlockedMembers(t *testing.T) {
	w := newWorld(t, []bininfo.BinInfo{binary("x", "0"), binary("y", "0"), binary("q", "0")})
	w.pkg("x", []string{"y"})
	w.pkg("y", []string{"x", "q"})
	w.pkg("q", nil)
	w.pkg("z", []string{"x"})
	w.run()
	for _, j := range w.sink.take() {
		if j.Package == "x" {
			w.finish(j, binary("x", "1"))
		}
	}
	w.run()
	got := w.state("z")
	want := "blocked: dependency cycle not finished (none in flight; 1 blocked)"
	if got.State != storage.StateBlocked || got.Details != want {
		t.Fatalf("z is %s %q, want %q", got.State, got.Details, want)
	}
}
