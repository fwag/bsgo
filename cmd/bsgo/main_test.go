package main

import (
	"context"
	"testing"
)

func TestInitStoreDoesNotSeedDefaultProject(t *testing.T) {
	ctx := context.Background()
	t.Setenv("DATABASE_URL", "postgres://invalid:invalid@127.0.0.1:54329/nonexistent?sslmode=disable")
	store := initStore(ctx)
	prjs, err := store.ListProjects(ctx)
	if err != nil {
		t.Fatalf("unexpected error listing projects: %v", err)
	}
	if len(prjs) != 0 {
		t.Fatalf("expected 0 projects in fresh store, got %d (found %s)", len(prjs), prjs[0].Project.Name)
	}
}
