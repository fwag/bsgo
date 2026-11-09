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
		{pkgName: "../../evil", expected: "packages/b5/evil.yaml"},
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
      rebuild: "local"
      block: "never"
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
	if tgt.Flags.EffectiveRebuild() != "local" {
		t.Errorf("expected effective rebuild 'local', got %q", tgt.Flags.EffectiveRebuild())
	}
	if tgt.Flags.EffectiveBlock() != "never" {
		t.Errorf("expected effective block 'never', got %q", tgt.Flags.EffectiveBlock())
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

func TestProjectConfigParsingAndMerging(t *testing.T) {
	yamlData := []byte(`
schema_version: "v1"
project:
  name: "openSUSE:Factory"
config:
  build_types: ["spec"]
  build_engine: "build"
  repo_types: ["rpm-md"]
  prefer: ["mariadb-server", "java-17-openjdk-devel"]
  ignore: ["broken-pkg"]
  substitute:
    default-jdk: ["java-17-openjdk-devel"]
  preinstall: ["bash", "coreutils", "rpm"]
  vminstall: ["kernel-default"]
  build_flags: ["allowrootforbuild"]
  optflags:
    x86_64: "-O2 -g"
  macros: "%vendor openSUSE"

targets:
  standard:
    architectures: ["x86_64"]
    dependencies:
      - project: "openSUSE:Base"
        target: "standard"

  images:
    architectures: ["x86_64"]
    config:
      build_types: ["kiwi", "docker"]
      build_engine: "podman"
      repo_types: ["none"]
      prefer: ["libjpeg-turbo"]
      substitute:
        system-packages:docker: ["podman"]
      macros: "%_target_image 1"

  bootstrap:
    architectures: ["x86_64"]
    config:
      from_scratch: true
      build_types: ["spec"]
      build_engine: "build"
      repo_types: ["rpm-md"]
      preinstall: ["busybox", "rpm"]
`)

	prj, err := manifest.ParseProject(yamlData)
	if err != nil {
		t.Fatalf("failed to parse project with config: %v", err)
	}

	if prj.Config == nil {
		t.Fatalf("expected non-nil root config")
	}
	if prj.Config.EffectiveBuildEngine() != "build" {
		t.Errorf("expected build engine 'build', got %q", prj.Config.EffectiveBuildEngine())
	}
	if !prj.Config.HasBuildType("spec") {
		t.Errorf("expected HasBuildType(spec) to be true")
	}
	if !prj.Config.HasRepoType("rpm-md") {
		t.Errorf("expected HasRepoType(rpm-md) to be true")
	}

	// 1. Target with no override inherits root config
	stdCfg := prj.EffectiveTargetConfig("standard")
	if stdCfg.EffectiveBuildEngine() != "build" {
		t.Errorf("standard target: expected build engine 'build', got %q", stdCfg.EffectiveBuildEngine())
	}
	if len(stdCfg.Prefer) != 2 {
		t.Errorf("standard target: expected 2 prefer items, got %d", len(stdCfg.Prefer))
	}

	// 2. Target with override merges fields
	imgCfg := prj.EffectiveTargetConfig("images")
	if imgCfg.EffectiveBuildEngine() != "podman" {
		t.Errorf("images target: expected build engine 'podman', got %q", imgCfg.EffectiveBuildEngine())
	}
	if !imgCfg.HasBuildType("kiwi") || !imgCfg.HasBuildType("docker") {
		t.Errorf("images target: expected kiwi and docker build types, got %v", imgCfg.BuildTypes)
	}
	if !imgCfg.HasRepoType("none") {
		t.Errorf("images target: expected repo type 'none', got %v", imgCfg.RepoTypes)
	}
	// Prefer should be merged: root (2) + images (1) = 3
	if len(imgCfg.Prefer) != 3 {
		t.Errorf("images target: expected 3 prefer items, got %d (%v)", len(imgCfg.Prefer), imgCfg.Prefer)
	}
	// Substitute map should be merged: default-jdk and system-packages:docker
	if len(imgCfg.Substitute) != 2 {
		t.Errorf("images target: expected 2 substitute entries, got %d", len(imgCfg.Substitute))
	}
	// Macros should be concatenated
	expectedMacros := "%vendor openSUSE\n%_target_image 1"
	if imgCfg.Macros != expectedMacros {
		t.Errorf("images target: expected macros %q, got %q", expectedMacros, imgCfg.Macros)
	}

	// 3. Target with from_scratch: true discards root config
	bootCfg := prj.EffectiveTargetConfig("bootstrap")
	if len(bootCfg.Prefer) != 0 {
		t.Errorf("bootstrap target: expected 0 prefer items from scratch, got %d", len(bootCfg.Prefer))
	}
	if len(bootCfg.Preinstall) != 2 || bootCfg.Preinstall[0] != "busybox" {
		t.Errorf("bootstrap target: expected busybox preinstall, got %v", bootCfg.Preinstall)
	}
	if bootCfg.Macros != "" {
		t.Errorf("bootstrap target: expected empty macros from scratch, got %q", bootCfg.Macros)
	}
}

func TestProjectConfigDefaults(t *testing.T) {
	var emptyCfg *manifest.ProjectConfig

	if emptyCfg.EffectiveBuildEngine() != "build" {
		t.Errorf("expected default engine 'build', got %q", emptyCfg.EffectiveBuildEngine())
	}
	if len(emptyCfg.EffectiveBuildTypes()) != 1 || emptyCfg.EffectiveBuildTypes()[0] != "spec" {
		t.Errorf("expected default build type ['spec'], got %v", emptyCfg.EffectiveBuildTypes())
	}
	if len(emptyCfg.EffectiveRepoTypes()) != 1 || emptyCfg.EffectiveRepoTypes()[0] != "rpm-md" {
		t.Errorf("expected default repo type ['rpm-md'], got %v", emptyCfg.EffectiveRepoTypes())
	}
	if !emptyCfg.HasBuildType("spec") {
		t.Errorf("expected HasBuildType(spec) to be true for nil config")
	}
	if !emptyCfg.HasRepoType("rpm-md") {
		t.Errorf("expected HasRepoType(rpm-md) to be true for nil config")
	}
}
