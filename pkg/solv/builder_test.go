package solv_test

import (
	"testing"

	"github.com/openSUSE/bsgo/pkg/solv"
)

func TestParseRPMFilename(t *testing.T) {
	spec := solv.ParseRPMFilename("libreadline8-8.3.6-3.1.x86_64.rpm")
	if spec.Name != "libreadline8" {
		t.Errorf("expected name libreadline8, got %s", spec.Name)
	}
	if spec.EVR != "8.3.6-3.1" {
		t.Errorf("expected EVR 8.3.6-3.1, got %s", spec.EVR)
	}
	if spec.Arch != "x86_64" {
		t.Errorf("expected arch x86_64, got %s", spec.Arch)
	}
}

func TestMergeAndDumpSolv(t *testing.T) {
	// 1. Initial dump with glibc
	pkg1 := solv.PackageSpec{
		Name:     "glibc",
		EVR:      "2.38-1",
		Arch:     "x86_64",
		Provides: []string{"libc.so.6()(64bit)"},
	}
	blob1, err := solv.MergeAndDumpSolv(nil, "x86_64", "testrepo", []solv.PackageSpec{pkg1})
	if err != nil {
		t.Fatalf("failed initial MergeAndDumpSolv: %v", err)
	}
	if len(blob1) == 0 {
		t.Fatalf("expected non-empty initial solv blob")
	}

	// 2. Incremental merge with gcc into existing blob
	pkg2 := solv.PackageSpec{
		Name:     "gcc",
		EVR:      "13.2-1",
		Arch:     "x86_64",
		Provides: []string{"c_compiler"},
		Requires: []string{"libc.so.6()(64bit)"},
	}
	blob2, err := solv.MergeAndDumpSolv(blob1, "x86_64", "testrepo", []solv.PackageSpec{pkg2})
	if err != nil {
		t.Fatalf("failed incremental MergeAndDumpSolv: %v", err)
	}

	// 3. Load merged blob into pool and resolve both dependencies
	pool, err := solv.NewPool("x86_64")
	if err != nil {
		t.Fatalf("failed creating pool: %v", err)
	}
	defer pool.Close()

	if _, err := pool.LoadRepoFromMemory("testrepo", blob2); err != nil {
		t.Fatalf("failed loading merged solv: %v", err)
	}

	res, err := pool.Resolve(solv.ExpansionRequest{
		BuildRequires: []string{"c_compiler"},
	})
	if err != nil {
		t.Fatalf("failed resolving from merged solv: %v", err)
	}

	if len(res.Packages) != 2 {
		t.Fatalf("expected 2 packages resolved from merged blob, got %d: %+v", len(res.Packages), res.Packages)
	}
	if res.Packages[0].Name != "glibc" || res.Packages[1].Name != "gcc" {
		t.Errorf("unexpected package resolution order: %+v", res.Packages)
	}
}
