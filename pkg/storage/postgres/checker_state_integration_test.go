package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/openSUSE/bsgo/pkg/storage/postgres"
)

func newCheckerStore(t *testing.T) (*postgres.PostgresStore, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := postgres.NewPostgresStore(ctx, getTestDatabaseURL())
	if err != nil {
		t.Skipf("skipping postgres integration test: %v", err)
	}
	t.Cleanup(store.Close)
	return store, fmt.Sprintf("ckr:%d", time.Now().UnixNano())
}

func TestPostgresBuildStatesBatchAndBuiltMeta(t *testing.T) {
	store, prj := newCheckerStore(t)
	ctx := context.Background()
	row := func(pkg, flavor, state, meta string) *storage.PackageBuildState {
		return &storage.PackageBuildState{Project: prj, Repo: "std", Arch: "x86_64", Package: pkg, Flavor: flavor, State: state, Meta: meta}
	}
	batch := []*storage.PackageBuildState{
		row("a", "", storage.StateScheduled, "m1"),
		row("b", "mini", storage.StateBlocked, ""),
	}
	if err := store.UpsertBuildStates(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertBuildState(ctx, row("a", "", storage.StateSucceeded, "")); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertBuildState(ctx, row("a", "", storage.StateScheduled, "m2")); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetBuildState(ctx, prj, "std", "x86_64", "a", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != storage.StateScheduled || got.Meta != "m2" || got.BuiltMeta != "m1" {
		t.Errorf("row %+v: want scheduled meta m2 built m1", got)
	}
	bad := []*storage.PackageBuildState{row("c", "", storage.StateBlocked, ""), row("d", "", "bogus", "")}
	if err := store.UpsertBuildStates(ctx, bad); err == nil {
		t.Error("a batch with an invalid row must fail")
	}
	if _, err := store.GetBuildState(ctx, prj, "std", "x86_64", "c", ""); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("rows of a failed batch must not exist, got %v", err)
	}
	list, err := store.ListBuildStates(ctx, prj, storage.BuildStateFilter{})
	if err != nil || len(list) != 2 {
		t.Fatalf("list %v %v, want the 2 rows of the first batch", list, err)
	}
}

func TestPostgresBuildCounters(t *testing.T) {
	store, prj := newCheckerStore(t)
	ctx := context.Background()
	key := func(repo, commit, meta string, ci int, versrel string) storage.CounterKey {
		return storage.CounterKey{
			Project: prj, Repo: repo, Arch: "x86_64", Package: "pkg", SourceCommit: commit, Meta: meta,
			CI: ci, Versrel: versrel,
		}
	}
	steps := []struct {
		k    storage.CounterKey
		want storage.BuildCounters
	}{
		{key("std", "c1", "m1", 7, "1.0-7"), storage.BuildCounters{CI: 7, B: 1}},
		{key("std", "c1", "m1", 7, "1.0-7"), storage.BuildCounters{CI: 7, B: 1}},
		{key("std", "c1", "m1", 9, "1.0-9"), storage.BuildCounters{CI: 7, B: 1}},
		{key("std", "c1", "m2", 7, "1.0-7"), storage.BuildCounters{CI: 7, B: 2}},
		{key("std", "c2", "m3", 8, "1.0-8"), storage.BuildCounters{CI: 8, B: 1}},
		{key("other", "c1", "m1", 7, "1.0-7"), storage.BuildCounters{CI: 7, B: 1}},
	}
	for i, s := range steps {
		got, err := store.NextBuildCounters(ctx, s.k)
		if err != nil || got != s.want {
			t.Errorf("step %d: got %+v, %v; want %+v", i, got, err, s.want)
		}
	}
}

func TestPostgresBuildCountersLegacyFloor(t *testing.T) {
	store, prj := newCheckerStore(t)
	ctx := context.Background()
	for _, row := range []struct {
		repo string
		b    int
	}{{"std", 130}, {"other", 9}} {
		_, err := store.Pool().Exec(ctx, `
			INSERT INTO build_counters (project, repo, arch, package, flavor, source_commit, meta, ci_cnt, bcnt)
			VALUES ($1, $2, 'x86_64', 'pkg', '', 'old', 'm', 1, $3)`, prj, row.repo, row.b)
		if err != nil {
			t.Fatal(err)
		}
	}
	key := func(repo string, ci int, meta string) storage.CounterKey {
		return storage.CounterKey{
			Project: prj, Repo: repo, Arch: "x86_64", Package: "pkg", SourceCommit: "c", Meta: meta,
			CI: ci, Versrel: fmt.Sprintf("1.0-%d", ci),
		}
	}
	_, err := store.Pool().Exec(ctx, `
		INSERT INTO build_counters (project, repo, arch, package, flavor, source_commit, meta, ci_cnt, bcnt)
		VALUES ($1, 'std', 'x86_64', 'pkg', '', 'c', 'legacy-meta', 1, 3)`, prj)
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		k    storage.CounterKey
		want storage.BuildCounters
	}{
		{key("std", 25, "legacy-meta"), storage.BuildCounters{CI: 25, B: 1}},
		{key("std", 25, "legacy-meta"), storage.BuildCounters{CI: 25, B: 1}},
		{key("std", 1, "m1"), storage.BuildCounters{CI: 1, B: 131}},
		{key("other", 1, "m1"), storage.BuildCounters{CI: 1, B: 10}},
		{key("std", 1, "m2"), storage.BuildCounters{CI: 1, B: 132}},
		{key("std", 2, "m3"), storage.BuildCounters{CI: 2, B: 1}},
	}
	for i, s := range steps {
		got, err := store.NextBuildCounters(ctx, s.k)
		if err != nil || got != s.want {
			t.Errorf("step %d: got %+v, %v; want %+v", i, got, err, s.want)
		}
	}
}

func TestPostgresForcedRebuildsAndIndexPointer(t *testing.T) {
	store, prj := newCheckerStore(t)
	ctx := context.Background()
	for _, r := range []storage.ForcedRebuild{
		{Project: prj, Package: "a"},
		{Project: prj, Package: "a"},
		{Project: prj, Repo: "std", Arch: "s390x", Package: "b", Flavor: "mini"},
	} {
		if err := store.RequestForcedRebuild(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	list, err := store.ListForcedRebuilds(ctx, prj)
	if err != nil || len(list) != 2 {
		t.Fatalf("list %v %v, want 2 distinct requests", list, err)
	}
	if err := store.ClearForcedRebuilds(ctx, prj, "std", "x86_64", []storage.UnitRef{{Package: "a"}, {Package: "b", Flavor: "mini"}}); err != nil {
		t.Fatal(err)
	}
	list, _ = store.ListForcedRebuilds(ctx, prj)
	if len(list) != 1 || list[0].Package != "b" {
		t.Errorf("remaining %v, want only the s390x request", list)
	}

	if _, err := store.GetBuildRepoIndex(ctx, prj, "std", "x86_64"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
	for _, hash := range []string{"h1", "h2"} {
		err := store.SaveBuildRepoIndex(ctx, &storage.BuildRepoIndex{
			Project: prj, Repo: "std", Arch: "x86_64", ContentHash: hash, SolvKey: "s/" + hash, EntriesKey: "e/" + hash,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	idx, err := store.GetBuildRepoIndex(ctx, prj, "std", "x86_64")
	if err != nil || idx.ContentHash != "h2" || idx.SolvKey != "s/h2" {
		t.Errorf("pointer %+v %v, want the latest", idx, err)
	}
}

func TestPostgresBuildCountersNeverGoBelowALegacyCI(t *testing.T) {
	store, prj := newCheckerStore(t)
	ctx := context.Background()
	for _, row := range []struct {
		commit string
		ci, b  int
	}{{"old1", 1, 130}, {"old2", 3, 4}} {
		_, err := store.Pool().Exec(ctx, `
			INSERT INTO build_counters (project, repo, arch, package, flavor, source_commit, meta, ci_cnt, bcnt)
			VALUES ($1, 'std', 'x86_64', 'pkg', '', $2, 'm', $3, $4)`, prj, row.commit, row.ci, row.b)
		if err != nil {
			t.Fatal(err)
		}
	}
	key := func(ci int, meta string) storage.CounterKey {
		return storage.CounterKey{
			Project: prj, Repo: "std", Arch: "x86_64", Package: "pkg", SourceCommit: "c", Meta: meta,
			CI: ci, Versrel: fmt.Sprintf("1.0-%d", ci),
		}
	}
	steps := []struct {
		k    storage.CounterKey
		want storage.BuildCounters
	}{
		{key(1, "m1"), storage.BuildCounters{CI: 3, B: 5}},
		{key(2, "m2"), storage.BuildCounters{CI: 3, B: 6}},
		{key(3, "m3"), storage.BuildCounters{CI: 3, B: 7}},
		{key(4, "m4"), storage.BuildCounters{CI: 4, B: 1}},
	}
	for i, s := range steps {
		got, err := store.NextBuildCounters(ctx, s.k)
		if err != nil || got != s.want {
			t.Errorf("step %d: got %+v, %v; want %+v", i, got, err, s.want)
		}
	}
}
