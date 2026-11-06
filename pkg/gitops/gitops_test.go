package gitops_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openSUSE/bsgo/pkg/gitops"
	"github.com/openSUSE/bsgo/pkg/manifest"
)

func TestGitOpsWithLocalBareRepo(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// 1. Create a local bare repository
	bareRepoPath := filepath.Join(tmpDir, "openSUSE-Factory.git")
	if err := gitops.InitBareRepo(bareRepoPath); err != nil {
		t.Fatalf("failed to init bare repo: %v", err)
	}

	// 2. Clone/checkout a working copy
	workDir := filepath.Join(tmpDir, "worktree")
	client, err := gitops.CloneLocal(bareRepoPath, workDir)
	if err != nil {
		t.Fatalf("failed to clone local bare repo: %v", err)
	}

	// 3. Commit project.yaml
	projectYAML := []byte(`
schema_version: "v1"
project:
  name: "openSUSE:Factory"
  title: "Factory Distribution"
targets:
  standard:
    architectures: ["x86_64"]
`)
	commitSHA, err := client.CommitFile(ctx, "project.yaml", projectYAML, "Initial project manifest", "bot", "bot@infra.local")
	if err != nil {
		t.Fatalf("failed to commit project.yaml: %v", err)
	}
	if commitSHA == "" {
		t.Errorf("expected non-empty commit SHA")
	}

	// 4. Commit a sharded package manifest
	shardFile := manifest.ShardPath("chromium") // packages/dd/chromium.yaml
	packageYAML := []byte(`
schema_version: "v1"
package:
  name: "chromium"
source:
  git: "https://github.com/openSUSE/chromium.git"
  branch: "main"
resources:
  memory: "32Gi"
  vcpus: 16
`)
	commitSHA2, err := client.CommitFile(ctx, shardFile, packageYAML, "Add chromium manifest", "bot", "bot@infra.local")
	if err != nil {
		t.Fatalf("failed to commit %s: %v", shardFile, err)
	}
	if commitSHA2 == "" {
		t.Errorf("expected non-empty commit SHA")
	}

	// 5. Verify file exists on disk and is readable
	fullPath := filepath.Join(workDir, shardFile)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatalf("failed to read back committed file: %v", err)
	}

	pkg, err := manifest.ParsePackage(data)
	if err != nil {
		t.Fatalf("failed to parse back package manifest: %v", err)
	}
	if pkg.Package.Name != "chromium" {
		t.Errorf("expected package name 'chromium', got %s", pkg.Package.Name)
	}
}
