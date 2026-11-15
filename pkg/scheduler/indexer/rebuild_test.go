package indexer_test

import (
	"context"
	"slices"
	"testing"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/scheduler/indexer"
	"github.com/openSUSE/bsgo/pkg/solv"
)

func solvSemantics(t *testing.T, blob []byte) (provs, reqs []string) {
	t.Helper()
	pool, err := solv.NewPool("x86_64")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.LoadRepoFromMemory("idx", blob); err != nil {
		t.Fatal(err)
	}
	return pool.AllProvides(), pool.AllRequires()
}

func TestRebuildMatchesIncrementalIndex(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	app := rpm("app", "1")
	app.Requires = []bininfo.Dep{{Name: "lib-feature"}}
	for _, step := range []struct {
		unit string
		bins []bininfo.BinInfo
	}{
		{"lib", []bininfo.BinInfo{rpm("lib", "1")}},
		{"app", []bininfo.BinInfo{app}},
		{"lib:flavor", []bininfo.BinInfo{rpm("libflavor", "1")}},
		{"lib", []bininfo.BinInfo{rpm("lib", "2"), rpm("lib-devel", "2")}},
	} {
		if _, err := e.integrate(step.unit, e.upload(step.unit, step.bins...)); err != nil {
			t.Fatal(err)
		}
	}
	inc, err := e.ix.Reader.Index(ctx, e.prpa)
	if err != nil {
		t.Fatal(err)
	}
	units := indexer.UnitsFromKeys(e.prpa, e.blobs.Keys())
	if want := []string{"app", "lib", "lib:flavor"}; !slices.Equal(units, want) {
		t.Fatalf("units %v, want %v", units, want)
	}

	before := len(e.blobs.Keys())
	dry, err := e.ix.Rebuild(ctx, e.prpa, units, true)
	if err != nil || dry.Changed || !dry.DryRun || dry.Binaries != len(inc.Entries) || len(e.blobs.Keys()) != before {
		t.Fatalf("dry run: %+v, %v (objects %d -> %d)", dry, err, before, len(e.blobs.Keys()))
	}
	res, err := e.ix.Rebuild(ctx, e.prpa, units, false)
	if err != nil {
		t.Fatal(err)
	}
	full, err := e.ix.Reader.Index(ctx, e.prpa)
	if err != nil || full.Hash != res.NewHash || res.OldHash != inc.Hash {
		t.Fatalf("pointer not moved to the rebuilt index: %+v, %v", res, err)
	}
	ip, ir := solvSemantics(t, inc.Solv)
	fp, fr := solvSemantics(t, full.Solv)
	if !slices.Equal(ip, fp) || !slices.Equal(ir, fr) {
		t.Errorf("rebuilt index differs from the incremental one:\n%v\n%v\n%v\n%v", ip, fp, ir, fr)
	}
	ie, err := buildrepo.EncodeEntries(inc.Entries)
	if err != nil {
		t.Fatal(err)
	}
	fe, err := buildrepo.EncodeEntries(full.Entries)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ie, fe) {
		t.Errorf("entries differ: %v vs %v", inc.Entries, full.Entries)
	}
	if _, err := e.ix.Rebuild(ctx, e.prpa, []string{"missing"}, true); err == nil {
		t.Error("a unit without bininfo.json must fail the rebuild")
	}
}
