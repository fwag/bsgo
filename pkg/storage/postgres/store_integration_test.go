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

	verifyBuildStates(ctx, t, store, testPrjName)
	verifyDoDRepo(ctx, t, store, fmt.Sprintf("https://mirror.example/%s/", testPrjName))
}

func verifyDoDRepo(ctx context.Context, t *testing.T, store *postgres.PostgresStore, url string) {
	t.Helper()
	if _, err := store.GetDoDRepo(ctx, url); err != storage.ErrNotFound {
		t.Fatalf("expected ErrNotFound for unknown dod repo, got %v", err)
	}
	if err := store.SaveDoDRepo(ctx, &storage.DoDRepo{URL: url}); err == nil {
		t.Fatal("expected validation error for incomplete dod repo")
	}
	first := &storage.DoDRepo{URL: url, Name: "mirror", RepomdChecksum: "aa", SolvKey: "dod/x/p1.solv"}
	if err := store.SaveDoDRepo(ctx, first); err != nil {
		t.Fatalf("failed to save dod repo: %v", err)
	}
	second := &storage.DoDRepo{URL: url, Name: "mirror", RepomdChecksum: "bb", SolvKey: "dod/x/p2.solv"}
	if err := store.SaveDoDRepo(ctx, second); err != nil {
		t.Fatalf("failed to upsert dod repo: %v", err)
	}
	got, err := store.GetDoDRepo(ctx, url)
	if err != nil {
		t.Fatalf("failed to get dod repo: %v", err)
	}
	if got.RepomdChecksum != "bb" || got.SolvKey != "dod/x/p2.solv" || got.Name != "mirror" || got.UpdatedAt.IsZero() {
		t.Errorf("unexpected dod repo: %+v", got)
	}
}

func verifyBuildStates(ctx context.Context, t *testing.T, store *postgres.PostgresStore, prj string) {
	t.Helper()
	rows := []storage.PackageBuildState{
		{Project: prj, Repo: "standard", Arch: "x86_64", Package: "a", State: storage.StateScheduled},
		{Project: prj, Repo: "standard", Arch: "x86_64", Package: "b", State: storage.StateUnresolvable, Details: "nothing provides foo"},
		{Project: prj, Repo: "standard", Arch: "aarch64", Package: "a", State: storage.StateSucceeded},
		{Project: prj, Repo: "standard", Arch: "x86_64", Package: "c", Flavor: "py311", State: storage.StateBlocked},
	}
	for i := range rows {
		if err := store.UpsertBuildState(ctx, &rows[i]); err != nil {
			t.Fatalf("failed to upsert build state: %v", err)
		}
	}
	// A later state replaces the earlier one for the same key.
	if err := store.UpsertBuildState(ctx, &storage.PackageBuildState{
		Project: prj, Repo: "standard", Arch: "x86_64", Package: "a", State: storage.StateSucceeded, JobID: "j1",
	}); err != nil {
		t.Fatalf("failed to re-upsert build state: %v", err)
	}
	if err := store.UpsertBuildState(ctx, &storage.PackageBuildState{
		Project: prj, Repo: "standard", Arch: "x86_64", Package: "z", State: "bogus",
	}); err == nil {
		t.Errorf("expected error for invalid state")
	}

	counts, err := store.CountBuildStates(ctx, prj, storage.BuildStateFilter{})
	if err != nil {
		t.Fatalf("failed to count build states: %v", err)
	}
	if counts[storage.StateSucceeded] != 2 || counts[storage.StateUnresolvable] != 1 || counts[storage.StateBlocked] != 1 || counts[storage.StateScheduled] != 0 {
		t.Errorf("unexpected counts: %v", counts)
	}

	list, err := store.ListBuildStates(ctx, prj, storage.BuildStateFilter{Arch: "x86_64", State: storage.StateUnresolvable})
	if err != nil {
		t.Fatalf("failed to list build states: %v", err)
	}
	if len(list) != 1 || list[0].Package != "b" || list[0].Details != "nothing provides foo" {
		t.Errorf("unexpected filtered list: %+v", list)
	}
	all, err := store.ListBuildStates(ctx, prj, storage.BuildStateFilter{Repo: "standard"})
	if err != nil || len(all) != 4 {
		t.Errorf("expected 4 rows, got %d (err %v)", len(all), err)
	}
}
