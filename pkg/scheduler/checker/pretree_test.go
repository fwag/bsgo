package checker_test

import (
	"context"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// loseTrees removes every stored meta, like a repository whose builds are
// from before metas were kept, and restarts the checker.
func (w *world) loseTrees() {
	w.t.Helper()
	err := w.store.PruneBuildMetas(context.Background(), testProject, testRepo, testArch, []string{"none"}, time.Now().Add(time.Hour))
	if err != nil {
		w.t.Fatal(err)
	}
	w.restart()
}

// metaLines returns the stored meta of the state of a unit.
func (w *world) metaLines(unit string) []string {
	w.t.Helper()
	metas, err := w.store.GetBuildMetas(context.Background(), testProject, testRepo, testArch, []string{w.state(unit).Meta})
	if err != nil || len(metas) != 1 {
		w.t.Fatalf("meta of %s: %v, %v", unit, metas, err)
	}
	return metas[0].Lines
}

func preTreeWorld(t *testing.T, opts ...worldOpt) (w *world, cCommit string) {
	t.Helper()
	w = newWorld(t, []bininfo.BinInfo{binary("c", "0")}, opts...)
	cCommit = w.pkg("c", nil)
	w.pkg("d", []string{"c"})
	w.rounds(10)
	return w, cCommit
}

// TestPreTreeProducerCountsByBuiltCommit: a dependency built in this prpa
// whose meta is unknown counts with the commit it was built from, the id its
// meta will carry once it exists, and not with the sha256 of the binary.
func TestPreTreeProducerCountsByBuiltCommit(t *testing.T) {
	w, commit := preTreeWorld(t)
	w.loseTrees()
	w.force("d")
	w.run()
	w.wantJobs("d")
	if got, want := w.metaLines("d")[1], commit+"  c"; got != want {
		t.Errorf("line %q, want %q", got, want)
	}
}

// TestPreTreeProducerGainsTreeQuietly: when the tree appears, the consumer
// only gains nested lines. Here there are none, so nothing is rebuilt, neither
// when the producer is looked at without its tree nor when it is rebuilt.
func TestPreTreeProducerGainsTreeQuietly(t *testing.T) {
	w, _ := preTreeWorld(t)
	w.loseTrees()
	w.run()
	w.wantJobs()
	w.force("c")
	if got := joinRounds(w.rounds(10)); got != "c:forced rebuild" {
		t.Errorf("rounds %q, want only c", got)
	}
}

// TestPreTreeProducerWithUnbuiltSourceChange: the commit of a binary is the
// one it was built from; a source change that is not built yet is not
// attributed to it.
func TestPreTreeProducerWithUnbuiltSourceChange(t *testing.T) {
	noBlock := func(w *world) {
		tgt := w.prj.Targets[testRepo]
		tgt.Flags.Block = "never"
		w.prj.Targets[testRepo] = tgt
	}
	w, commit := preTreeWorld(t, noBlock)
	w.touch("c", nil)
	w.loseTrees()
	w.run()
	w.wantJobs("c")
	st := w.state("c")
	if st.BuiltCommit != commit || st.SourceCommit == commit {
		t.Errorf("c built %q, source %q, want built %q and a new source", st.BuiltCommit, st.SourceCommit, commit)
	}
	if st := w.state("d"); st.State != storage.StateSucceeded {
		t.Errorf("d is %s %q: the unbuilt source change of c was attributed to its binary", st.State, st.Details)
	}
}

// TestPreTreeProducerWithUnknownBuiltCommit: a producer whose build commit is
// not recorded (a legacy row that is not succeeded, with a newer source
// commit) counts by the sha256 of its binary, and its consumers rebuild when
// the new build succeeds.
func TestPreTreeProducerWithUnknownBuiltCommit(t *testing.T) {
	w := newWorld(t, nil)
	files := map[string]string{"c.spec": spec("c", nil)}
	commit := w.addPackage("c", files, func(pm *manifest.PackageManifest) { pm.Build.Enabled = false })
	w.pkg("d", []string{"c"})
	// The binary of an old build, whose row was written before trees and
	// built commits existed, followed by a failed build of a newer commit.
	w.bins["c"] = []bininfo.BinInfo{binary("c", "old")}
	w.publishIndex()
	ctx := context.Background()
	row := func(state, source string) *storage.PackageBuildState {
		return &storage.PackageBuildState{
			Project: testProject, Repo: testRepo, Arch: testArch, Package: "c",
			State: state, SourceCommit: source, Meta: "legacy",
		}
	}
	if err := w.store.UpsertBuildState(ctx, row(storage.StateSucceeded, "")); err != nil {
		t.Fatal(err)
	}
	if err := w.store.UpsertBuildState(ctx, row(storage.StateFailed, commit)); err != nil {
		t.Fatal(err)
	}
	w.run()
	dJob := w.wantJobs("d")[0]
	want := sha("cold")[:32] + "  c"
	if got := w.metaLines("d")[1]; got != want {
		t.Errorf("line %q, want %q", got, want)
	}
	w.finish(dJob, binary("d", "1"))
	// c is enabled again and built; d follows.
	w.addPackage("c", files)
	got := joinRounds(w.rounds(10))
	if want := "c:meta change | d:meta change"; got != want {
		t.Errorf("rounds %q, want %q", got, want)
	}
}
