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

func TestNamespaceScopedAndPrivateConfig(t *testing.T) {
	yamlData := []byte(`
schema_version: "v1"
project:
  name: "SUSE:SLFO:1.2"
  title: "SLFO 1.2"

config:
  prefer: ["mariadb-server", "python313"]
  build_flags: ["setvcs"]

namespace_scoped:
  build_flags: ["excludebuild:lttng-modules"]
  macros: "%_without_apparmor 1"

private:
  build_flags: ["onlybuild:ALP-dummy-release"]

targets:
  standard:
    architectures: ["x86_64"]
`)

	prj, err := manifest.ParseProject(yamlData)
	if err != nil {
		t.Fatalf("failed parsing project: %v", err)
	}

	if prj.ProjectNamespace() != "SUSE:SLFO" {
		t.Errorf("expected namespace 'SUSE:SLFO', got %q", prj.ProjectNamespace())
	}

	if !prj.IsInNamespace("SUSE:SLFO:1.2") {
		t.Errorf("expected exact project to be in namespace")
	}
	if !prj.IsInNamespace("SUSE:SLFO:1.2:PullRequest") {
		t.Errorf("expected subproject to be in namespace")
	}
	if !prj.IsInNamespace("SUSE:SLFO:Products:SLES") {
		t.Errorf("expected sibling subproject in SUSE:SLFO to be in namespace")
	}
	if prj.IsInNamespace("home:developer:branches:SLFO") {
		t.Errorf("home project must NOT be in namespace")
	}

	// 1. Self build: gets Config + NamespaceScoped + Private
	selfCfg := prj.EffectiveTargetConfig("standard")
	hasFlag := func(flags []string, flag string) bool {
		for _, f := range flags {
			if f == flag {
				return true
			}
		}
		return false
	}
	if !hasFlag(selfCfg.BuildFlags, "setvcs") {
		t.Errorf("self expected public flag 'setvcs'")
	}
	if !hasFlag(selfCfg.BuildFlags, "excludebuild:lttng-modules") {
		t.Errorf("self expected namespace-scoped flag 'excludebuild:lttng-modules'")
	}
	if !hasFlag(selfCfg.BuildFlags, "onlybuild:ALP-dummy-release") {
		t.Errorf("self expected private flag 'onlybuild:ALP-dummy-release'")
	}
	if selfCfg.Macros != "%_without_apparmor 1" {
		t.Errorf("self expected macro '%%_without_apparmor 1', got %q", selfCfg.Macros)
	}

	// 2. Same-namespace consumer: gets Config + NamespaceScoped, but NO Private
	subPrjCfg := prj.EffectiveConfigForConsumer("standard", "SUSE:SLFO:1.2:PullRequest")
	if !hasFlag(subPrjCfg.BuildFlags, "setvcs") {
		t.Errorf("subproject expected public flag 'setvcs'")
	}
	if !hasFlag(subPrjCfg.BuildFlags, "excludebuild:lttng-modules") {
		t.Errorf("subproject expected namespace-scoped flag 'excludebuild:lttng-modules'")
	}
	if hasFlag(subPrjCfg.BuildFlags, "onlybuild:ALP-dummy-release") {
		t.Errorf("subproject must NOT inherit private flag 'onlybuild:ALP-dummy-release'")
	}

	// 3. Outside-namespace consumer: gets ONLY Config (public)
	homeCfg := prj.EffectiveConfigForConsumer("standard", "home:developer:branches:SLFO")
	if !hasFlag(homeCfg.BuildFlags, "setvcs") {
		t.Errorf("home project expected public flag 'setvcs'")
	}
	if hasFlag(homeCfg.BuildFlags, "excludebuild:lttng-modules") {
		t.Errorf("home project must NOT inherit namespace-scoped 'excludebuild:lttng-modules'")
	}
	if hasFlag(homeCfg.BuildFlags, "onlybuild:ALP-dummy-release") {
		t.Errorf("home project must NOT inherit private flag 'onlybuild:ALP-dummy-release'")
	}
	if homeCfg.Macros != "" {
		t.Errorf("home project must NOT inherit namespace-scoped macros, got %q", homeCfg.Macros)
	}
}

func TestSubtractiveCancellationMerging(t *testing.T) {
	base := &manifest.ProjectConfig{
		Prefer:     []string{"mariadb-server", "postfix", "gcc"},
		BuildFlags: []string{"setvcs", "useccache:git"},
	}
	override := &manifest.ProjectConfig{
		Prefer:     []string{"-mariadb-server", "!postfix", "mysql-server"},
		BuildFlags: []string{"!useccache:git", "cumulaterpms"},
	}

	merged := base.Merge(override)
	if len(merged.Prefer) != 2 || merged.Prefer[0] != "gcc" || merged.Prefer[1] != "mysql-server" {
		t.Errorf("expected prefer ['gcc', 'mysql-server'], got %v", merged.Prefer)
	}
	if len(merged.BuildFlags) != 2 || merged.BuildFlags[0] != "setvcs" || merged.BuildFlags[1] != "cumulaterpms" {
		t.Errorf("expected buildflags ['setvcs', 'cumulaterpms'], got %v", merged.BuildFlags)
	}
}

func TestProjectConfigScopesNamingAndAliases(t *testing.T) {
	yamlCanonical := []byte(`
schema_version: "v1"
project:
  name: "SUSE:SLFO:1.2"
  namespace: "SUSE:SLFO"

public_config:
  prefer: ["mariadb-server"]

protected_config:
  build_flags: ["excludebuild:lttng-modules"]

private_config:
  build_flags: ["onlybuild:ALP-dummy-release"]

targets:
  standard:
    architectures: ["x86_64"]
`)

	prj, err := manifest.ParseProject(yamlCanonical)
	if err != nil {
		t.Fatalf("failed parsing canonical scopes: %v", err)
	}

	if prj.GetPublicConfig() == nil || len(prj.GetPublicConfig().Prefer) != 1 {
		t.Errorf("expected public_config to be populated")
	}
	if prj.GetProtectedConfig() == nil || len(prj.GetProtectedConfig().BuildFlags) != 1 {
		t.Errorf("expected protected_config to be populated")
	}
	if prj.GetPrivateConfig() == nil || len(prj.GetPrivateConfig().BuildFlags) != 1 {
		t.Errorf("expected private_config to be populated")
	}

	if prj.Config != prj.PublicConfig {
		t.Errorf("expected Config alias to match PublicConfig")
	}
	if prj.Protected != prj.ProtectedConfig || prj.NamespaceScoped != prj.ProtectedConfig {
		t.Errorf("expected Protected and NamespaceScoped aliases to match ProtectedConfig")
	}
	if prj.Private != prj.PrivateConfig {
		t.Errorf("expected Private alias to match PrivateConfig")
	}
}

func TestTargetArchConfigResolution(t *testing.T) {
	yamlData := []byte(`
schema_version: "v1"
project:
  name: "SUSE:SLFO:1.2"

public_config:
  prefer: ["mariadb-server"]
  build_flags: ["setvcs"]

targets:
  standard:
    architectures: ["x86_64", "ppc64le", "s390x"]
    config:
      build_types: ["spec"]
    arch_config:
      x86_64:
        build_flags: ["signflavor:2023-05-10"]
        prefer: ["kernel-default-base"]
      ppc64le:
        build_flags: ["signflavor:2025-01-22"]
        constraints: ["hardware:cpu:flag power9"]
      s390x:
        build_flags: ["signflavor:2025-01-22"]
        prefer: ["kernel-default"]
        constraints: ["hardware:cpu:flag vxd"]
`)

	prj, err := manifest.ParseProject(yamlData)
	if err != nil {
		t.Fatalf("failed parsing project: %v", err)
	}

	hasFlag := func(flags []string, f string) bool {
		for _, x := range flags {
			if x == f {
				return true
			}
		}
		return false
	}

	// 1. x86_64 resolution
	x86Cfg := prj.EffectiveTargetArchConfig("standard", "x86_64")
	if len(x86Cfg.Constraints) != 0 {
		t.Errorf("x86_64 must have no constraints, got %v", x86Cfg.Constraints)
	}
	if !hasFlag(x86Cfg.BuildFlags, "signflavor:2023-05-10") {
		t.Errorf("x86_64 expected signflavor:2023-05-10")
	}

	// 2. ppc64le resolution
	ppcCfg := prj.EffectiveTargetArchConfig("standard", "ppc64le")
	if len(ppcCfg.Constraints) != 1 || ppcCfg.Constraints[0] != "hardware:cpu:flag power9" {
		t.Errorf("ppc64le expected power9 constraint, got %v", ppcCfg.Constraints)
	}
	if !hasFlag(ppcCfg.BuildFlags, "signflavor:2025-01-22") {
		t.Errorf("ppc64le expected signflavor:2025-01-22")
	}

	// 3. s390x resolution
	s390Cfg := prj.EffectiveTargetArchConfig("standard", "s390x")
	if len(s390Cfg.Constraints) != 1 || s390Cfg.Constraints[0] != "hardware:cpu:flag vxd" {
		t.Errorf("s390x expected vxd constraint, got %v", s390Cfg.Constraints)
	}
	if !hasFlag(s390Cfg.Prefer, "kernel-default") {
		t.Errorf("s390x expected kernel-default prefer, got %v", s390Cfg.Prefer)
	}
}
