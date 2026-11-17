package postgres_test

import (
	"context"
	"slices"
	"testing"

	"github.com/openSUSE/bsgo/pkg/storage"
)

func TestPostgresBuildMetas(t *testing.T) {
	store, prj := newCheckerStore(t)
	ctx := context.Background()
	metas := []storage.BuildMeta{
		{Hash: "a", Lines: []string{"s1  pkg", "i1  dep"}},
		{Hash: "b", Lines: []string{"s2  pkg"}},
	}
	// Saving twice is a no-op.
	for range 2 {
		if err := store.SaveBuildMetas(ctx, prj, "std", "x86_64", metas); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.GetBuildMetas(ctx, prj, "std", "x86_64", []string{"a", "b", "unknown"})
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v, %v: want two metas", got, err)
	}
	for _, m := range got {
		if m.Hash == "a" && !slices.Equal(m.Lines, metas[0].Lines) {
			t.Errorf("meta a came back as %q", m.Lines)
		}
	}
	if other, err := store.GetBuildMetas(ctx, prj, "std", "i586", []string{"a"}); err != nil || len(other) != 0 {
		t.Errorf("another arch sees %+v, %v", other, err)
	}
	if err := store.PruneBuildMetas(ctx, prj, "std", "x86_64", []string{"b"}); err != nil {
		t.Fatal(err)
	}
	left, err := store.GetBuildMetas(ctx, prj, "std", "x86_64", []string{"a", "b"})
	if err != nil || len(left) != 1 || left[0].Hash != "b" {
		t.Errorf("after prune: %+v, %v", left, err)
	}
}
