package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/openSUSE/bsgo/pkg/cli"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func TestAdminTriggerCLI(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore()

	prj := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: "openSUSE:Factory"},
		Targets: map[string]manifest.BuildTarget{
			"standard": {
				Architectures: []string{"x86_64", "aarch64"},
				Flags:         manifest.TargetFlags{Build: true, Rebuild: "transitive"},
			},
		},
	}
	_ = store.CreateProject(ctx, prj)

	for _, name := range []string{"curl", "openssl"} {
		pkg := &manifest.PackageManifest{
			SchemaVersion: "v1",
			Package:       manifest.PackageInfo{Name: name},
			Source:        manifest.PackageSource{Git: "https://git/" + name, Commit: "abc1234"},
			Build:         manifest.PackageBuild{Enabled: true},
		}
		_ = store.UpsertPackage(ctx, "openSUSE:Factory", pkg)
	}

	t.Run("admin trigger single package json dry-run", func(t *testing.T) {
		rootCmd := cli.NewRootCommand(store)
		var out bytes.Buffer
		rootCmd.SetOut(&out)
		rootCmd.SetArgs([]string{
			"admin", "trigger", "openSUSE:Factory", "curl",
			"--priority", "high",
			"--reason", "CVE-2026-1234",
			"--target", "standard",
			"--arch", "x86_64",
			"--dry-run",
			"--json",
		})

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var results []map[string]any
		if err := json.Unmarshal(out.Bytes(), &results); err != nil {
			t.Fatalf("failed to unmarshal json output: %v, output: %s", err, out.String())
		}

		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		res := results[0]
		if res["package"] != "curl" {
			t.Errorf("expected package 'curl', got %v", res["package"])
		}
		if res["priority"] != "high" {
			t.Errorf("expected priority 'high', got %v", res["priority"])
		}
		if res["reason"] != "CVE-2026-1234" {
			t.Errorf("expected reason 'CVE-2026-1234', got %v", res["reason"])
		}
		if res["subject"] != "bsgo.triggers.high.openSUSE:Factory.curl" {
			t.Errorf("expected subject 'bsgo.triggers.high.openSUSE:Factory.curl', got %v", res["subject"])
		}
	})

	t.Run("admin trigger all packages in project", func(t *testing.T) {
		rootCmd := cli.NewRootCommand(store)
		var out bytes.Buffer
		rootCmd.SetOut(&out)
		rootCmd.SetArgs([]string{
			"admin", "trigger", "openSUSE:Factory",
			"--all",
			"--priority", "med",
			"--dry-run",
			"--json",
		})

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var results []map[string]any
		if err := json.Unmarshal(out.Bytes(), &results); err != nil {
			t.Fatalf("failed to unmarshal json output: %v", err)
		}

		if len(results) != 2 {
			t.Fatalf("expected 2 results for --all, got %d", len(results))
		}
	})
}
