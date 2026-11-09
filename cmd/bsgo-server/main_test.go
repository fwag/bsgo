package main

import (
	"context"
	"testing"

	"github.com/openSUSE/bsgo/pkg/manifest"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
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

func TestCreateFallbackJobAttachesRepositoryDependencies(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore()

	prj := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: "net-proj"},
		Targets: map[string]manifest.BuildTarget{
			"standard": {
				Architectures: []string{"x86_64"},
				Dependencies: []manifest.DependencySource{
					{Project: "crypto-proj", Target: "standard"},
				},
			},
		},
	}
	_ = store.CreateProject(ctx, prj)

	trig := &v1.BuildTrigger{
		TriggerID:    "trig-001",
		Project:      "net-proj",
		Package:      "openssh",
		Target:       "standard",
		Architecture: "x86_64",
	}

	job, err := createFallbackJob(ctx, store, trig)
	if err != nil {
		t.Fatalf("unexpected error creating fallback job: %v", err)
	}
	if len(job.Repositories) != 2 {
		t.Fatalf("expected 2 repositories (upstream dependency + self), got %d", len(job.Repositories))
	}
	if job.Repositories[0].Project != "crypto-proj" || job.Repositories[0].S3Prefix != "repos/crypto-proj/standard/x86_64/" {
		t.Errorf("expected crypto-proj upstream, got %+v", job.Repositories[0])
	}
	if job.Repositories[1].Project != "net-proj" || job.Repositories[1].S3Prefix != "repos/net-proj/standard/x86_64/" {
		t.Errorf("expected net-proj self, got %+v", job.Repositories[1])
	}

	// Validation rejection
	if _, err := createFallbackJob(ctx, store, &v1.BuildTrigger{Target: "standard"}); err == nil {
		t.Errorf("expected error when architecture is missing")
	}
	if _, err := createFallbackJob(ctx, store, &v1.BuildTrigger{Architecture: "x86_64"}); err == nil {
		t.Errorf("expected error when target is missing")
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
