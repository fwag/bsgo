package checker_test

import (
	"context"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// TestChainOrdering builds A <- B <- C without any bootstrap repository: each
// unit waits for its provider and the jobs come out in dependency order.
func TestChainOrdering(t *testing.T) {
	w := newWorld(t, nil)
	w.pkg("a", nil)
	w.pkg("b", []string{"a"})
	w.pkg("c", []string{"b"})

	w.run()
	jobs := w.wantJobs("a")
	w.wantState("a", storage.StateScheduled, "new build")
	w.wantState("b", storage.StateBlocked, "blocked: a")
	w.wantState("c", storage.StateBlocked, "blocked: b")

	w.run() // nothing changed: a is still building, no duplicate job
	w.wantJobs()

	w.finish(jobs[0], binary("a", "1"))
	res := w.run()
	jobs = w.wantJobs("b")
	if res.Counts[storage.StateSucceeded] != 1 {
		t.Errorf("counts %v, want a succeeded", res.Counts)
	}
	w.wantState("a", storage.StateSucceeded, "")
	w.wantState("c", storage.StateBlocked, "blocked: b")

	w.finish(jobs[0], binary("b", "1"))
	w.run()
	jobs = w.wantJobs("c")
	w.finish(jobs[0], binary("c", "1"))
	w.run()
	w.wantJobs()
	for _, u := range []string{"a", "b", "c"} {
		w.wantState(u, storage.StateSucceeded, "")
	}
}

// TestBlockedOnExpandedDependency covers a provider that is available from
// the bootstrap repository: the consumer still waits for the in-prpa unit.
func TestBlockedOnExpandedDependency(t *testing.T) {
	w := newWorld(t, []bininfo.BinInfo{binary("a", "0")})
	w.pkg("a", nil)
	w.pkg("b", []string{"a"})

	w.run()
	jobs := w.wantJobs("a")
	w.wantState("b", storage.StateBlocked, "blocked: a")
	if len(jobs) != 1 {
		t.Fatal("want one job")
	}
}

func TestBootstrapDependencyShadowedByOwnBinaries(t *testing.T) {
	boot := []bininfo.BinInfo{binary("lib", "0")}
	w := newWorld(t, boot)
	w.pkg("lib", nil)
	w.pkg("app", []string{"lib"})

	w.run()
	libJob := w.wantJobs("lib")[0]
	w.finish(libJob, binary("lib", "1"))
	w.run()
	appJob := w.wantJobs("app")[0]

	if len(appJob.Bdeps) != 1 {
		t.Fatalf("bdeps %+v, want exactly lib", appJob.Bdeps)
	}
	b := appJob.Bdeps[0]
	if b.Name != "lib" || b.Version != "1" || b.Source.S3Key == "" || b.Source.URL != "" {
		t.Errorf("app builds against %+v, want the own lib-1 from the build repository", b)
	}
	if !strings.HasPrefix(b.Source.S3Key, "artifacts/"+testProject+"/"+testRepo+"/"+testArch+"/lib/") {
		t.Errorf("unexpected key %s", b.Source.S3Key)
	}
}

func TestBootstrapBdepComesFromURL(t *testing.T) {
	w := newWorld(t, []bininfo.BinInfo{binary("lib", "0")})
	w.pkg("app", []string{"lib"})
	w.run()
	job := w.wantJobs("app")[0]
	if len(job.Bdeps) != 1 {
		t.Fatalf("bdeps %+v", job.Bdeps)
	}
	b := job.Bdeps[0]
	want := bootURL + "x86_64/lib-0-1.x86_64.rpm"
	if b.Source.URL != want || b.Source.S3Key != "" {
		t.Errorf("source %+v, want url %s", b.Source, want)
	}
	if b.SHA256 != sha("lib0") || b.NotMeta {
		t.Errorf("bdep %+v: want the repository sha256 and a meta dependency", b)
	}
	if job.SourceCommit == "" || job.Meta == "" || job.Release != "1.1" {
		t.Errorf("job %+v: want commit, meta and release 1.1", job)
	}
}

func TestUnresolvablePassesOBSTextThrough(t *testing.T) {
	w := newWorld(t, []bininfo.BinInfo{binary("lib", "0")})
	w.pkg("app", []string{"nothing-like-this"})
	w.run()
	w.wantJobs()
	st := w.state("app")
	if st.State != storage.StateUnresolvable {
		t.Fatalf("state %s, want unresolvable", st.State)
	}
	if !strings.HasPrefix(st.Details, "nothing provides nothing-like-this") {
		t.Errorf("details %q, want the OBS wording", st.Details)
	}
}

func TestExcludedAndDisabled(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		mutate  func(*manifest.PackageManifest)
		cfg     *manifest.ProjectConfig
		state   string
		details string
	}{
		{
			name:  "exclusive arch",
			files: map[string]string{"p.spec": "ExclusiveArch: aarch64\n" + spec("p", nil)},
			state: storage.StateExcluded,
		},
		{
			name:  "exclude arch",
			files: map[string]string{"p.spec": "ExcludeArch: x86_64\n" + spec("p", nil)},
			state: storage.StateExcluded,
		},
		{
			name:   "manifest disabled",
			files:  map[string]string{"p.spec": spec("p", nil)},
			mutate: func(m *manifest.PackageManifest) { m.Build.Enabled = false },
			state:  storage.StateDisabled,
		},
		{
			name:   "manifest target disabled",
			files:  map[string]string{"p.spec": spec("p", nil)},
			mutate: func(m *manifest.PackageManifest) { m.Build.Targets = map[string]bool{testRepo: false} },
			state:  storage.StateDisabled,
		},
		{
			name:   "manifest arch exclude",
			files:  map[string]string{"p.spec": spec("p", nil)},
			mutate: func(m *manifest.PackageManifest) { m.Build.Architectures.Exclude = []string{testArch} },
			state:  storage.StateExcluded,
		},
		{
			name:    "excludebuild flag",
			files:   map[string]string{"p.spec": spec("p", nil)},
			cfg:     &manifest.ProjectConfig{BuildFlags: []string{"excludebuild:p"}},
			state:   storage.StateExcluded,
			details: "package blacklist",
		},
		{
			name:    "onlybuild flag",
			files:   map[string]string{"p.spec": spec("p", nil)},
			cfg:     &manifest.ProjectConfig{BuildFlags: []string{"onlybuild:other"}},
			state:   storage.StateExcluded,
			details: "package whitelist",
		},
		{
			name:    "no recipe",
			files:   map[string]string{"README": "x"},
			state:   storage.StateBroken,
			details: "no recipe file",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, nil, func(w *world) { w.prj.PublicConfig = tc.cfg })
			var mut []func(*manifest.PackageManifest)
			if tc.mutate != nil {
				mut = append(mut, tc.mutate)
			}
			w.addPackage("p", tc.files, mut...)
			w.run()
			w.wantJobs()
			w.wantState("p", tc.state, tc.details)
		})
	}
}

func TestMetaUnchangedNoRebuildChangedRebuild(t *testing.T) {
	w := newWorld(t, nil)
	w.pkg("lib", nil)
	w.pkg("app", []string{"lib"})
	w.run()
	w.finish(w.wantJobs("lib")[0], binary("lib", "1"))
	w.run()
	w.finish(w.wantJobs("app")[0], binary("app", "1"))

	w.run()
	w.wantJobs() // same sources, same dependency binaries: nothing to do
	w.wantState("app", storage.StateSucceeded, "")

	// lib was rebuilt with identical sources: new binary identity.
	w.pkg("lib", nil) // same content, same commit
	libRow := w.state("lib")
	rebuild := *libRow
	rebuild.JobID = "rebuilt"
	w.entries["lib"][0].SHA256 = sha("a-new-build")
	w.publishIndex()
	w.run()
	jobs := w.wantJobs("app")
	if jobs[0].Explain != "meta change" {
		t.Errorf("explain %q, want meta change", jobs[0].Explain)
	}
	w.wantState("app", storage.StateScheduled, "meta change")
}

func TestSourceChangeSchedulesAndCancelsInFlight(t *testing.T) {
	w := newWorld(t, nil)
	w.addPackage("p", map[string]string{"p.spec": spec("p", nil)})
	w.run()
	first := w.wantJobs("p")[0]

	w.addPackage("p", map[string]string{"p.spec": spec("p", nil) + "# changed\n"})
	w.run()
	second := w.wantJobs("p")[0]
	if second.Explain != "source change" || second.JobID == first.JobID {
		t.Errorf("second job %+v, want a new job for a source change", second)
	}
	if len(w.sink.cancelled) != 1 || w.sink.cancelled[0] != first.JobID {
		t.Errorf("cancelled %v, want the first job", w.sink.cancelled)
	}
}

func TestMultibuildFlavorIsSeparateUnit(t *testing.T) {
	w := newWorld(t, nil)
	w.addPackage("m", map[string]string{
		"m.spec":      spec("m", nil),
		"_multibuild": "<multibuild><flavor>mini</flavor></multibuild>",
	})
	w.run()
	jobs := w.wantJobs("m", "m:mini")
	if jobs[1].BasePackage != "m" || jobs[1].Flavor != "mini" {
		t.Errorf("flavor job %+v", jobs[1])
	}
	w.wantState("m", storage.StateScheduled, "new build")
	st := w.state("m:mini")
	if st.Package != "m" || st.Flavor != "mini" {
		t.Errorf("row %+v: want package m flavor mini", st)
	}
}

func TestForcedRebuild(t *testing.T) {
	w := newWorld(t, nil)
	w.pkg("p", nil)
	w.run()
	first := w.wantJobs("p")[0]
	w.finish(first, binary("p", "1"))
	w.run()
	w.wantJobs()

	err := w.store.RequestForcedRebuild(context.Background(), storage.ForcedRebuild{Project: testProject, Package: "p"})
	if err != nil {
		t.Fatal(err)
	}
	w.run()
	second := w.wantJobs("p")[0]
	if second.Explain != "forced rebuild" || second.BCnt != first.BCnt+1 || second.Release != "1.2" {
		t.Errorf("forced job %+v, want B_CNT %d and release 1.2", second, first.BCnt+1)
	}
	left, _ := w.store.ListForcedRebuilds(context.Background(), testProject)
	if len(left) != 0 {
		t.Errorf("forced request not consumed: %v", left)
	}
}

func TestSourcesUnavailableIsBlockedWithRetry(t *testing.T) {
	w := newWorld(t, nil)
	w.pkg("p", nil)
	w.recipes.err = context.DeadlineExceeded
	res := w.run()
	if res.RetryAfter <= 0 {
		t.Error("want a retry delay when sources are unavailable")
	}
	st := w.state("p")
	if st.State != storage.StateBlocked || !strings.HasPrefix(st.Details, "source unavailable") {
		t.Errorf("state %+v", st)
	}
}

func TestRebuildPolicyLocalIgnoresDependencyChanges(t *testing.T) {
	tests := []struct {
		policy   string
		wantJobs []string
	}{
		{"local", nil},
		{"transitive", []string{"app"}},
	}
	for _, tc := range tests {
		t.Run(tc.policy, func(t *testing.T) {
			w := newWorld(t, nil, func(w *world) {
				tgt := w.prj.Targets[testRepo]
				tgt.Flags.Rebuild = tc.policy
				w.prj.Targets[testRepo] = tgt
			})
			w.pkg("lib", nil)
			w.pkg("app", []string{"lib"})
			w.run()
			w.finish(w.wantJobs("lib")[0], binary("lib", "1"))
			w.run()
			w.finish(w.wantJobs("app")[0], binary("app", "1"))

			w.entries["lib"][0].SHA256 = sha("a-new-lib-build")
			w.publishIndex()
			w.run()
			w.wantJobs(tc.wantJobs...)
		})
	}
}
