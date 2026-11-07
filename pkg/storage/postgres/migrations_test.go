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

	if len(migrations) != 2 {
		t.Fatalf("expected 2 migrations, got %d", len(migrations))
	}

	if migrations[0].Version != 1 {
		t.Errorf("expected migration 0 version 1, got %d", migrations[0].Version)
	}
	if migrations[1].Version != 2 {
		t.Errorf("expected migration 1 version 2, got %d", migrations[1].Version)
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
}
