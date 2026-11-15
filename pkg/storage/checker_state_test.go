package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/openSUSE/bsgo/pkg/storage"
)

func TestNextBuildCounters(t *testing.T) {
	ctx := context.Background()
	key := func(repo, commit, meta string) storage.CounterKey {
		return storage.CounterKey{Project: "p", Repo: repo, Arch: "x86_64", Package: "pkg", SourceCommit: commit, Meta: meta}
	}
	tests := []struct {
		name string
		k    storage.CounterKey
		want storage.BuildCounters
	}{
		{"first build of first commit", key("std", "c1", "m1"), storage.BuildCounters{CI: 1, B: 1}},
		{"same key is idempotent", key("std", "c1", "m1"), storage.BuildCounters{CI: 1, B: 1}},
		{"rebuild of the same commit", key("std", "c1", "m2"), storage.BuildCounters{CI: 1, B: 2}},
		{"new commit", key("std", "c2", "m3"), storage.BuildCounters{CI: 2, B: 1}},
		{"other repo keeps CI and counts B itself", key("other", "c1", "m1"), storage.BuildCounters{CI: 1, B: 1}},
		{"other repo new commit shares CI numbering", key("other", "c3", "m9"), storage.BuildCounters{CI: 3, B: 1}},
	}
	s := storage.NewMemoryStore()
	for _, tc := range tests {
		got, err := s.NextBuildCounters(ctx, tc.k)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %+v, %v; want %+v", tc.name, got, err, tc.want)
		}
	}
	if _, err := s.NextBuildCounters(ctx, storage.CounterKey{}); err == nil {
		t.Error("an incomplete key must be rejected")
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
