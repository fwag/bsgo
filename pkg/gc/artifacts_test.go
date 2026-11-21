package gc_test

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/gc"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/storage"
)

const (
	unitDir = "artifacts/p/standard/x86_64/patch/"
	armDir  = "artifacts/p/standard/aarch64/tool/"
)

type artEnv struct {
	t     *testing.T
	objs  *gc.MemoryObjects
	store *storage.MemoryStore
}

func newArtEnv(t *testing.T, packages ...string) *artEnv {
	t.Helper()
	e := &artEnv{t: t, objs: gc.NewMemoryObjects(), store: storage.NewMemoryStore()}
	for _, name := range packages {
		pkg := &manifest.PackageManifest{Package: manifest.PackageInfo{Name: name}, Source: manifest.PackageSource{Git: "g"}}
		if err := e.store.UpsertPackage(context.Background(), "p", pkg); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

// index writes the entry list of prpa and points the prpa at it; every key
// becomes an entry.
func (e *artEnv) index(p buildrepo.Prpa, hash string, keys ...string) {
	e.t.Helper()
	entries := make([]buildrepo.Entry, len(keys))
	for i, k := range keys {
		entries[i] = buildrepo.Entry{Unit: "u", Key: k, Name: "n"}
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		e.t.Fatal(err)
	}
	e.objs.Put(buildrepo.EntriesKey(p, hash), raw, ago(30*day))
	e.objs.Put(buildrepo.SolvKey(p, hash), []byte("solv"), ago(30*day))
	point(e.t, e.store, p, hash)
}

func (e *artEnv) put(key string, age time.Duration) { e.objs.Put(key, []byte("data"), ago(age)) }

// bininfo stores the bininfo.json of a unit listing files.
func (e *artEnv) bininfo(dir string, age time.Duration, files ...string) {
	e.t.Helper()
	infos := make([]bininfo.BinInfo, len(files))
	for i, f := range files {
		infos[i] = bininfo.BinInfo{Filename: f, Name: f}
	}
	raw, err := bininfo.Marshal(infos)
	if err != nil {
		e.t.Fatal(err)
	}
	e.objs.Put(dir+bininfo.FileName, raw, ago(age))
}

func (e *artEnv) refs() *gc.Refs {
	return &gc.Refs{Pointers: e.store, Objects: e.objs, Now: func() time.Time { return now }, Grace: day}
}

// indexAt is like index but stores the version with the given age and lets
// the entries carry versions and releases.
func (e *artEnv) indexAt(p buildrepo.Prpa, hash string, age time.Duration, entries ...buildrepo.Entry) {
	e.t.Helper()
	raw, err := json.Marshal(entries)
	if err != nil {
		e.t.Fatal(err)
	}
	e.objs.Put(buildrepo.EntriesKey(p, hash), raw, ago(age))
	e.objs.Put(buildrepo.SolvKey(p, hash), []byte("solv"), ago(age))
	point(e.t, e.store, p, hash)
}

func (e *artEnv) plan(opts gc.Options) []gc.Plan {
	e.t.Helper()
	plans, err := gc.PlanArtifactGC(context.Background(), e.refs(), e.store, now, opts)
	if err != nil {
		e.t.Fatal(err)
	}
	return plans
}

func deleted(plans []gc.Plan) []string {
	var out []string
	for _, p := range plans {
		out = append(out, p.Delete...)
	}
	return out
}

func TestPlanArtifactGC(t *testing.T) {
	x86 := testPrpa
	arm := buildrepo.Prpa{Project: "p", Repo: "standard", Arch: "aarch64"}
	opts := gc.Options{Grace: day}
	tests := []struct {
		name  string
		setup func(e *artEnv)
		want  []string
	}{
		{
			name: "old release deleted, current kept",
			setup: func(e *artEnv) {
				e.index(x86, "h", unitDir+"patch-2.7.6-1.16.x86_64.rpm")
				e.put(unitDir+"patch-2.7.6-1.5.x86_64.rpm", 10*day)
				e.put(unitDir+"patch-2.7.6-1.16.x86_64.rpm", 2*day)
				e.bininfo(unitDir, 2*day, "patch-2.7.6-1.16.x86_64.rpm")
				e.put(unitDir+"build.log", 2*day)
			},
			want: []string{unitDir + "patch-2.7.6-1.5.x86_64.rpm"},
		},
		{
			name: "exported binary referenced from another arch kept",
			setup: func(e *artEnv) {
				e.index(x86, "h", unitDir+"patch-2.7.6-1.16.x86_64.rpm")
				e.index(arm, "h", unitDir+"patch-doc-2.7.6-1.16.noarch.rpm")
				e.put(unitDir+"patch-2.7.6-1.16.x86_64.rpm", 2*day)
				e.put(unitDir+"patch-doc-2.7.6-1.16.noarch.rpm", 2*day)
				e.put(unitDir+"patch-doc-2.7.6-1.5.noarch.rpm", 10*day)
				// The current bininfo lists only the own architecture's file,
				// as the unit's own entries do not include the export.
				e.bininfo(unitDir, 2*day, "patch-2.7.6-1.16.x86_64.rpm")
			},
			want: []string{unitDir + "patch-doc-2.7.6-1.5.noarch.rpm"},
		},
		{
			name: "debuginfo and source RPM of the current build kept, those of an old build deleted",
			setup: func(e *artEnv) {
				e.index(x86, "h", unitDir+"patch-2-2.x86_64.rpm")
				for _, f := range []string{"patch-2-2.x86_64.rpm", "patch-debuginfo-2-2.x86_64.rpm", "patch-debugsource-2-2.x86_64.rpm", "patch-2-2.src.rpm"} {
					e.put(unitDir+f, 3*day)
				}
				for _, f := range []string{"patch-1-1.x86_64.rpm", "patch-debuginfo-1-1.x86_64.rpm", "patch-debugsource-1-1.x86_64.rpm", "patch-1-1.src.rpm"} {
					e.put(unitDir+f, 9*day)
				}
				e.bininfo(unitDir, 3*day, "patch-2-2.x86_64.rpm", "patch-debuginfo-2-2.x86_64.rpm", "patch-debugsource-2-2.x86_64.rpm", "patch-2-2.src.rpm")
			},
			want: []string{
				unitDir + "patch-1-1.src.rpm", unitDir + "patch-1-1.x86_64.rpm",
				unitDir + "patch-debuginfo-1-1.x86_64.rpm", unitDir + "patch-debugsource-1-1.x86_64.rpm",
			},
		},
		{
			name: "young objects kept",
			setup: func(e *artEnv) {
				e.index(x86, "h", unitDir+"patch-2-2.x86_64.rpm")
				e.put(unitDir+"patch-2-2.x86_64.rpm", 2*day)
				e.bininfo(unitDir, 2*day, "patch-2-2.x86_64.rpm")
				e.put(unitDir+"patch-1-1.x86_64.rpm", 2*time.Hour)
			},
		},
		{
			name: "removed unit deleted after grace, young removed unit kept",
			setup: func(e *artEnv) {
				e.index(x86, "h", unitDir+"patch-2-2.x86_64.rpm")
				e.put(unitDir+"patch-2-2.x86_64.rpm", 2*day)
				e.bininfo(unitDir, 2*day, "patch-2-2.x86_64.rpm")
				gone := "artifacts/p/standard/x86_64/gone/"
				e.put(gone+"gone-1-1.x86_64.rpm", 9*day)
				e.bininfo(gone, 9*day, "gone-1-1.x86_64.rpm")
				e.put(gone+"build.log", 9*day)
				young := "artifacts/p/standard/x86_64/fresh/"
				e.put(young+"fresh-1-1.x86_64.rpm", time.Hour)
			},
			want: []string{
				"artifacts/p/standard/x86_64/gone/bininfo.json",
				"artifacts/p/standard/x86_64/gone/build.log",
				"artifacts/p/standard/x86_64/gone/gone-1-1.x86_64.rpm",
			},
		},
		{
			name: "metadata of a live unit kept, other files of old builds deleted",
			setup: func(e *artEnv) {
				e.index(x86, "h", unitDir+"patch-2-2.x86_64.rpm")
				e.put(unitDir+"patch-2-2.x86_64.rpm", 3*day)
				e.bininfo(unitDir, 3*day, "patch-2-2.x86_64.rpm")
				for _, f := range []string{"_slsa_provenance.json", "_statistics", "build.log"} {
					e.put(unitDir+f, 3*day)
				}
				e.put(unitDir+"image-1.qcow2", 9*day)
				e.put(unitDir+"image-2.qcow2", 3*day)
			},
			want: []string{unitDir + "image-1.qcow2"},
		},
		{
			name: "live unit without bininfo kept as a whole",
			setup: func(e *artEnv) {
				e.index(x86, "h", unitDir+"patch-2-2.x86_64.rpm")
				e.put(unitDir+"patch-2-2.x86_64.rpm", 3*day)
				e.put(unitDir+"patch-1-1.x86_64.rpm", 9*day)
			},
		},
		{
			name: "flavored unit directory with doubled flavor name is handled",
			setup: func(e *artEnv) {
				dir := "artifacts/p/standard/x86_64/gcc13:cross-aarch64-gcc13:cross-aarch64-gcc13/"
				e.index(x86, "h", dir+"cross-2-2.x86_64.rpm")
				e.put(dir+"cross-2-2.x86_64.rpm", 3*day)
				e.put(dir+"cross-1-1.x86_64.rpm", 9*day)
				e.bininfo(dir, 3*day, "cross-2-2.x86_64.rpm")
			},
			want: []string{"artifacts/p/standard/x86_64/gcc13:cross-aarch64-gcc13:cross-aarch64-gcc13/cross-1-1.x86_64.rpm"},
		},
		{
			name: "foreign key shapes are not touched",
			setup: func(e *artEnv) {
				e.index(x86, "h", unitDir+"patch-2-2.x86_64.rpm")
				e.put("artifacts/p/legacy-pkg/old.rpm", 99*day)
				e.put("artifacts/p/standard/x86_64/top.rpm", 99*day)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newArtEnv(t, "patch", "gcc13", "tool")
			tc.setup(e)
			if got := deleted(e.plan(opts)); !sameSet(got, tc.want) {
				t.Errorf("delete %v\nwant %v", got, tc.want)
			}
		})
	}
}

func TestPlanArtifactGCUnreadableBininfoKeepsUnitAndWarns(t *testing.T) {
	e := newArtEnv(t, "patch")
	e.index(testPrpa, "h", unitDir+"patch-2-2.x86_64.rpm")
	e.put(unitDir+"patch-2-2.x86_64.rpm", 3*day)
	e.put(unitDir+"patch-1-1.x86_64.rpm", 9*day)
	e.objs.Put(unitDir+bininfo.FileName, []byte("not json"), ago(3*day))
	plans := e.plan(gc.Options{Grace: day})
	if len(deleted(plans)) != 0 || len(plans[0].Warnings) != 1 {
		t.Errorf("delete %v, warnings %v", deleted(plans), plans[0].Warnings)
	}
}

func TestPlanArtifactGCRefusesEmptyPointerTable(t *testing.T) {
	e := newArtEnv(t, "patch")
	e.put(unitDir+"patch-1-1.x86_64.rpm", 9*day)
	if _, err := gc.PlanArtifactGC(context.Background(), e.refs(), e.store, now, gc.Options{Grace: day}); err == nil {
		t.Fatal("empty pointer table must be refused")
	}
}

func TestPlanArtifactGCProjectFilter(t *testing.T) {
	e := newArtEnv(t, "patch")
	e.index(testPrpa, "h", unitDir+"patch-2-2.x86_64.rpm")
	e.put(unitDir+"patch-1-1.x86_64.rpm", 9*day)
	e.put("artifacts/q/standard/x86_64/zzz/zzz-1-1.x86_64.rpm", 9*day)
	got := deleted(e.plan(gc.Options{Grace: day, Project: "q"}))
	if !sameSet(got, []string{"artifacts/q/standard/x86_64/zzz/zzz-1-1.x86_64.rpm"}) {
		t.Errorf("delete %v", got)
	}
}

// A binary that becomes referenced between planning and applying (a build
// reuses an old file name, or a pointer moves) must survive.
func TestApplyArtifactsHonoursPointerSwapSincePlanning(t *testing.T) {
	e := newArtEnv(t, "patch")
	e.index(testPrpa, "h1", unitDir+"patch-2-2.x86_64.rpm")
	e.put(unitDir+"patch-2-2.x86_64.rpm", 9*day)
	e.put(unitDir+"patch-1-1.x86_64.rpm", 9*day)
	e.bininfo(unitDir, 9*day, "patch-2-2.x86_64.rpm")
	refs := e.refs()
	plans, err := gc.PlanArtifactGC(context.Background(), refs, e.store, now, gc.Options{Grace: day})
	if err != nil {
		t.Fatal(err)
	}
	if !sameSet(deleted(plans), []string{unitDir + "patch-1-1.x86_64.rpm"}) {
		t.Fatalf("plan %v", deleted(plans))
	}

	e.index(testPrpa, "h2", unitDir+"patch-1-1.x86_64.rpm")
	res := gc.Apply(context.Background(), e.objs, plans, false, refs.Keys)
	if !e.objs.Has(unitDir+"patch-1-1.x86_64.rpm") || res[0].Deleted != 0 {
		t.Errorf("newly referenced binary deleted: %+v", res[0])
	}
}

func entry(key, ver, rel string) buildrepo.Entry {
	return buildrepo.Entry{Unit: "patch", Key: key, Name: "patch", Version: ver, Release: rel}
}

// An RPM uploaded long ago that dropped out of the index a minute ago is still
// needed by jobs and publisher runs that loaded the previous pointer.
func TestPlanArtifactGCKeepsBinariesOfRecentlySupersededIndex(t *testing.T) {
	tests := []struct {
		name       string
		currentAge time.Duration
		want       []string
	}{
		{"superseded a minute ago", time.Minute, nil},
		{"superseded long ago", 2 * day, []string{unitDir + "patch-1-1.x86_64.rpm"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newArtEnv(t, "patch")
			e.indexAt(testPrpa, "h1", 30*day, entry(unitDir+"patch-1-1.x86_64.rpm", "1", "1"))
			e.indexAt(testPrpa, "h2", tc.currentAge, entry(unitDir+"patch-2-2.x86_64.rpm", "2", "2"))
			e.put(unitDir+"patch-1-1.x86_64.rpm", 30*day)
			e.put(unitDir+"patch-2-2.x86_64.rpm", tc.currentAge)
			e.bininfo(unitDir, tc.currentAge, "patch-2-2.x86_64.rpm")
			if got := deleted(e.plan(gc.Options{Grace: day})); !sameSet(got, tc.want) {
				t.Errorf("delete %v, want %v", got, tc.want)
			}
		})
	}
}

// Build N+1 uploaded its bininfo.json and then failed: the debug and source
// RPMs of build N, which the index still references, must stay.
func TestPlanArtifactGCKeepsFilesOfIndexedBuildWhenBininfoIsNewer(t *testing.T) {
	e := newArtEnv(t, "patch")
	e.indexAt(testPrpa, "h", 30*day, entry(unitDir+"patch-2-2.x86_64.rpm", "2", "2"))
	for _, f := range []string{"patch-2-2.x86_64.rpm", "patch-debuginfo-2-2.x86_64.rpm", "patch-debugsource-2-2.x86_64.rpm", "patch-2-2.src.rpm"} {
		e.put(unitDir+f, 9*day)
	}
	for _, f := range []string{"patch-debuginfo-1-1.x86_64.rpm", "patch-1-1.src.rpm"} {
		e.put(unitDir+f, 20*day)
	}
	e.bininfo(unitDir, 2*day, "patch-3-3.x86_64.rpm", "patch-debuginfo-3-3.x86_64.rpm")
	want := []string{unitDir + "patch-debuginfo-1-1.x86_64.rpm", unitDir + "patch-1-1.src.rpm"}
	if got := deleted(e.plan(gc.Options{Grace: day})); !sameSet(got, want) {
		t.Errorf("delete %v, want %v", got, want)
	}
}

func TestPlanArtifactGCMissingModTimeCountsAsYoung(t *testing.T) {
	e := newArtEnv(t, "patch")
	e.index(testPrpa, "h", unitDir+"patch-2-2.x86_64.rpm")
	e.put(unitDir+"patch-2-2.x86_64.rpm", 3*day)
	e.bininfo(unitDir, 3*day, "patch-2-2.x86_64.rpm")
	e.objs.Put(unitDir+"patch-1-1.x86_64.rpm", []byte("x"), time.Time{})
	if got := deleted(e.plan(gc.Options{Grace: day})); len(got) != 0 {
		t.Errorf("delete %v", got)
	}
}

// A key uploaded again between planning and deleting (same name, same index
// hash) must be skipped.
func TestApplySkipsKeysUploadedAgainSincePlanning(t *testing.T) {
	e := newArtEnv(t, "patch")
	e.index(testPrpa, "h", unitDir+"patch-2-2.x86_64.rpm")
	e.put(unitDir+"patch-2-2.x86_64.rpm", 3*day)
	e.put(unitDir+"patch-1-1.x86_64.rpm", 9*day)
	e.put(unitDir+"patch-0-0.x86_64.rpm", 9*day)
	e.bininfo(unitDir, 3*day, "patch-2-2.x86_64.rpm")
	refs := e.refs()
	plans, err := gc.PlanArtifactGC(context.Background(), refs, e.store, now, gc.Options{Grace: day})
	if err != nil {
		t.Fatal(err)
	}
	e.put(unitDir+"patch-1-1.x86_64.rpm", 0)
	res := gc.Apply(context.Background(), e.objs, plans, false, refs.Keys)
	if !e.objs.Has(unitDir+"patch-1-1.x86_64.rpm") || e.objs.Has(unitDir+"patch-0-0.x86_64.rpm") || res[0].Deleted != 1 {
		t.Errorf("objects %v, result %+v", e.objs.Keys(), res[0])
	}
}

type failingProjects struct {
	gc.Projects
	err error
}

func (f failingProjects) ListPackages(context.Context, string) ([]*manifest.PackageManifest, error) {
	return nil, f.err
}

func TestPlanArtifactGCPackageListingProblemsDeleteNothing(t *testing.T) {
	gone := "artifacts/p/standard/x86_64/gone/"
	setup := func(e *artEnv) {
		e.index(testPrpa, "h", unitDir+"patch-2-2.x86_64.rpm")
		e.put(unitDir+"patch-2-2.x86_64.rpm", 3*day)
		e.put(gone+"gone-1-1.x86_64.rpm", 9*day)
	}
	t.Run("listing error", func(t *testing.T) {
		e := newArtEnv(t, "patch")
		setup(e)
		plans, err := gc.PlanArtifactGC(context.Background(), e.refs(), failingProjects{Projects: e.store, err: errors.New("db down")}, now, gc.Options{Grace: day})
		if err != nil {
			t.Fatal(err)
		}
		if len(deleted(plans)) != 0 || plans[0].Error == "" {
			t.Errorf("plans %+v", plans)
		}
	})
	t.Run("project with pointers but no packages", func(t *testing.T) {
		e := newArtEnv(t)
		setup(e)
		plans := e.plan(gc.Options{Grace: day})
		if len(deleted(plans)) != 0 || plans[0].Error == "" {
			t.Errorf("plans %+v", plans)
		}
		res := gc.Apply(context.Background(), e.objs, plans, false, e.refs().Keys)
		if res[0].Deleted != 0 || !e.objs.Has(gone+"gone-1-1.x86_64.rpm") {
			t.Errorf("an errored plan deleted objects: %+v", res[0])
		}
	})
	t.Run("unit with a build state row exists", func(t *testing.T) {
		e := newArtEnv(t, "other")
		setup(e)
		st := &storage.PackageBuildState{Project: "p", Repo: "standard", Arch: "x86_64", Package: "gone", State: "succeeded"}
		if err := e.store.UpsertBuildState(context.Background(), st); err != nil {
			t.Fatal(err)
		}
		e.put(gone+"gone-0-0.x86_64.rpm", 9*day)
		e.bininfo(gone, 5*day, "gone-1-1.x86_64.rpm")
		if got := deleted(e.plan(gc.Options{Grace: day})); !sameSet(got, []string{gone + "gone-0-0.x86_64.rpm"}) {
			t.Errorf("delete %v", got)
		}
	})
}

func TestPlanWarningsAreSorted(t *testing.T) {
	e := newArtEnv(t, "patch", "tool")
	e.index(testPrpa, "h", unitDir+"patch-2-2.x86_64.rpm")
	for _, d := range []string{unitDir, "artifacts/p/standard/x86_64/tool/", "artifacts/p/standard/x86_64/patch2/"} {
		e.put(d+"x-1-1.x86_64.rpm", 9*day)
		e.objs.Put(d+bininfo.FileName, []byte("bad"), ago(9*day))
	}
	e.put(unitDir+"patch-2-2.x86_64.rpm", 3*day)
	w := e.plan(gc.Options{Grace: day})[0].Warnings
	if len(w) < 2 || !sort.StringsAreSorted(w) {
		t.Errorf("warnings %v", w)
	}
}
