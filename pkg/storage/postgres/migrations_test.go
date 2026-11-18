package postgres_test

import (
	"io/fs"
	"math"
	"testing"

	"github.com/openSUSE/bsgo/pkg/storage/postgres"
	"github.com/pressly/goose/v3"
)

func TestEmbeddedMigrations(t *testing.T) {
	subFS, err := fs.Sub(postgres.EmbedMigrations, "migrations")
	if err != nil {
		t.Fatalf("failed to create sub filesystem: %v", err)
	}

	goose.SetBaseFS(subFS)
	defer goose.SetBaseFS(nil)

	migrations, err := goose.CollectMigrations(".", 0, math.MaxInt64)
	if err != nil {
		t.Fatalf("failed to collect migrations: %v", err)
	}

	if len(migrations) != 12 {
		t.Fatalf("expected 12 migrations, got %d", len(migrations))
	}

	if migrations[0].Version != 1 {
		t.Errorf("expected migration 0 version 1, got %d", migrations[0].Version)
	}
	if migrations[1].Version != 2 {
		t.Errorf("expected migration 1 version 2, got %d", migrations[1].Version)
	}
	if migrations[2].Version != 3 {
		t.Errorf("expected migration 2 version 3, got %d", migrations[2].Version)
	}
	if migrations[3].Version != 4 {
		t.Errorf("expected migration 3 version 4, got %d", migrations[3].Version)
	}

	if migrations[4].Version != 5 {
		t.Errorf("expected migration 4 version 5, got %d", migrations[4].Version)
	}
	if migrations[5].Version != 6 {
		t.Errorf("expected migration 5 version 6, got %d", migrations[5].Version)
	}

	// Verify each migration contains expected statements
	m1Content, err := fs.ReadFile(subFS, "00001_projects_and_packages.sql")
	if err != nil {
		t.Fatalf("failed to read migration 1: %v", err)
	}
	if len(m1Content) == 0 {
		t.Error("migration 1 content is empty")
	}

	m2Content, err := fs.ReadFile(subFS, "00002_users_and_access_control.sql")
	if err != nil {
		t.Fatalf("failed to read migration 2: %v", err)
	}
	if len(m2Content) == 0 {
		t.Error("migration 2 content is empty")
	}

	m3Content, err := fs.ReadFile(subFS, "00003_project_signing_keys.sql")
	if err != nil {
		t.Fatalf("failed to read migration 3: %v", err)
	}
	if len(m3Content) == 0 {
		t.Error("migration 3 content is empty")
	}

	m4Content, err := fs.ReadFile(subFS, "00004_target_solv_cache.sql")
	if err != nil {
		t.Fatalf("failed to read migration 4: %v", err)
	}
	if len(m4Content) == 0 {
		t.Error("migration 4 content is empty")
	}
}
