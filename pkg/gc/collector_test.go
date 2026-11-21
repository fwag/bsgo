package gc_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/gc"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/scheduler/coord"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// failingDelete fails the batches that contain a key with the given part.
type failingDelete struct {
	*gc.MemoryObjects
	part string
}

func (f failingDelete) DeleteBatch(ctx context.Context, keys []string) error {
	for _, k := range keys {
		if strings.Contains(k, f.part) {
			return errors.New("boom")
		}
	}
	return f.MemoryObjects.DeleteBatch(ctx, keys)
}

type logs struct{ lines []string }

func (l *logs) logf(format string, args ...any) {
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func collectorEnv(t *testing.T) (*gc.Collector, *gc.MemoryObjects, *logs) {
	t.Helper()
	objs, store := gc.NewMemoryObjects(), storage.NewMemoryStore()
	for _, p := range []buildrepo.Prpa{testPrpa, {Project: "q", Repo: "standard", Arch: "x86_64"}} {
		putIndex(objs, p, "cur", 30*day)
		putIndex(objs, p, "old", 30*day)
		point(t, store, p, "cur")
	}
	l := &logs{}
	return &gc.Collector{
		Objects: objs, Store: store, Leases: &coord.Memory{}, Owner: "a",
		Now: func() time.Time { return now }, Logf: l.logf,
	}, objs, l
}

func TestCollectorRunDeletesAndLogsOneLinePerPrefix(t *testing.T) {
	c, objs, l := collectorEnv(t)
	res, err := c.Run(context.Background(), []string{gc.PrefixBuildRepo}, gc.Options{Grace: day})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Deleted != 4 || len(objs.Keys()) != 4 {
		t.Errorf("deleted %d, left %v", res[0].Deleted, objs.Keys())
	}
	want := "[GC] buildrepo: kept 4, deleted 4 objects, freed "
	if len(l.lines) != 1 || !strings.HasPrefix(l.lines[0], want) || !strings.HasSuffix(l.lines[0], "s") {
		t.Errorf("log %q", l.lines)
	}
}

func TestCollectorPlanIsDryRun(t *testing.T) {
	c, objs, _ := collectorEnv(t)
	res, err := c.Plan(context.Background(), []string{gc.PrefixBuildRepo}, gc.Options{Grace: day})
	if err != nil {
		t.Fatal(err)
	}
	if !res[0].DryRun || res[0].Deleted != 4 || len(objs.Keys()) != 8 {
		t.Errorf("result %+v, objects %d", res[0], len(objs.Keys()))
	}
}

func TestCollectorSingleRunAtATime(t *testing.T) {
	c, objs, _ := collectorEnv(t)
	leases := &coord.Memory{}
	c.Leases = leases
	if _, err := leases.TryLease(context.Background(), gc.LeaseKey, "other-replica"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Run(context.Background(), []string{gc.PrefixBuildRepo}, gc.Options{Grace: day}); !errors.Is(err, gc.ErrBusy) {
		t.Fatalf("got %v, want ErrBusy", err)
	}
	if len(objs.Keys()) != 8 {
		t.Error("a run without the lease deleted objects")
	}
}

func TestCollectorReleasesLease(t *testing.T) {
	c, _, _ := collectorEnv(t)
	leases := &coord.Memory{}
	c.Leases = leases
	if _, err := c.Run(context.Background(), []string{gc.PrefixBuildRepo}, gc.Options{Grace: day}); err != nil {
		t.Fatal(err)
	}
	if _, err := leases.TryLease(context.Background(), gc.LeaseKey, "other"); err != nil {
		t.Errorf("lease not released: %v", err)
	}
}

func TestCollectorContinuesAfterPrpaError(t *testing.T) {
	c, objs, l := collectorEnv(t)
	c.Objects = failingDelete{MemoryObjects: objs, part: "buildrepo/p/"}
	res, err := c.Run(context.Background(), []string{gc.PrefixBuildRepo}, gc.Options{Grace: day})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Errors != 1 || res[0].Deleted != 2 {
		t.Errorf("result %+v", res[0])
	}
	if !objs.Has(buildrepo.SolvKey(testPrpa, "old")) || objs.Has(buildrepo.SolvKey(buildrepo.Prpa{Project: "q", Repo: "standard", Arch: "x86_64"}, "old")) {
		t.Errorf("objects %v", objs.Keys())
	}
	found := false
	for _, line := range l.lines {
		found = found || strings.Contains(line, "p/standard/x86_64: deleting objects: boom")
	}
	if !found {
		t.Errorf("error not logged: %q", l.lines)
	}
}

func TestCollectorRunsArtifactsAndRejectsUnknownPrefix(t *testing.T) {
	e := newArtEnv(t, "patch")
	e.index(testPrpa, "h", unitDir+"patch-2-2.x86_64.rpm")
	e.put(unitDir+"patch-2-2.x86_64.rpm", 3*day)
	e.put(unitDir+"patch-1-1.x86_64.rpm", 9*day)
	e.bininfo(unitDir, 3*day, "patch-2-2.x86_64.rpm")
	c := &gc.Collector{Objects: e.objs, Store: e.store, Leases: &coord.Memory{}, Owner: "a", Now: func() time.Time { return now }}
	if _, err := c.Run(context.Background(), []string{gc.PrefixArtifacts}, gc.Options{Grace: day}); err != nil {
		t.Fatal(err)
	}
	if e.objs.Has(unitDir+"patch-1-1.x86_64.rpm") || !e.objs.Has(unitDir+"patch-2-2.x86_64.rpm") {
		t.Errorf("objects %v", e.objs.Keys())
	}
	if _, err := c.Run(context.Background(), []string{"repos"}, gc.Options{Grace: day}); err == nil {
		t.Error("repos/ must not be collectable")
	}
}

func TestFormatBytes(t *testing.T) {
	tests := map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 7 << 30: "7.0 GiB", 1<<30 + 1<<29: "1.5 GiB"}
	for n, want := range tests {
		if got := gc.FormatBytes(n); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestCollectorRejectsUnsafeOptions(t *testing.T) {
	c, objs, _ := collectorEnv(t)
	for _, o := range []gc.Options{{}, {Grace: -day}, {Grace: time.Minute}} {
		if _, err := c.Run(context.Background(), []string{gc.PrefixBuildRepo}, o); err == nil {
			t.Errorf("Run accepted %+v", o)
		}
		if _, err := c.Plan(context.Background(), []string{gc.PrefixBuildRepo}, o); err == nil {
			t.Errorf("Plan accepted %+v", o)
		}
	}
	if len(objs.Keys()) != 8 {
		t.Error("objects deleted")
	}
}

// Execute deletes the confirmed plan and nothing that became garbage later.
func TestCollectorExecuteAppliesOnlyThePlan(t *testing.T) {
	c, objs, _ := collectorEnv(t)
	opts := gc.Options{Grace: day}
	plan, err := c.Plan(context.Background(), []string{gc.PrefixBuildRepo}, opts)
	if err != nil {
		t.Fatal(err)
	}
	later := buildrepo.Prpa{Project: "q", Repo: "standard", Arch: "x86_64"}
	putIndex(objs, later, "extra", 30*day)
	res, err := c.Execute(context.Background(), plan, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Deleted != 4 || !objs.Has(buildrepo.SolvKey(later, "extra")) {
		t.Errorf("deleted %d, objects %v", res[0].Deleted, objs.Keys())
	}
}
