package main

import (
	"context"
	"testing"
)

func TestInitControlPlaneStoreDoesNotSeedDefaultProject(t *testing.T) {
	ctx := context.Background()
	store := initControlPlaneStore(ctx, "postgres://invalid:invalid@127.0.0.1:54329/nonexistent?sslmode=disable")
	prjs, err := store.ListProjects(ctx)
	if err != nil {
		t.Fatalf("unexpected error listing projects: %v", err)
	}
	if len(prjs) != 0 {
		t.Fatalf("expected 0 projects in fresh store, got %d (found %s)", len(prjs), prjs[0].Project.Name)
	}
}

func TestGetEnv(t *testing.T) {
	if val := getEnv("NON_EXISTENT_VAR_XYZ", "default"); val != "default" {
		t.Errorf("expected default, got %s", val)
	}
}
