package manifest_test

import (
	"testing"

	"github.com/openSUSE/bsgo/pkg/manifest"
)

func TestShardPath(t *testing.T) {
	tests := []struct {
		pkgName  string
		expected string
	}{
		{pkgName: "chromium", expected: "packages/dd/chromium.yaml"},
		{pkgName: "glibc", expected: "packages/1b/glibc.yaml"},
		{pkgName: "ripgrep", expected: "packages/33/ripgrep.yaml"},
		{pkgName: "python-requests", expected: "packages/99/python-requests.yaml"},
	}

	for _, tt := range tests {
		t.Run(tt.pkgName, func(t *testing.T) {
			got := manifest.ShardPath(tt.pkgName)
			if got != tt.expected {
				t.Errorf("ShardPath(%q) = %q, want %q", tt.pkgName, got, tt.expected)
			}
		})
	}
}

func TestParseProjectManifestValid(t *testing.T) {
	yamlData := []byte(`
schema_version: "v1"
project:
  name: "openSUSE:Factory"
  title: "openSUSE Factory Distribution"
  description: "Rolling release"
targets:
  standard:
    architectures: ["x86_64", "aarch64"]
    dependencies:
      - project: "openSUSE:Base"
        target: "standard"
      - url: "https://download.opensuse.org/factory/repo/oss/"
        type: "rpm-md"
    flags:
      build: true
      publish: true
      debuginfo: true
`)

	prj, err := manifest.ParseProject(yamlData)
	if err != nil {
		t.Fatalf("unexpected error parsing valid project: %v", err)
	}

	if prj.Project.Name != "openSUSE:Factory" {
		t.Errorf("expected name 'openSUSE:Factory', got %q", prj.Project.Name)
	}
	if len(prj.Targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(prj.Targets))
	}
	tgt, ok := prj.Targets["standard"]
	if !ok {
		t.Fatalf("missing 'standard' target")
	}
	if len(tgt.Architectures) != 2 {
		t.Errorf("expected 2 archs, got %d", len(tgt.Architectures))
	}
	if len(tgt.Dependencies) != 2 {
		t.Errorf("expected 2 dependencies, got %d", len(tgt.Dependencies))
	}
}

func TestParsePackageManifestValid(t *testing.T) {
	yamlData := []byte(`
schema_version: "v1"
package:
  name: "chromium"
  title: "Chromium Browser"
source:
  git: "https://github.com/openSUSE/chromium.git"
  branch: "factory"
build:
  enabled: true
  targets:
    standard: true
  architectures:
    exclude: ["s390x"]
resources:
  memory: "32Gi"
  vcpus: 16
  disk: "100Gi"
  timeout: "8h"
`)

	pkg, err := manifest.ParsePackage(yamlData)
	if err != nil {
		t.Fatalf("unexpected error parsing valid package: %v", err)
	}

	if pkg.Package.Name != "chromium" {
		t.Errorf("expected name 'chromium', got %q", pkg.Package.Name)
	}
	if pkg.Source.Branch != "factory" {
		t.Errorf("expected branch 'factory', got %q", pkg.Source.Branch)
	}
	if pkg.Resources.Memory != "32Gi" {
		t.Errorf("expected memory '32Gi', got %q", pkg.Resources.Memory)
	}
	if pkg.Resources.VCPUs != 16 {
		t.Errorf("expected vcpus 16, got %d", pkg.Resources.VCPUs)
	}
}

func TestParseInvalidManifests(t *testing.T) {
	t.Run("missing schema_version", func(t *testing.T) {
		_, err := manifest.ParseProject([]byte(`project: {name: "foo"}`))
		if err == nil {
			t.Errorf("expected error for missing schema_version")
		}
	})

	t.Run("missing project name", func(t *testing.T) {
		_, err := manifest.ParseProject([]byte(`schema_version: "v1"`))
		if err == nil {
			t.Errorf("expected error for missing project name")
		}
	})

	t.Run("missing package name", func(t *testing.T) {
		_, err := manifest.ParsePackage([]byte(`schema_version: "v1", source: {git: "http://git"}`))
		if err == nil {
			t.Errorf("expected error for missing package name")
		}
	})

	t.Run("missing package source git", func(t *testing.T) {
		_, err := manifest.ParsePackage([]byte(`schema_version: "v1", package: {name: "foo"}`))
		if err == nil {
			t.Errorf("expected error for missing source git")
		}
	})
}
