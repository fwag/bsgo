package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/openSUSE/bsgo/pkg/storage"
)

func TestPostgresPrpaState(t *testing.T) {
	store, prj := newCheckerStore(t)
	ctx := context.Background()
	if _, err := store.GetPrpaState(ctx, prj, "std", "x86_64"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing row: got %v, want ErrNotFound", err)
	}
	rows := []*storage.PrpaState{
		{Project: prj, Repo: "std", Arch: "x86_64", State: storage.RepoBuilding, Details: "1 jobs in flight"},
		{Project: prj, Repo: "std", Arch: "aarch64", State: storage.RepoBlocked},
		{Project: prj, Repo: "std", Arch: "x86_64", State: storage.RepoPublished, IndexHash: "h"},
	}
	for _, r := range rows {
		if err := store.SetPrpaState(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.GetPrpaState(ctx, prj, "std", "x86_64")
	if err != nil || got.State != storage.RepoPublished || got.IndexHash != "h" || got.Details != "" {
		t.Errorf("row %+v, %v: want published with hash h", got, err)
	}
	list, err := store.ListPrpaStates(ctx, prj)
	if err != nil || len(list) != 2 || list[0].Arch != "aarch64" {
		t.Errorf("list %+v, %v", list, err)
	}
	if err := store.SetPrpaState(ctx, &storage.PrpaState{Project: prj, Repo: "std", Arch: "x", State: "bogus"}); err == nil {
		t.Error("an unknown state must be rejected")
	}
}
