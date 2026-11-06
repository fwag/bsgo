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
}
