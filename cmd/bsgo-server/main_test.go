package main

import (
	"context"
	"testing"

	"github.com/openSUSE/bsgo/pkg/storage"
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

func TestInitSignerDriver(t *testing.T) {
	store := storage.NewMemoryStore()

	// Memory driver
	cfgMem := ServerConfig{SignerType: "memory"}
	drvMem := initSignerDriver(cfgMem, store)
	if drvMem == nil {
		t.Fatalf("expected non-nil memory driver")
	}

	// Remote driver
	cfgRemote := ServerConfig{SignerType: "signd", SigndAddr: "127.0.0.1:5167"}
	drvRemote := initSignerDriver(cfgRemote, store)
	if drvRemote == nil {
		t.Fatalf("expected non-nil remote driver")
	}
}

func TestGetEnv(t *testing.T) {
	if val := getEnv("NON_EXISTENT_VAR_XYZ", "default"); val != "default" {
		t.Errorf("expected default, got %s", val)
	}
}
