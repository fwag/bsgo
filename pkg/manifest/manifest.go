package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

// ShardPath returns the 2-character hex hash partition path for a package.
// e.g. packages/dd/chromium.yaml
func ShardPath(packageName string) string {
	h := sha256.Sum256([]byte(packageName))
	shard := hex.EncodeToString(h[:1])
	return fmt.Sprintf("packages/%s/%s.yaml", shard, packageName)
}

type ProjectManifest struct {
	SchemaVersion string                 `yaml:"schema_version"`
	Project       ProjectInfo            `yaml:"project"`
	Targets       map[string]BuildTarget `yaml:"targets"`
}

type ProjectInfo struct {
	Name        string `yaml:"name"`
	Title       string `yaml:"title,omitempty"`
	Description string `yaml:"description,omitempty"`
}

type BuildTarget struct {
	Architectures []string           `yaml:"architectures"`
	Dependencies  []DependencySource `yaml:"dependencies"`
	Flags         TargetFlags        `yaml:"flags,omitempty"`
}

type DependencySource struct {
	Project string `yaml:"project,omitempty"`
	Target  string `yaml:"target,omitempty"`
	URL     string `yaml:"url,omitempty"`
	Type    string `yaml:"type,omitempty"`
}

type TargetFlags struct {
	Build     bool `yaml:"build"`
	Publish   bool `yaml:"publish"`
	Debuginfo bool `yaml:"debuginfo"`
}

type PackageManifest struct {
	SchemaVersion string         `yaml:"schema_version"`
	Package       PackageInfo    `yaml:"package"`
	Source        PackageSource  `yaml:"source"`
	Build         PackageBuild   `yaml:"build,omitempty"`
	Resources     ResourceLimits `yaml:"resources,omitempty"`
}

type PackageInfo struct {
	Name        string `yaml:"name"`
	Title       string `yaml:"title,omitempty"`
	Description string `yaml:"description,omitempty"`
}

type PackageSource struct {
	Git    string `yaml:"git"`
	Branch string `yaml:"branch,omitempty"`
	Commit string `yaml:"commit,omitempty"`
	Recipe string `yaml:"recipe,omitempty"`
}

type PackageBuild struct {
	Enabled       bool            `yaml:"enabled"`
	Targets       map[string]bool `yaml:"targets,omitempty"`
	Architectures ArchFilter      `yaml:"architectures,omitempty"`
}

type ArchFilter struct {
	Include []string `yaml:"include,omitempty"`
	Exclude []string `yaml:"exclude,omitempty"`
}

type ResourceLimits struct {
	Memory  string `yaml:"memory,omitempty"`
	VCPUs   int    `yaml:"vcpus,omitempty"`
	Disk    string `yaml:"disk,omitempty"`
	Timeout string `yaml:"timeout,omitempty"`
}

func ParseProject(data []byte) (*ProjectManifest, error) {
	var prj ProjectManifest
	if err := yaml.Unmarshal(data, &prj); err != nil {
		return nil, fmt.Errorf("failed to parse project yaml: %w", err)
	}

	if prj.SchemaVersion == "" {
		return nil, errors.New("missing required field: schema_version")
	}
	if prj.Project.Name == "" {
		return nil, errors.New("missing required field: project.name")
	}

	return &prj, nil
}

func ParsePackage(data []byte) (*PackageManifest, error) {
	var pkg PackageManifest
	if err := yaml.Unmarshal(data, &pkg); err != nil {
		return nil, fmt.Errorf("failed to parse package yaml: %w", err)
	}

	if pkg.SchemaVersion == "" {
		return nil, errors.New("missing required field: schema_version")
	}
	if pkg.Package.Name == "" {
		return nil, errors.New("missing required field: package.name")
	}
	if pkg.Source.Git == "" {
		return nil, errors.New("missing required field: source.git")
	}

	return &pkg, nil
}
