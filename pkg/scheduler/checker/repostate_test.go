package checker_test

import (
	"context"
	"testing"

	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func (w *world) repoState() *storage.PrpaState {
	w.t.Helper()
	st, err := w.store.GetPrpaState(context.Background(), testProject, testRepo, testArch)
	if err != nil {
		w.t.Fatalf("repository state: %v", err)
	}
	return st
}

func (w *world) wantRepoState(state string) {
	w.t.Helper()
	if got := w.repoState().State; got != state {
		w.t.Errorf("repository state %s, want %s", got, state)
	}
}

func publishTarget(w *world) {
	tgt := w.prj.Targets[testRepo]
	tgt.Flags.Publish = true
	w.prj.Targets[testRepo] = tgt
}

// TestRepositoryStateFollowsThePass walks a chain through building, blocked
// and finished, and checks the publisher's states survive passes only while
// they are current.
func TestRepositoryStateFollowsThePass(t *testing.T) {
	w := newWorld(t, nil, publishTarget)
	w.pkg("a", nil)

	w.run()
	jobs := w.wantJobs("a")
	w.wantRepoState(storage.RepoBuilding)

	w.finish(jobs[0], binary("a", "1"))
	w.run()
	w.wantRepoState(storage.RepoFinished)
	hash := w.repoState().IndexHash
	if hash == "" {
		t.Fatal("finished state must record the index hash")
	}

	published := &storage.PrpaState{Project: testProject, Repo: testRepo, Arch: testArch,
		State: storage.RepoPublished, IndexHash: hash}
	if err := w.store.SetPrpaState(context.Background(), published); err != nil {
		t.Fatal(err)
	}
	w.run()
	w.wantRepoState(storage.RepoPublished)

	published.IndexHash = "stale"
	if err := w.store.SetPrpaState(context.Background(), published); err != nil {
		t.Fatal(err)
	}
	w.run()
	w.wantRepoState(storage.RepoFinished)
}

func TestRepositoryStateBlockedUnpublishedAndBroken(t *testing.T) {
	tests := []struct {
		name  string
		opts  []worldOpt
		setup func(*world)
		want  string
	}{
		{"nothing in flight but a blocked unit", nil, func(w *world) {
			w.addPackage("lost", map[string]string{"lost.spec": spec("lost", nil)}, func(pm *manifest.PackageManifest) {
				pm.Source.Git = "https://git.example/missing"
			})
		}, storage.RepoBlocked},
		{"target without publish is unpublished", nil, func(w *world) {
			w.pkg("x", []string{"nothing-provides-this"})
		}, storage.RepoUnpublished},
		{"unusable configuration is broken", []worldOpt{func(w *world) {
			w.prj.PublicConfig = &manifest.ProjectConfig{MacrosFile: "legacy"}
		}}, func(w *world) { w.pkg("x", nil) }, storage.RepoBroken},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, nil, tc.opts...)
			tc.setup(w)
			if tc.want == storage.RepoBroken {
				if _, err := w.chk.Run(context.Background(), w.prpa); err == nil {
					t.Fatal("a broken configuration must fail the pass")
				}
			} else {
				w.run()
			}
			w.wantRepoState(tc.want)
		})
	}
}
