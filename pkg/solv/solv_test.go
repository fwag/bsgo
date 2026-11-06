package solv_test

import (
	"errors"
	"testing"

	"github.com/openSUSE/bsgo/pkg/solv"
)

func TestSimpleResolution(t *testing.T) {
	pool, err := solv.NewPool("x86_64")
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	repo := pool.CreateRepo("base")
	err = repo.AddPackage(solv.PackageSpec{
		Name:     "glibc",
		EVR:      "2.38-1",
		Arch:     "x86_64",
		Provides: []string{"libc.so.6()(64bit)"},
	})
	if err != nil {
		t.Fatalf("failed to add glibc: %v", err)
	}

	err = repo.AddPackage(solv.PackageSpec{
		Name:     "gcc",
		EVR:      "13.2-1",
		Arch:     "x86_64",
		Provides: []string{"c_compiler"},
		Requires: []string{"libc.so.6()(64bit)"},
	})
	if err != nil {
		t.Fatalf("failed to add gcc: %v", err)
	}

	res, err := pool.Resolve(solv.ExpansionRequest{
		BuildRequires: []string{"c_compiler"},
	})
	if err != nil {
		t.Fatalf("unexpected resolution error: %v", err)
	}

	if len(res.Packages) != 2 {
		t.Fatalf("expected 2 packages in resolution, got %d", len(res.Packages))
	}

	// Order: glibc should be installed before gcc
	if res.Packages[0].Name != "glibc" {
		t.Errorf("expected glibc installed first, got %s", res.Packages[0].Name)
	}
	if res.Packages[1].Name != "gcc" {
		t.Errorf("expected gcc installed second, got %s", res.Packages[1].Name)
	}
}

func TestMissingProvider(t *testing.T) {
	pool, err := solv.NewPool("x86_64")
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	_, err = pool.Resolve(solv.ExpansionRequest{
		BuildRequires: []string{"nonexistent-library"},
	})
	if err == nil {
		t.Fatalf("expected error for nonexistent dependency")
	}

	var noProvErr *solv.NoProviderError
	if !errors.As(err, &noProvErr) {
		t.Errorf("expected NoProviderError, got %T: %v", err, err)
	} else if noProvErr.Requirement != "nonexistent-library" {
		t.Errorf("expected missing requirement 'nonexistent-library', got %q", noProvErr.Requirement)
	}
}

func TestAmbiguityAndPreference(t *testing.T) {
	pool, err := solv.NewPool("x86_64")
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	repo := pool.CreateRepo("base")
	_ = repo.AddPackage(solv.PackageSpec{
		Name:     "sendmail",
		EVR:      "8.17-1",
		Arch:     "x86_64",
		Provides: []string{"smtp_daemon"},
	})
	_ = repo.AddPackage(solv.PackageSpec{
		Name:     "postfix",
		EVR:      "3.8-1",
		Arch:     "x86_64",
		Provides: []string{"smtp_daemon"},
	})

	// 1. Without preference -> should return AmbiguityError (ErrChoice)
	_, err = pool.Resolve(solv.ExpansionRequest{
		BuildRequires: []string{"smtp_daemon"},
	})
	if err == nil {
		t.Fatalf("expected AmbiguityError for conflicting providers")
	}
	var ambigErr *solv.AmbiguityError
	if !errors.As(err, &ambigErr) {
		t.Errorf("expected AmbiguityError, got: %v", err)
	}

	// 2. With explicit preference -> should succeed picking postfix
	res, err := pool.Resolve(solv.ExpansionRequest{
		BuildRequires: []string{"smtp_daemon"},
		Prefer:        []string{"postfix"},
	})
	if err != nil {
		t.Fatalf("unexpected error with preference: %v", err)
	}
	if len(res.Packages) != 1 || res.Packages[0].Name != "postfix" {
		t.Errorf("expected postfix chosen, got: %+v", res.Packages)
	}
}

func TestMemorySolvRoundTrip(t *testing.T) {
	pool1, err := solv.NewPool("x86_64")
	if err != nil {
		t.Fatalf("failed to create pool1: %v", err)
	}
	defer pool1.Close()

	repo1 := pool1.CreateRepo("src")
	_ = repo1.AddPackage(solv.PackageSpec{
		Name:     "ripgrep",
		EVR:      "14.1.0-1",
		Arch:     "x86_64",
		Provides: []string{"ripgrep"},
	})

	blob, err := repo1.DumpMemory()
	if err != nil {
		t.Fatalf("failed to dump memory solv blob: %v", err)
	}
	if len(blob) == 0 {
		t.Fatalf("expected non-empty solv blob")
	}

	// Load into second pool from memory blob
	pool2, err := solv.NewPool("x86_64")
	if err != nil {
		t.Fatalf("failed to create pool2: %v", err)
	}
	defer pool2.Close()

	_, err = pool2.LoadRepoFromMemory("loaded", blob)
	if err != nil {
		t.Fatalf("failed to load repo from memory: %v", err)
	}

	res, err := pool2.Resolve(solv.ExpansionRequest{
		BuildRequires: []string{"ripgrep"},
	})
	if err != nil {
		t.Fatalf("failed to resolve from loaded memory repo: %v", err)
	}
	if len(res.Packages) != 1 || res.Packages[0].Name != "ripgrep" {
		t.Errorf("unexpected resolution: %+v", res.Packages)
	}
}

func TestWorkerPool(t *testing.T) {
	wp, err := solv.NewWorkerPool("x86_64", 4)
	if err != nil {
		t.Fatalf("failed to create worker pool: %v", err)
	}
	defer wp.Close()

	// Resolving missing requirement through worker pool should return typed error
	_, err = wp.Resolve(t.Context(), solv.ExpansionRequest{
		BuildRequires: []string{"some-missing-package"},
	})
	if err == nil {
		t.Fatalf("expected error from worker pool")
	}
	var noProvErr *solv.NoProviderError
	if !errors.As(err, &noProvErr) {
		t.Errorf("expected NoProviderError, got: %v", err)
	}
}
