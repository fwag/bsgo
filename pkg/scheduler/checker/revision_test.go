package checker_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func withSLFORelease(w *world) {
	w.prj.PublicConfig = &manifest.ProjectConfig{Release: "<CI_CNT>.<B_CNT> spec:160000.<CI_CNT>.<B_CNT>"}
}

// resolveAs resolves pending revisions with a fixed version and git vrev.
func (w *world) resolveAs(version string, gitVrev int) {
	w.t.Helper()
	w.resolveRevisions(func(storage.PackageRevision) storage.RevisionResult {
		return storage.RevisionResult{Version: version, GitVrev: gitVrev}
	})
}

// noRevisions is a store written by an old binary: it has no revision log.
type noRevisions struct{ *storage.MemoryStore }

func (noRevisions) RevisionFor(context.Context, string, string, string) (*storage.PackageRevision, error) {
	return nil, storage.ErrNotFound
}

func TestPendingRevisionBlocksUnitWithoutJob(t *testing.T) {
	w := newWorld(t, nil, withPendingRevisions)
	commit := w.pkg("p", nil)
	res := w.run()
	w.wantJobs()
	w.wantState("p", storage.StateBlocked, "waiting for release number (source history of "+commit[:12]+")")
	if res.RetryAfter <= 0 || res.RetryAfter > 15*time.Second {
		t.Errorf("retry after %s, want a short retry", res.RetryAfter)
	}
	rev, err := w.store.RevisionFor(context.Background(), testProject, "p", commit)
	if err != nil || rev.WantedAt.IsZero() {
		t.Errorf("the checker must record the revision as wanted: %+v, %v", rev, err)
	}

	w.resolveAs("1", 3)
	w.run()
	jobs := w.wantJobs("p")
	if jobs[0].CICnt != 3 || jobs[0].Versrel != "1-3" || jobs[0].Release != "3.1" {
		t.Errorf("job %+v, want CI 3 versrel 1-3 release 3.1", jobs[0])
	}
}

func TestBlockedVerdictShowsLastResolverError(t *testing.T) {
	w := newWorld(t, nil, withPendingRevisions)
	commit := w.pkg("p", nil)
	ctx := context.Background()
	claimed, err := w.store.ClaimRevisions(ctx, "t", 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim %v %v", claimed, err)
	}
	if _, err := w.store.FailRevision(ctx, claimed[0], "fetch: connection reset", 0); err != nil {
		t.Fatal(err)
	}
	w.run()
	w.wantState("p", storage.StateBlocked, "waiting for release number (source history of "+commit[:12]+"): fetch: connection reset")
}

func TestMissingRevisionIsBlockedAndLoggedOncePerInterval(t *testing.T) {
	w := newWorld(t, nil)
	var logs []string
	w.deps.Store = noRevisions{w.store}
	w.deps.Logf = func(format string, args ...any) { logs = append(logs, format) }
	w.restart()
	w.addPackage("m", map[string]string{
		"m.spec":      spec("m", nil),
		"_multibuild": "<multibuild><flavor>mini</flavor></multibuild>",
	})
	w.run()
	w.wantJobs()
	if st := w.state("m"); st.State != storage.StateBlocked || !strings.HasPrefix(st.Details, "waiting for release number") {
		t.Errorf("state %+v", st)
	}
	w.run()
	if len(logs) != 1 {
		t.Errorf("%d log lines, want one per revision however many passes run: %v", len(logs), logs)
	}
}

func TestReleaseOfAKernelLikeRevision(t *testing.T) {
	w := newWorld(t, nil, withSLFORelease, withPendingRevisions)
	w.pkg("kernel-source", nil)
	w.resolveAs("1", 25)
	w.run()
	jobs := w.wantJobs("kernel-source")
	if jobs[0].Release != "160000.25.1" || jobs[0].CICnt != 25 || jobs[0].BCnt != 1 {
		t.Errorf("release %q (%d,%d), want 160000.25.1", jobs[0].Release, jobs[0].CICnt, jobs[0].BCnt)
	}
	if st := w.state("kernel-source"); st.Versrel != "1-25" || st.Bcnt != 1 {
		t.Errorf("state %+v: want versrel 1-25 recorded", st)
	}
}

func TestFlavorVersrelUsesTheFlavorRecipeVersion(t *testing.T) {
	w := newWorld(t, nil, withPendingRevisions)
	w.addPackage("m", map[string]string{
		"m.spec":      spec("m", nil),
		"mini.spec":   strings.Replace(spec("mini", nil), "Version: 1", "Version: 2.5", 1),
		"_multibuild": "<multibuild><flavor>mini</flavor></multibuild>",
	})
	w.resolveAs("1", 7)
	w.run()
	jobs := w.wantJobs("m", "m:mini")
	if jobs[0].Versrel != "1-7" || jobs[1].Versrel != "2.5-7" {
		t.Errorf("versrels %q %q, want 1-7 and 2.5-7", jobs[0].Versrel, jobs[1].Versrel)
	}
}

func TestBcntContinuesPerVersrel(t *testing.T) {
	w := newWorld(t, nil, withPendingRevisions)
	w.pkg("p", nil)
	w.resolveAs("1", 4)
	w.run()
	first := w.wantJobs("p")[0]
	w.finish(first, binary("p", "1"))

	if err := w.store.RequestForcedRebuild(context.Background(), storage.ForcedRebuild{Project: testProject, Package: "p"}); err != nil {
		t.Fatal(err)
	}
	w.run()
	second := w.wantJobs("p")[0]
	if second.Release != "4.2" {
		t.Errorf("rebuild release %q, want 4.2", second.Release)
	}
	w.finish(second, binary("p", "1"))

	w.addPackage("p", map[string]string{"p.spec": strings.Replace(spec("p", nil), "Version: 1", "Version: 2", 1)})
	w.resolveAs("2", 1)
	w.run()
	third := w.wantJobs("p")[0]
	if third.Versrel != "2-1" || third.Release != "1.1" {
		t.Errorf("new version job %q release %q, want versrel 2-1 release 1.1 (B restarts for a new versrel)", third.Versrel, third.Release)
	}
}

func TestLegacyFloorKeepsReleasesAboveInstalledBuilds(t *testing.T) {
	tests := []struct {
		name        string
		git         int
		wantRelease string
	}{
		{"CI not above the old one continues B", 1, "1.131"},
		{"higher CI starts at 1", 2, "2.1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, nil, withPendingRevisions)
			commit := w.pkg("p", nil)
			w.store.AddLegacyCounters(storage.CounterKey{
				Project: testProject, Repo: testRepo, Arch: testArch, Package: "p", SourceCommit: "old", Meta: "m",
			}, storage.BuildCounters{CI: 1, B: 130})
			w.resolveAs("1", tc.git)
			w.run()
			job := w.wantJobs("p")[0]
			if job.Release != tc.wantRelease || job.SourceCommit != commit {
				t.Errorf("release %q, want %q", job.Release, tc.wantRelease)
			}
		})
	}
}

func TestReleaseIsNotPartOfTheMeta(t *testing.T) {
	w := newWorld(t, nil, withPendingRevisions)
	commit := w.pkg("p", nil)
	w.resolveAs("1", 1)
	w.run()
	job := w.wantJobs("p")[0]
	w.finish(job, binary("p", "1"))
	// An operator moves the release number: nothing may be rebuilt for it.
	if _, err := w.store.SetRevisionVrev(context.Background(), testProject, "p", commit, "1", 50); err != nil {
		t.Fatal(err)
	}
	w.run()
	w.wantJobs()
	w.wantState("p", storage.StateSucceeded, "")
}
