package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/cli"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func setupTestStore(t *testing.T) storage.Store {
	t.Helper()
	store := storage.NewMemoryStore()
	ctx := context.Background()

	prj := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project: manifest.ProjectInfo{
			Name:  "openSUSE:Factory",
			Title: "openSUSE Factory Distribution",
		},
		Targets: map[string]manifest.BuildTarget{
			"standard": {
				Architectures: []string{"x86_64", "aarch64"},
			},
		},
	}
	if err := store.CreateProject(ctx, prj); err != nil {
		t.Fatalf("failed to create test project: %v", err)
	}

	pkg := &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package: manifest.PackageInfo{
			Name: "chromium",
		},
		Source: manifest.PackageSource{
			Git:    "https://github.com/openSUSE/chromium.git",
			Branch: "main",
		},
		Build: manifest.PackageBuild{
			Enabled: true,
		},
		Resources: manifest.ResourceLimits{
			Memory: "16Gi",
			VCPUs:  8,
		},
	}
	if err := store.UpsertPackage(ctx, "openSUSE:Factory", pkg); err != nil {
		t.Fatalf("failed to create test package: %v", err)
	}

	return store
}

func TestProjectListHumanAndJSON(t *testing.T) {
	store := setupTestStore(t)

	// Test Human Output
	cmd := cli.NewRootCommand(store)
	outBuf := new(bytes.Buffer)
	errBuf := new(bytes.Buffer)
	cmd.SetOut(outBuf)
	cmd.SetErr(errBuf)
	cmd.SetArgs([]string{"project", "list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(outBuf.String(), "openSUSE:Factory") {
		t.Errorf("expected output to contain 'openSUSE:Factory', got: %s", outBuf.String())
	}

	// Test JSON Output
	cmdJSON := cli.NewRootCommand(store)
	outJSON := new(bytes.Buffer)
	cmdJSON.SetOut(outJSON)
	cmdJSON.SetArgs([]string{"project", "list", "--json"})

	if err := cmdJSON.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var prjs []manifest.ProjectManifest
	if err := json.Unmarshal(outJSON.Bytes(), &prjs); err != nil {
		t.Fatalf("expected valid json array, got error: %v\nOutput: %s", err, outJSON.String())
	}
	if len(prjs) != 1 || prjs[0].Project.Name != "openSUSE:Factory" {
		t.Errorf("unexpected json project content: %v", prjs)
	}
}

func TestPackageSetResourcesJSON(t *testing.T) {
	store := setupTestStore(t)

	cmd := cli.NewRootCommand(store)
	outBuf := new(bytes.Buffer)
	cmd.SetOut(outBuf)
	cmd.SetArgs([]string{
		"package", "set-resources", "openSUSE:Factory/chromium",
		"--memory", "32Gi",
		"--vcpus", "16",
		"--json",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var res map[string]any
	if err := json.Unmarshal(outBuf.Bytes(), &res); err != nil {
		t.Fatalf("expected valid json from set-resources, got: %v\nOutput: %s", err, outBuf.String())
	}

	if res["status"] != "UPDATED" {
		t.Errorf("expected status 'UPDATED', got %v", res["status"])
	}
	if res["manifest"] != "packages/dd/chromium.yaml" {
		t.Errorf("expected shard path 'packages/dd/chromium.yaml', got %v", res["manifest"])
	}
}

func TestAuthCreateToken(t *testing.T) {
	store := setupTestStore(t)

	cmd := cli.NewRootCommand(store)
	outBuf := new(bytes.Buffer)
	cmd.SetOut(outBuf)
	cmd.SetArgs([]string{
		"auth", "create-token",
		"--name", "ai-factory-bot",
		"--scopes", "project:write:home:bot,build:trigger:*",
		"--json",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error creating token: %v", err)
	}

	var res map[string]any
	if err := json.Unmarshal(outBuf.Bytes(), &res); err != nil {
		t.Fatalf("expected valid json from create-token, got: %v\nOutput: %s", err, outBuf.String())
	}

	rawToken, ok := res["token"].(string)
	if !ok || !strings.HasPrefix(rawToken, "bsgo_pat_") {
		t.Errorf("expected token starting with bsgo_pat_, got: %v", res["token"])
	}
}
