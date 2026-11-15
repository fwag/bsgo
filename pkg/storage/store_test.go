package storage_test

import (
	"context"
	"testing"

	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func TestMemoryStore(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore()

	// 1. Create project
	prj := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project: manifest.ProjectInfo{
			Name:  "openSUSE:Factory",
			Title: "openSUSE Factory Distribution",
		},
	}
	if err := store.CreateProject(ctx, prj); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Conflict test
	if err := store.CreateProject(ctx, prj); err != storage.ErrConflict {
		t.Errorf("expected ErrConflict, got %v", err)
	}

	// Get project
	gotPrj, err := store.GetProject(ctx, "openSUSE:Factory")
	if err != nil {
		t.Fatalf("failed to get project: %v", err)
	}
	if gotPrj.Project.Title != "openSUSE Factory Distribution" {
		t.Errorf("unexpected project title: %s", gotPrj.Project.Title)
	}

	// 2. Upsert package
	pkg := &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package: manifest.PackageInfo{
			Name: "chromium",
		},
		Source: manifest.PackageSource{
			Git:    "https://github.com/openSUSE/chromium.git",
			Branch: "main",
		},
	}
	if err := store.UpsertPackage(ctx, "openSUSE:Factory", pkg); err != nil {
		t.Fatalf("failed to upsert package: %v", err)
	}

	// Get package
	gotPkg, err := store.GetPackage(ctx, "openSUSE:Factory", "chromium")
	if err != nil {
		t.Fatalf("failed to get package: %v", err)
	}
	if gotPkg.Package.Name != "chromium" {
		t.Errorf("expected package name 'chromium', got %s", gotPkg.Package.Name)
	}

	// List packages
	pkgs, err := store.ListPackages(ctx, "openSUSE:Factory")
	if err != nil {
		t.Fatalf("failed to list packages: %v", err)
	}
	if len(pkgs) != 1 {
		t.Errorf("expected 1 package, got %d", len(pkgs))
	}

	// 3. Target Solv operations
	solv := &storage.TargetSolv{
		ProjectName: "openSUSE:Factory",
		TargetName:  "standard",
		Arch:        "x86_64",
		SolvBlob:    []byte("binary-solv-data"),
		ContentHash: "hash-1234",
	}
	if err := store.SaveTargetSolv(ctx, solv); err != nil {
		t.Fatalf("failed to save target solv: %v", err)
	}

	gotSolv, err := store.GetTargetSolv(ctx, "openSUSE:Factory", "standard", "x86_64")
	if err != nil {
		t.Fatalf("failed to get target solv: %v", err)
	}
	if string(gotSolv.SolvBlob) != "binary-solv-data" || gotSolv.ContentHash != "hash-1234" {
		t.Errorf("unexpected target solv: %+v", gotSolv)
	}

	solvs, err := store.ListTargetSolvs(ctx, "openSUSE:Factory")
	if err != nil {
		t.Fatalf("failed to list target solvs: %v", err)
	}
	if len(solvs) != 1 {
		t.Errorf("expected 1 target solv, got %d", len(solvs))
	}
}

func TestMemoryStoreBuildStates(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore()
	prj := "home:test"

	valid := []storage.PackageBuildState{
		{Project: prj, Repo: "standard", Arch: "x86_64", Package: "a", State: storage.StateScheduled},
		{Project: prj, Repo: "standard", Arch: "x86_64", Package: "b", State: storage.StateUnresolvable, Details: "no provider"},
		{Project: prj, Repo: "standard", Arch: "aarch64", Package: "a", State: storage.StateSucceeded},
		{Project: prj, Repo: "standard", Arch: "x86_64", Package: "c", Flavor: "f", State: storage.StateBlocked},
		{Project: "other", Repo: "standard", Arch: "x86_64", Package: "a", State: storage.StateFailed},
	}
	for i := range valid {
		if err := store.UpsertBuildState(ctx, &valid[i]); err != nil {
			t.Fatalf("upsert %d failed: %v", i, err)
		}
	}
	// Later state overwrites earlier one for the same key.
	if err := store.UpsertBuildState(ctx, &storage.PackageBuildState{
		Project: prj, Repo: "standard", Arch: "x86_64", Package: "a", State: storage.StateSucceeded,
	}); err != nil {
		t.Fatalf("re-upsert failed: %v", err)
	}

	invalid := []storage.PackageBuildState{
		{Project: prj, Repo: "", Arch: "x86_64", Package: "a", State: storage.StateFailed},
		{Project: prj, Repo: "standard", Arch: "", Package: "a", State: storage.StateFailed},
		{Project: prj, Repo: "standard", Arch: "x86_64", Package: "a", State: "bogus"},
	}
	for i := range invalid {
		if err := store.UpsertBuildState(ctx, &invalid[i]); err == nil {
			t.Errorf("expected error for invalid row %d", i)
		}
	}

	tests := []struct {
		name      string
		filter    storage.BuildStateFilter
		wantRows  int
		wantCount map[string]int
	}{
		{"all", storage.BuildStateFilter{}, 4, map[string]int{"succeeded": 2, "unresolvable": 1, "blocked": 1}},
		{"arch", storage.BuildStateFilter{Arch: "x86_64"}, 3, map[string]int{"succeeded": 1, "unresolvable": 1, "blocked": 1}},
		{"state", storage.BuildStateFilter{State: storage.StateUnresolvable}, 1, map[string]int{"unresolvable": 1}},
		{"repo-miss", storage.BuildStateFilter{Repo: "nope"}, 0, map[string]int{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			list, err := store.ListBuildStates(ctx, prj, tc.filter)
			if err != nil || len(list) != tc.wantRows {
				t.Fatalf("expected %d rows, got %d (err %v)", tc.wantRows, len(list), err)
			}
			counts, err := store.CountBuildStates(ctx, prj, tc.filter)
			if err != nil {
				t.Fatalf("count failed: %v", err)
			}
			if len(counts) != len(tc.wantCount) {
				t.Fatalf("expected counts %v, got %v", tc.wantCount, counts)
			}
			for k, v := range tc.wantCount {
				if counts[k] != v {
					t.Errorf("count[%s] = %d, want %d", k, counts[k], v)
				}
			}
		})
	}
}

func TestMemoryStoreUpsertPreservesBuildIdentity(t *testing.T) {
	tests := []struct {
		name       string
		next       storage.PackageBuildState
		wantSrcMD5 string
		wantMeta   string
		wantBcnt   int
	}{
		{"status-only update keeps identity", storage.PackageBuildState{State: storage.StateBuilding}, "abc", "m1", 3},
		{"new identity replaces old", storage.PackageBuildState{State: storage.StateScheduled, SrcMD5: "def", Meta: "m2", Bcnt: 4}, "def", "m2", 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := storage.NewMemoryStore()
			base := storage.PackageBuildState{Project: "p", Repo: "r", Arch: "a", Package: "x",
				State: storage.StateSucceeded, SrcMD5: "abc", Meta: "m1", Bcnt: 3}
			if err := s.UpsertBuildState(ctx, &base); err != nil {
				t.Fatal(err)
			}
			next := tt.next
			next.Project, next.Repo, next.Arch, next.Package = "p", "r", "a", "x"
			if err := s.UpsertBuildState(ctx, &next); err != nil {
				t.Fatal(err)
			}
			got, err := s.ListBuildStates(ctx, "p", storage.BuildStateFilter{})
			if err != nil || len(got) != 1 {
				t.Fatalf("list: %v %d", err, len(got))
			}
			if got[0].SrcMD5 != tt.wantSrcMD5 || got[0].Meta != tt.wantMeta || got[0].Bcnt != tt.wantBcnt {
				t.Errorf("got %+v", got[0])
			}
		})
	}
}
