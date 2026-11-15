package buildrepo_test

import (
	"context"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func TestPrpaAndUnitNames(t *testing.T) {
	p, err := buildrepo.ParsePrpa("SUSE:SLFO:1.2/standard/x86_64")
	if err != nil || p.Project != "SUSE:SLFO:1.2" || p.String() != "SUSE:SLFO:1.2/standard/x86_64" {
		t.Fatalf("got %+v, %v", p, err)
	}
	for _, bad := range []string{"", "a/b", "a/b/c/d", "a//c"} {
		if _, err := buildrepo.ParsePrpa(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
	pkg, flavor := buildrepo.SplitUnit(buildrepo.UnitName("gcc13", "cross-ppc"))
	if pkg != "gcc13" || flavor != "cross-ppc" {
		t.Errorf("round trip gave %q %q", pkg, flavor)
	}
	if buildrepo.UnitName("x", "") != "x" {
		t.Error("a unit without flavor is the package name")
	}
}

func TestReaderLoadsAndVerifies(t *testing.T) {
	ctx := context.Background()
	p := buildrepo.Prpa{Project: "p", Repo: "r", Arch: "x86_64"}
	store := storage.NewMemoryStore()
	blobs := buildrepo.NewMemoryBlobs()
	r := buildrepo.NewReader(store, blobs)

	idx, err := r.Index(ctx, p)
	if err != nil || idx.Hash != "" || len(idx.Solv) != 0 {
		t.Fatalf("an unknown prpa must give an empty index, got %+v, %v", idx, err)
	}

	entries, err := buildrepo.EncodeEntries([]buildrepo.Entry{{Unit: "a", Key: "k"}})
	if err != nil {
		t.Fatal(err)
	}
	solv := []byte("SOLVdata")
	hash := buildrepo.ContentHash(solv, entries)
	save := func(h string) {
		t.Helper()
		err := store.SaveBuildRepoIndex(ctx, &storage.BuildRepoIndex{
			Project: p.Project, Repo: p.Repo, Arch: p.Arch, ContentHash: h,
			SolvKey: buildrepo.SolvKey(p, hash), EntriesKey: buildrepo.EntriesKey(p, hash),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	save(hash)
	if _, err := r.Index(ctx, p); err == nil {
		t.Fatal("a pointer to missing objects must fail")
	}
	_ = blobs.PutObject(ctx, buildrepo.SolvKey(p, hash), solv)
	_ = blobs.PutObject(ctx, buildrepo.EntriesKey(p, hash), entries)
	idx, err = r.Index(ctx, p)
	if err != nil || idx.Hash != hash || len(idx.Entries) != 1 || idx.Entries[0].Unit != "a" {
		t.Fatalf("got %+v, %v", idx, err)
	}

	save("0000")
	r2 := buildrepo.NewReader(store, blobs)
	if _, err := r2.Index(ctx, p); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Errorf("tampered objects must be detected, got %v", err)
	}
}
