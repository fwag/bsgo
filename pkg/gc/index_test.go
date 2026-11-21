package gc_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/gc"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/storage"
)

var now = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) time.Time { return now.Add(-d) }

const day = 24 * time.Hour

var testPrpa = buildrepo.Prpa{Project: "p", Repo: "standard", Arch: "x86_64"}

// putIndex stores the pair of index version hash of prpa with the given age.
func putIndex(o *gc.MemoryObjects, p buildrepo.Prpa, hash string, age time.Duration) (solv, entries string) {
	solv, entries = buildrepo.SolvKey(p, hash), buildrepo.EntriesKey(p, hash)
	o.Put(solv, []byte("solv-"+hash), ago(age))
	o.Put(entries, []byte("[]"), ago(age))
	return solv, entries
}

func point(t *testing.T, s *storage.MemoryStore, p buildrepo.Prpa, hash string) {
	t.Helper()
	err := s.SaveBuildRepoIndex(context.Background(), &storage.BuildRepoIndex{
		Project: p.Project, Repo: p.Repo, Arch: p.Arch, ContentHash: hash,
		SolvKey: buildrepo.SolvKey(p, hash), EntriesKey: buildrepo.EntriesKey(p, hash),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func pair(p buildrepo.Prpa, hash string) []string {
	return []string{buildrepo.EntriesKey(p, hash), buildrepo.SolvKey(p, hash)}
}

func TestPlanIndexGC(t *testing.T) {
	other := buildrepo.Prpa{Project: "gone", Repo: "standard", Arch: "x86_64"}
	tests := []struct {
		name string
		// versions are hash -> age for testPrpa; "cur" is the pointer target.
		versions   map[string]time.Duration
		current    string
		orphans    map[string]time.Duration
		keepN      int
		wantDelete []string
		wantKeep   []string
	}{
		{
			name:     "current pair kept",
			versions: map[string]time.Duration{"cur": 30 * day}, current: "cur", keepN: 0,
			wantKeep: pair(testPrpa, "cur"),
		},
		{
			name:     "last N superseded versions kept, older deleted together",
			versions: map[string]time.Duration{"cur": 1 * day, "v1": 5 * day, "v2": 4 * day, "v3": 3 * day, "v4": 2 * day}, current: "cur", keepN: 2,
			wantKeep:   append(append(pair(testPrpa, "cur"), pair(testPrpa, "v3")...), pair(testPrpa, "v4")...),
			wantDelete: append(pair(testPrpa, "v1"), pair(testPrpa, "v2")...),
		},
		{
			name:     "young superseded versions kept beyond N",
			versions: map[string]time.Duration{"cur": 1 * time.Hour, "v1": 2 * time.Hour, "v2": 3 * time.Hour, "v3": 4 * time.Hour}, current: "cur", keepN: 0,
			wantKeep: append(append(append(pair(testPrpa, "cur"), pair(testPrpa, "v1")...), pair(testPrpa, "v2")...), pair(testPrpa, "v3")...),
		},
		{
			name:     "grace is measured on the newer object of the pair",
			versions: map[string]time.Duration{"cur": 30 * day, "old": 30 * day}, current: "cur", keepN: 0,
			wantKeep:   pair(testPrpa, "cur"),
			wantDelete: pair(testPrpa, "old"),
		},
		{
			name:     "orphan prpa deleted after grace",
			versions: map[string]time.Duration{"cur": 30 * day}, current: "cur", keepN: 3,
			orphans:    map[string]time.Duration{"a": 5 * day, "b": 3 * day},
			wantKeep:   pair(testPrpa, "cur"),
			wantDelete: append(pair(other, "a"), pair(other, "b")...),
		},
		{
			name:     "young orphan kept, and the older version it superseded with it",
			versions: map[string]time.Duration{"cur": 30 * day}, current: "cur", keepN: 3,
			orphans:  map[string]time.Duration{"a": 5 * day, "b": 2 * time.Hour},
			wantKeep: append(append(pair(testPrpa, "cur"), pair(other, "a")...), pair(other, "b")...),
		},
		{
			name:     "version superseded less than grace ago is kept whatever its age and KeepVersions",
			versions: map[string]time.Duration{"cur": time.Minute, "old": 90 * day, "older": 91 * day}, current: "cur", keepN: 0,
			wantKeep:   append(pair(testPrpa, "cur"), pair(testPrpa, "old")...),
			wantDelete: pair(testPrpa, "older"),
		},
		{
			name:     "version superseded longer than grace ago is deleted",
			versions: map[string]time.Duration{"cur": 2 * day, "old": 90 * day}, current: "cur", keepN: 0,
			wantKeep:   pair(testPrpa, "cur"),
			wantDelete: pair(testPrpa, "old"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			objs, store := gc.NewMemoryObjects(), storage.NewMemoryStore()
			for h, age := range tc.versions {
				putIndex(objs, testPrpa, h, age)
			}
			for h, age := range tc.orphans {
				putIndex(objs, other, h, age)
			}
			point(t, store, testPrpa, tc.current)

			plans, err := gc.PlanIndexGC(context.Background(), store, objs, now, gc.Options{Grace: day, KeepVersions: tc.keepN})
			if err != nil {
				t.Fatal(err)
			}
			var keep, del []string
			for _, p := range plans {
				keep, del = append(keep, p.Keep...), append(del, p.Delete...)
			}
			if !sameSet(keep, tc.wantKeep) || !sameSet(del, tc.wantDelete) {
				t.Errorf("keep %v, delete %v\nwant keep %v, delete %v", keep, del, tc.wantKeep, tc.wantDelete)
			}
		})
	}
}

func TestPlanIndexGCMissingModTimeCountsAsYoung(t *testing.T) {
	objs, store := gc.NewMemoryObjects(), storage.NewMemoryStore()
	putIndex(objs, testPrpa, "cur", 30*day)
	point(t, store, testPrpa, "cur")
	objs.Put(buildrepo.SolvKey(testPrpa, "nots"), []byte("x"), time.Time{})
	objs.Put(buildrepo.EntriesKey(testPrpa, "nots"), []byte("x"), time.Time{})
	plans, err := gc.PlanIndexGC(context.Background(), store, objs, now, gc.Options{Grace: day})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans[0].Delete) != 0 {
		t.Errorf("deleted %v", plans[0].Delete)
	}
}

func TestOptionsValidate(t *testing.T) {
	tests := []struct {
		name    string
		opts    gc.Options
		wantErr bool
	}{
		{"default", gc.Options{Grace: gc.DefaultGrace, KeepVersions: 3}, false},
		{"minimum", gc.Options{Grace: gc.MinGrace}, false},
		{"zero grace", gc.Options{}, true},
		{"negative grace", gc.Options{Grace: -time.Hour}, true},
		{"below minimum", gc.Options{Grace: 59 * time.Minute}, true},
		{"negative versions", gc.Options{Grace: day, KeepVersions: -1}, true},
	}
	for _, tc := range tests {
		if err := tc.opts.Validate(); (err != nil) != tc.wantErr {
			t.Errorf("%s: got %v, wantErr %v", tc.name, err, tc.wantErr)
		}
	}
}

func sameSet(a, b []string) bool {
	m := map[string]int{}
	for _, k := range a {
		m[k]++
	}
	for _, k := range b {
		m[k]--
	}
	for _, n := range m {
		if n != 0 {
			return false
		}
	}
	return true
}

func TestPlanIndexGCKeepsForeignFiles(t *testing.T) {
	objs, store := gc.NewMemoryObjects(), storage.NewMemoryStore()
	putIndex(objs, testPrpa, "cur", 30*day)
	point(t, store, testPrpa, "cur")
	objs.Put("buildrepo/p/standard/x86_64/notes.txt", []byte("x"), ago(30*day))
	plans, err := gc.PlanIndexGC(context.Background(), store, objs, now, gc.Options{Grace: day})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans[0].Delete) != 0 {
		t.Errorf("deleted %v", plans[0].Delete)
	}
}

func TestPlanIndexGCProjectFilter(t *testing.T) {
	objs, store := gc.NewMemoryObjects(), storage.NewMemoryStore()
	other := buildrepo.Prpa{Project: "q", Repo: "standard", Arch: "x86_64"}
	for _, p := range []buildrepo.Prpa{testPrpa, other} {
		putIndex(objs, p, "cur", 30*day)
		putIndex(objs, p, "old", 30*day)
		point(t, store, p, "cur")
	}
	plans, err := gc.PlanIndexGC(context.Background(), store, objs, now, gc.Options{Grace: day, Project: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].Prpa != other.String() || len(plans[0].Delete) != 2 {
		t.Errorf("plans %+v", plans)
	}
}

func TestPlanIndexGCRefusesEmptyPointerTable(t *testing.T) {
	objs := gc.NewMemoryObjects()
	putIndex(objs, testPrpa, "cur", 30*day)
	if _, err := gc.PlanIndexGC(context.Background(), storage.NewMemoryStore(), objs, now, gc.Options{Grace: day}); err == nil {
		t.Fatal("empty pointer table must be refused")
	}
}

func TestApplyDeletesAndDryRunDoesNot(t *testing.T) {
	objs, store := gc.NewMemoryObjects(), storage.NewMemoryStore()
	putIndex(objs, testPrpa, "cur", 30*day)
	putIndex(objs, testPrpa, "old", 30*day)
	point(t, store, testPrpa, "cur")
	plans, err := gc.PlanIndexGC(context.Background(), store, objs, now, gc.Options{Grace: day})
	if err != nil {
		t.Fatal(err)
	}
	protected := func(ctx context.Context) (map[string]struct{}, error) {
		return map[string]struct{}{buildrepo.SolvKey(testPrpa, "cur"): {}}, nil
	}

	dry := gc.Apply(context.Background(), objs, plans, true, protected)
	if len(objs.Keys()) != 4 {
		t.Fatalf("dry run deleted objects: %v", objs.Keys())
	}
	if dry[0].Deleted != 2 || !reflect.DeepEqual(dry[0].Delete, plans[0].Delete) {
		t.Errorf("dry run reports %+v, want the plan %+v", dry[0], plans[0])
	}

	done := gc.Apply(context.Background(), objs, plans, false, protected)
	if done[0].Deleted != 2 || !reflect.DeepEqual(objs.Keys(), pair(testPrpa, "cur")) {
		t.Errorf("deleted %d, left %v", done[0].Deleted, objs.Keys())
	}
}

// A pointer that moves between planning and applying onto an object that was
// planned for deletion must not lose it.
func TestApplyHonoursPointerSwapSincePlanning(t *testing.T) {
	objs, store := gc.NewMemoryObjects(), storage.NewMemoryStore()
	putIndex(objs, testPrpa, "cur", 30*day)
	putIndex(objs, testPrpa, "old", 30*day)
	point(t, store, testPrpa, "cur")
	plans, err := gc.PlanIndexGC(context.Background(), store, objs, now, gc.Options{Grace: day})
	if err != nil {
		t.Fatal(err)
	}

	// The indexer rewrites "old" and moves the pointer to it.
	putIndex(objs, testPrpa, "old", 0)
	point(t, store, testPrpa, "old")

	protected := func(ctx context.Context) (map[string]struct{}, error) {
		list, err := store.ListBuildRepoIndexes(ctx)
		if err != nil {
			return nil, err
		}
		set := map[string]struct{}{}
		for _, p := range list {
			set[p.SolvKey], set[p.EntriesKey] = struct{}{}, struct{}{}
		}
		return set, nil
	}
	res := gc.Apply(context.Background(), objs, plans, false, protected)
	if !objs.Has(buildrepo.SolvKey(testPrpa, "old")) || !objs.Has(buildrepo.EntriesKey(testPrpa, "old")) {
		t.Fatalf("new current pair was deleted; left %v", objs.Keys())
	}
	if res[0].Deleted != 0 {
		t.Errorf("deleted %d, want 0", res[0].Deleted)
	}
}

func TestApplyBatchesOf1000(t *testing.T) {
	objs := gc.NewMemoryObjects()
	var del []string
	for i := range 2500 {
		k := "buildrepo/p/standard/x86_64/" + fmt.Sprintf("%d.solv", i)
		objs.Put(k, []byte("x"), ago(30*day))
		del = append(del, k)
	}
	none := func(context.Context) (map[string]struct{}, error) { return nil, nil }
	res := gc.Apply(context.Background(), objs, []gc.Plan{{Prefix: gc.PrefixBuildRepo, Prpa: "p/standard/x86_64", Delete: del}}, false, none)
	if res[0].Deleted != 2500 || !reflect.DeepEqual(objs.Batches, []int{1000, 1000, 500}) {
		t.Errorf("deleted %d in batches %v", res[0].Deleted, objs.Batches)
	}
}
