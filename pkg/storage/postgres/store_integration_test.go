package postgres_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/openSUSE/bsgo/pkg/storage/postgres"
)

func getTestDatabaseURL() string {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://bsgo:password@localhost:5432/bsgodb?sslmode=disable"
	}
	return dbURL
}

func TestPostgresMigrationsAndStore(t *testing.T) {
	connStr := getTestDatabaseURL()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Check connectivity
	testPool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Skipf("skipping postgres integration test: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		testPool.Close()
		t.Skipf("skipping postgres integration test (ping failed): %v", err)
	}
	defer testPool.Close()

	// Verify idempotency of Migrate
	if err := postgres.Migrate(ctx, testPool); err != nil {
		t.Fatalf("failed to run initial migrate: %v", err)
	}
	if err := postgres.Migrate(ctx, testPool); err != nil {
		t.Fatalf("failed to run idempotent migrate: %v", err)
	}

	// Initialize store (applies goose migrations again)
	store, err := postgres.NewPostgresStore(ctx, connStr)
	if err != nil {
		t.Fatalf("failed to create postgres store: %v", err)
	}
	defer store.Close()

	testPrjName := fmt.Sprintf("openSUSE:Test-%d", time.Now().UnixNano())

	// 1. Verify Project creation
	prj := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project: manifest.ProjectInfo{
			Name:        testPrjName,
			Title:       "openSUSE Test Distribution",
			Description: "Rolling release",
		},
	}
	if err := store.CreateProject(ctx, prj); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	gotPrj, err := store.GetProject(ctx, testPrjName)
	if err != nil {
		t.Fatalf("failed to get project: %v", err)
	}
	if gotPrj.Project.Title != "openSUSE Test Distribution" {
		t.Errorf("expected title 'openSUSE Test Distribution', got '%s'", gotPrj.Project.Title)
	}

	// 2. Verify Package upsert
	pkg := &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package: manifest.PackageInfo{
			Name: "bash",
		},
		Source: manifest.PackageSource{
			Git:    "https://github.com/openSUSE/bash.git",
			Branch: "main",
		},
		Build: manifest.PackageBuild{
			Enabled: true,
		},
		Resources: manifest.ResourceLimits{
			Memory: "4Gi",
			VCPUs:  4,
		},
	}
	if err := store.UpsertPackage(ctx, testPrjName, pkg); err != nil {
		t.Fatalf("failed to upsert package: %v", err)
	}

	gotPkg, err := store.GetPackage(ctx, testPrjName, "bash")
	if err != nil {
		t.Fatalf("failed to get package: %v", err)
	}
	if gotPkg.Package.Name != "bash" {
		t.Errorf("expected package name 'bash', got '%s'", gotPkg.Package.Name)
	}

	// 3. Verify List
	pkgs, err := store.ListPackages(ctx, testPrjName)
	if err != nil {
		t.Fatalf("failed to list packages: %v", err)
	}
	if len(pkgs) != 1 {
		t.Errorf("expected 1 package, got %d", len(pkgs))
	}

	// 4. Verify TargetSolv operations
	solv := &storage.TargetSolv{
		ProjectName: testPrjName,
		TargetName:  "standard",
		Arch:        "x86_64",
		SolvBlob:    []byte("binary-solv-test-data"),
		ContentHash: "sha256-dummy-hash",
	}
	if err := store.SaveTargetSolv(ctx, solv); err != nil {
		t.Fatalf("failed to save target solv: %v", err)
	}

	gotSolv, err := store.GetTargetSolv(ctx, testPrjName, "standard", "x86_64")
	if err != nil {
		t.Fatalf("failed to get target solv: %v", err)
	}
	if string(gotSolv.SolvBlob) != "binary-solv-test-data" || gotSolv.ContentHash != "sha256-dummy-hash" {
		t.Errorf("unexpected target solv: %+v", gotSolv)
	}

	solvs, err := store.ListTargetSolvs(ctx, testPrjName)
	if err != nil {
		t.Fatalf("failed to list target solvs: %v", err)
	}
	if len(solvs) != 1 {
		t.Errorf("expected 1 target solv, got %d", len(solvs))
	}
}
