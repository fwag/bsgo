package storage_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/openSUSE/bsgo/pkg/storage"
)

func TestNextBuildCounters(t *testing.T) {
	ctx := context.Background()
	key := func(repo, commit, meta string, ci int, versrel string) storage.CounterKey {
		return storage.CounterKey{
			Project: "p", Repo: repo, Arch: "x86_64", Package: "pkg", SourceCommit: commit, Meta: meta,
			CI: ci, Versrel: versrel,
		}
	}
	tests := []struct {
		name string
		k    storage.CounterKey
		want storage.BuildCounters
	}{
		{"first build", key("std", "c1", "m1", 7, "1.0-7"), storage.BuildCounters{CI: 7, B: 1}},
		{"same key is idempotent", key("std", "c1", "m1", 7, "1.0-7"), storage.BuildCounters{CI: 7, B: 1}},
		{"idempotent even when the revision was re-resolved", key("std", "c1", "m1", 9, "1.0-9"), storage.BuildCounters{CI: 7, B: 1}},
		{"rebuild of the same versrel", key("std", "c1", "m2", 7, "1.0-7"), storage.BuildCounters{CI: 7, B: 2}},
		{"same versrel from another commit continues", key("std", "c1b", "m3", 7, "1.0-7"), storage.BuildCounters{CI: 7, B: 3}},
		{"new versrel restarts B", key("std", "c2", "m4", 8, "1.0-8"), storage.BuildCounters{CI: 8, B: 1}},
		{"other repo counts B itself", key("other", "c1", "m1", 7, "1.0-7"), storage.BuildCounters{CI: 7, B: 1}},
		{"other version of the same vrev is its own versrel", key("std", "c3", "m5", 7, "2.0-7"), storage.BuildCounters{CI: 7, B: 1}},
	}
	s := storage.NewMemoryStore()
	for _, tc := range tests {
		got, err := s.NextBuildCounters(ctx, tc.k)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %+v, %v; want %+v", tc.name, got, err, tc.want)
		}
	}
	for _, bad := range []storage.CounterKey{{}, key("std", "c", "m", 0, "1-0"), key("std", "c", "m", 1, "")} {
		if _, err := s.NextBuildCounters(ctx, bad); err == nil {
			t.Errorf("incomplete key %+v must be rejected", bad)
		}
	}
}

func TestNextBuildCountersLegacyFloor(t *testing.T) {
	ctx := context.Background()
	s := storage.NewMemoryStore()
	old := storage.CounterKey{Project: "p", Repo: "std", Arch: "x86_64", Package: "pkg", SourceCommit: "old", Meta: "m"}
	s.AddLegacyCounters(old, storage.BuildCounters{CI: 1, B: 130})
	oldOther := old
	oldOther.Repo = "other"
	s.AddLegacyCounters(oldOther, storage.BuildCounters{CI: 1, B: 9})
	key := func(repo string, ci int, meta string) storage.CounterKey {
		return storage.CounterKey{
			Project: "p", Repo: repo, Arch: "x86_64", Package: "pkg", SourceCommit: "c", Meta: meta,
			CI: ci, Versrel: fmt.Sprintf("1.0-%d", ci),
		}
	}
	tests := []struct {
		name string
		k    storage.CounterKey
		want storage.BuildCounters
	}{
		{"CI equal to the legacy one continues above its B", key("std", 1, "m1"), storage.BuildCounters{CI: 1, B: 131}},
		{"the floor is per repository", key("other", 1, "m1"), storage.BuildCounters{CI: 1, B: 10}},
		{"next attempt continues the versrel", key("std", 1, "m2"), storage.BuildCounters{CI: 1, B: 132}},
		{"higher CI starts at 1", key("std", 2, "m3"), storage.BuildCounters{CI: 2, B: 1}},
	}
	for _, tc := range tests {
		got, err := s.NextBuildCounters(ctx, tc.k)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %+v, %v; want %+v", tc.name, got, err, tc.want)
		}
	}
}

func TestNextBuildCountersNeverGoesBelowALegacyCI(t *testing.T) {
	ctx := context.Background()
	s := storage.NewMemoryStore()
	legacy := func(commit string, ci, b int) {
		s.AddLegacyCounters(storage.CounterKey{
			Project: "p", Repo: "std", Arch: "x86_64", Package: "pkg", SourceCommit: commit, Meta: "m",
		}, storage.BuildCounters{CI: ci, B: b})
	}
	legacy("old1", 1, 130)
	legacy("old2", 3, 4)
	key := func(ci int, meta string) storage.CounterKey {
		return storage.CounterKey{
			Project: "p", Repo: "std", Arch: "x86_64", Package: "pkg", SourceCommit: "c", Meta: meta,
			CI: ci, Versrel: fmt.Sprintf("1.0-%d", ci),
		}
	}
	tests := []struct {
		name string
		k    storage.CounterKey
		want storage.BuildCounters
	}{
		{"a lower CI is raised to the legacy one, above its builds", key(1, "m1"), storage.BuildCounters{CI: 3, B: 5}},
		{"the raised release continues", key(2, "m2"), storage.BuildCounters{CI: 3, B: 6}},
		{"an equal CI behaves the same", key(3, "m3"), storage.BuildCounters{CI: 3, B: 7}},
		{"a higher CI is kept", key(4, "m4"), storage.BuildCounters{CI: 4, B: 1}},
	}
	for _, tc := range tests {
		got, err := s.NextBuildCounters(ctx, tc.k)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %+v, %v; want %+v", tc.name, got, err, tc.want)
		}
	}
}

func TestNextBuildCountersRenumbersALegacyRowOfTheSameKey(t *testing.T) {
	ctx := context.Background()
	s := storage.NewMemoryStore()
	k := storage.CounterKey{
		Project: "p", Repo: "std", Arch: "x86_64", Package: "pkg", SourceCommit: "c", Meta: "m",
		CI: 25, Versrel: "1.0-25",
	}
	s.AddLegacyCounters(k, storage.BuildCounters{CI: 1, B: 3})
	for i := range 2 {
		got, err := s.NextBuildCounters(ctx, k)
		if err != nil || got != (storage.BuildCounters{CI: 25, B: 1}) {
			t.Errorf("call %d: got %+v, %v; want the legacy job renumbered to 25.1 and kept", i, got, err)
		}
	}
}

func TestBuiltMetaFollowsSucceeded(t *testing.T) {
	ctx := context.Background()
	s := storage.NewMemoryStore()
	row := func(state, meta string) *storage.PackageBuildState {
		return &storage.PackageBuildState{Project: "p", Repo: "r", Arch: "a", Package: "x", State: state, Meta: meta}
	}
	steps := []struct {
		st        *storage.PackageBuildState
		wantMeta  string
		wantBuilt string
	}{
		{row(storage.StateScheduled, "m1"), "m1", ""},
		{row(storage.StateSucceeded, ""), "m1", "m1"},
		{row(storage.StateScheduled, "m2"), "m2", "m1"},
		{row(storage.StateFailed, ""), "m2", "m1"},
	}
	for i, s1 := range steps {
		if err := s.UpsertBuildStates(ctx, []*storage.PackageBuildState{s1.st}); err != nil {
			t.Fatal(err)
		}
		list, _ := s.ListBuildStates(ctx, "p", storage.BuildStateFilter{})
		if list[0].Meta != s1.wantMeta || list[0].BuiltMeta != s1.wantBuilt {
			t.Errorf("step %d: meta %q built %q, want %q %q", i, list[0].Meta, list[0].BuiltMeta, s1.wantMeta, s1.wantBuilt)
		}
	}
	bad := []*storage.PackageBuildState{row(storage.StateBlocked, ""), row("nonsense", "")}
	if err := s.UpsertBuildStates(ctx, bad); err == nil {
		t.Error("a batch with an invalid row must be rejected as a whole")
	}
	if list, _ := s.ListBuildStates(ctx, "p", storage.BuildStateFilter{}); list[0].State != storage.StateFailed {
		t.Errorf("rejected batch changed the stored state to %s", list[0].State)
	}
}

func TestForcedRebuilds(t *testing.T) {
	ctx := context.Background()
	s := storage.NewMemoryStore()
	reqs := []storage.ForcedRebuild{
		{Project: "p", Package: "a"},
		{Project: "p", Repo: "r", Arch: "x", Package: "b", Flavor: "mini"},
		{Project: "other", Package: "a"},
	}
	for _, r := range reqs {
		if err := s.RequestForcedRebuild(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RequestForcedRebuild(ctx, reqs[0]); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListForcedRebuilds(ctx, "p")
	if len(list) != 2 {
		t.Fatalf("got %d requests, want 2", len(list))
	}
	if err := s.ClearForcedRebuilds(ctx, "p", "r", "y", []storage.UnitRef{{Package: "a"}, {Package: "b", Flavor: "mini"}}); err != nil {
		t.Fatal(err)
	}
	list, _ = s.ListForcedRebuilds(ctx, "p")
	if len(list) != 1 || list[0].Package != "b" {
		t.Errorf("remaining %v: only the request for another arch should be left", list)
	}
}

func TestBuildRepoIndexPointer(t *testing.T) {
	ctx := context.Background()
	s := storage.NewMemoryStore()
	if _, err := s.GetBuildRepoIndex(ctx, "p", "r", "a"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
	idx := &storage.BuildRepoIndex{Project: "p", Repo: "r", Arch: "a", ContentHash: "h", SolvKey: "s", EntriesKey: "e"}
	if err := s.SaveBuildRepoIndex(ctx, idx); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetBuildRepoIndex(ctx, "p", "r", "a")
	if err != nil || got.ContentHash != "h" {
		t.Errorf("got %+v, %v", got, err)
	}
	if err := s.SaveBuildRepoIndex(ctx, &storage.BuildRepoIndex{Project: "p"}); err == nil {
		t.Error("an incomplete pointer must be rejected")
	}
}
