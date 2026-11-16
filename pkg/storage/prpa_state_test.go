package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/openSUSE/bsgo/pkg/storage"
)

func TestDeriveRepoState(t *testing.T) {
	cur := func(state, hash string) *storage.PrpaState {
		return &storage.PrpaState{State: state, IndexHash: hash}
	}
	tests := []struct {
		name        string
		in          storage.RepoStateInput
		wantState   string
		wantDetails string
		wantWrite   bool
	}{
		{"config error is broken even with jobs", storage.RepoStateInput{
			ConfigError: "bad prjconf", Counts: map[string]int{storage.StateBuilding: 2}},
			storage.RepoBroken, "bad prjconf", true},
		{"scheduled job is building", storage.RepoStateInput{
			Counts: map[string]int{storage.StateScheduled: 1}}, storage.RepoBuilding, "1 jobs in flight", true},
		{"dispatching counts as in flight", storage.RepoStateInput{
			Counts: map[string]int{storage.StateDispatching: 1, storage.StateSigning: 1}},
			storage.RepoBuilding, "2 jobs in flight", true},
		{"building beats blocked", storage.RepoStateInput{
			Counts: map[string]int{storage.StateBuilding: 1, storage.StateBlocked: 3}},
			storage.RepoBuilding, "1 jobs in flight", true},
		{"blocked without jobs", storage.RepoStateInput{
			Counts: map[string]int{storage.StateBlocked: 3, storage.StateSucceeded: 5}},
			storage.RepoBlocked, "3 units blocked", true},
		{"idle without publish is unpublished", storage.RepoStateInput{
			Counts: map[string]int{storage.StateSucceeded: 5}}, storage.RepoUnpublished, "", true},
		{"idle with publish is finished", storage.RepoStateInput{
			Counts: map[string]int{storage.StateFailed: 1}, PublishEnabled: true}, storage.RepoFinished, "", true},
		{"unchanged state is not rewritten", storage.RepoStateInput{
			Counts: map[string]int{storage.StateScheduled: 1}, Current: &storage.PrpaState{
				State: storage.RepoBuilding, Details: "1 jobs in flight"}},
			storage.RepoBuilding, "1 jobs in flight", false},
		{"published of the current index is kept", storage.RepoStateInput{
			PublishEnabled: true, IndexHash: "h1", Current: cur(storage.RepoPublished, "h1")},
			storage.RepoPublished, "", false},
		{"published of an older index becomes finished", storage.RepoStateInput{
			PublishEnabled: true, IndexHash: "h2", Current: cur(storage.RepoPublished, "h1")},
			storage.RepoFinished, "", true},
		{"publishing is kept", storage.RepoStateInput{
			PublishEnabled: true, IndexHash: "h2", Current: cur(storage.RepoPublishing, "h1")},
			storage.RepoPublishing, "", false},
		{"new work overrides published", storage.RepoStateInput{
			Counts: map[string]int{storage.StateScheduled: 1}, IndexHash: "h1",
			Current: cur(storage.RepoPublished, "h1")}, storage.RepoBuilding, "1 jobs in flight", true},
		{"publish disabled overrides published", storage.RepoStateInput{
			IndexHash: "h1", Current: cur(storage.RepoPublished, "h1")}, storage.RepoUnpublished, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			state, details, write := storage.DeriveRepoState(tc.in)
			if state != tc.wantState || details != tc.wantDetails || write != tc.wantWrite {
				t.Errorf("got (%q, %q, %v), want (%q, %q, %v)", state, details, write,
					tc.wantState, tc.wantDetails, tc.wantWrite)
			}
		})
	}
}

func TestMemoryPrpaState(t *testing.T) {
	ctx := context.Background()
	s := storage.NewMemoryStore()
	if _, err := s.GetPrpaState(ctx, "p", "std", "x86_64"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing row: got %v, want ErrNotFound", err)
	}
	rows := []*storage.PrpaState{
		{Project: "p", Repo: "std", Arch: "x86_64", State: storage.RepoBuilding, Details: "1 jobs in flight"},
		{Project: "p", Repo: "std", Arch: "aarch64", State: storage.RepoBlocked},
		{Project: "q", Repo: "std", Arch: "x86_64", State: storage.RepoFinished},
		{Project: "p", Repo: "std", Arch: "x86_64", State: storage.RepoPublished, IndexHash: "h"},
	}
	for _, r := range rows {
		if err := s.SetPrpaState(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListPrpaStates(ctx, "p")
	if err != nil || len(list) != 2 || list[0].Arch != "aarch64" || list[1].State != storage.RepoPublished || list[1].IndexHash != "h" {
		t.Errorf("list: %+v, %v", list, err)
	}
	bad := []*storage.PrpaState{{State: storage.RepoBuilding}, {Project: "p", Repo: "r", Arch: "a", State: "bogus"}}
	for _, b := range bad {
		if err := s.SetPrpaState(ctx, b); err == nil {
			t.Errorf("row %+v must be rejected", b)
		}
	}
}
