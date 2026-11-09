package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ShardPath returns the 2-character hex hash partition path for a package.
// e.g. packages/dd/chromium.yaml
func ShardPath(packageName string) string {
	cleanName := filepath.Base(filepath.Clean(packageName))
	cleanName = strings.ReplaceAll(cleanName, "..", "")
	h := sha256.Sum256([]byte(cleanName))
	shard := hex.EncodeToString(h[:1])
	return fmt.Sprintf("packages/%s/%s.yaml", shard, cleanName)
}

type ProjectManifest struct {
	SchemaVersion string                 `yaml:"schema_version"`
	Project       ProjectInfo            `yaml:"project"`
	Config        *ProjectConfig         `yaml:"config,omitempty"`
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
	Config        *ProjectConfig     `yaml:"config,omitempty"`
	Flags         TargetFlags        `yaml:"flags,omitempty"`
}

// ProjectConfig defines project-wide or target-specific build, solver, and publication settings.
type ProjectConfig struct {
	BuildTypes   []string            `yaml:"build_types,omitempty"`
	BuildEngine  string              `yaml:"build_engine,omitempty"`
	RepoTypes    []string            `yaml:"repo_types,omitempty"`
	FromScratch  bool                `yaml:"from_scratch,omitempty"`
	Prefer       []string            `yaml:"prefer,omitempty"`
	Ignore       []string            `yaml:"ignore,omitempty"`
	Substitute   map[string][]string `yaml:"substitute,omitempty"`
	Conflict     [][]string          `yaml:"conflict,omitempty"`
	Preinstall   []string            `yaml:"preinstall,omitempty"`
	VMinstall    []string            `yaml:"vminstall,omitempty"`
	Order        []string            `yaml:"order,omitempty"`
	BuildFlags   []string            `yaml:"build_flags,omitempty"`
	PublishFlags []string            `yaml:"publish_flags,omitempty"`
	Optflags     map[string]string   `yaml:"optflags,omitempty"`
	Macros       string              `yaml:"macros,omitempty"`
	MacrosFile   string              `yaml:"macros_file,omitempty"`
}

// EffectiveBuildTypes returns the declared build types or defaults to ["spec"].
func (c *ProjectConfig) EffectiveBuildTypes() []string {
	if c == nil || len(c.BuildTypes) == 0 {
		return []string{"spec"}
	}
	res := make([]string, len(c.BuildTypes))
	copy(res, c.BuildTypes)
	return res
}

// EffectiveBuildEngine returns the declared build engine or defaults to "build".
func (c *ProjectConfig) EffectiveBuildEngine() string {
	if c == nil || strings.TrimSpace(c.BuildEngine) == "" {
		return "build"
	}
	return strings.ToLower(strings.TrimSpace(c.BuildEngine))
}

// EffectiveRepoTypes returns the declared repo types or defaults to ["rpm-md"].
func (c *ProjectConfig) EffectiveRepoTypes() []string {
	if c == nil || len(c.RepoTypes) == 0 {
		return []string{"rpm-md"}
	}
	res := make([]string, len(c.RepoTypes))
	copy(res, c.RepoTypes)
	return res
}

// HasBuildType reports whether the configuration allows the specified build type.
func (c *ProjectConfig) HasBuildType(bt string) bool {
	bt = strings.ToLower(strings.TrimSpace(bt))
	for _, t := range c.EffectiveBuildTypes() {
		if strings.ToLower(strings.TrimSpace(t)) == bt {
			return true
		}
	}
	return false
}

// HasRepoType reports whether the configuration contains the specified repo publication type.
func (c *ProjectConfig) HasRepoType(rt string) bool {
	rt = strings.ToLower(strings.TrimSpace(rt))
	for _, t := range c.EffectiveRepoTypes() {
		if strings.ToLower(strings.TrimSpace(t)) == rt {
			return true
		}
	}
	return false
}

// Clone creates a deep copy of ProjectConfig.
func (c *ProjectConfig) Clone() *ProjectConfig {
	if c == nil {
		return nil
	}
	cloned := *c
	cloned.BuildTypes = cloneSlice(c.BuildTypes)
	cloned.RepoTypes = cloneSlice(c.RepoTypes)
	cloned.Prefer = cloneSlice(c.Prefer)
	cloned.Ignore = cloneSlice(c.Ignore)
	cloned.Preinstall = cloneSlice(c.Preinstall)
	cloned.VMinstall = cloneSlice(c.VMinstall)
	cloned.Order = cloneSlice(c.Order)
	cloned.BuildFlags = cloneSlice(c.BuildFlags)
	cloned.PublishFlags = cloneSlice(c.PublishFlags)
	cloned.Substitute = cloneSubstituteMap(c.Substitute)
	cloned.Conflict = cloneConflicts(c.Conflict)
	cloned.Optflags = cloneStringMap(c.Optflags)
	return &cloned
}

// Merge returns a new ProjectConfig combining base (c) with override.
// When override has FromScratch set to true, upstream base configuration is discarded.
func (c *ProjectConfig) Merge(override *ProjectConfig) *ProjectConfig {
	if c == nil && override == nil {
		return &ProjectConfig{}
	}
	if c == nil {
		return override.Clone()
	}
	if override == nil {
		return c.Clone()
	}
	if override.FromScratch {
		return override.Clone()
	}

	merged := c.Clone()
	if len(override.BuildTypes) > 0 {
		merged.BuildTypes = cloneSlice(override.BuildTypes)
	}
	if override.BuildEngine != "" {
		merged.BuildEngine = override.BuildEngine
	}
	if len(override.RepoTypes) > 0 {
		merged.RepoTypes = cloneSlice(override.RepoTypes)
	}

	merged.Prefer = mergeSlices(merged.Prefer, override.Prefer)
	merged.Ignore = mergeSlices(merged.Ignore, override.Ignore)
	merged.Preinstall = mergeSlices(merged.Preinstall, override.Preinstall)
	merged.VMinstall = mergeSlices(merged.VMinstall, override.VMinstall)
	merged.Order = mergeSlices(merged.Order, override.Order)
	merged.BuildFlags = mergeSlices(merged.BuildFlags, override.BuildFlags)
	merged.PublishFlags = mergeSlices(merged.PublishFlags, override.PublishFlags)

	merged.Substitute = mergeSubstituteMaps(merged.Substitute, override.Substitute)
	merged.Conflict = mergeConflicts(merged.Conflict, override.Conflict)
	merged.Optflags = mergeStringMaps(merged.Optflags, override.Optflags)

	if override.MacrosFile != "" {
		merged.MacrosFile = override.MacrosFile
	}
	if override.Macros != "" {
		if merged.Macros == "" {
			merged.Macros = override.Macros
		} else {
			merged.Macros = merged.Macros + "\n" + override.Macros
		}
	}
	return merged
}

// EffectiveTargetConfig returns the merged configuration for a target, combining
// project-level defaults with any target-specific overrides.
func (prj *ProjectManifest) EffectiveTargetConfig(targetName string) *ProjectConfig {
	tgt, ok := prj.Targets[targetName]
	if !ok {
		if prj.Config != nil {
			return prj.Config.Clone()
		}
		return &ProjectConfig{}
	}
	return prj.Config.Merge(tgt.Config)
}

func cloneSlice(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	res := make([]string, len(s))
	copy(res, s)
	return res
}

func cloneStringMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	res := make(map[string]string, len(m))
	for k, v := range m {
		res[k] = v
	}
	return res
}

func cloneSubstituteMap(m map[string][]string) map[string][]string {
	if len(m) == 0 {
		return nil
	}
	res := make(map[string][]string, len(m))
	for k, v := range m {
		res[k] = cloneSlice(v)
	}
	return res
}

func cloneConflicts(c [][]string) [][]string {
	if len(c) == 0 {
		return nil
	}
	res := make([][]string, len(c))
	for i, inner := range c {
		res[i] = cloneSlice(inner)
	}
	return res
}

func mergeSlices(base, override []string) []string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(base)+len(override))
	var result []string
	for _, item := range base {
		if _, exists := seen[item]; !exists {
			seen[item] = struct{}{}
			result = append(result, item)
		}
	}
	for _, item := range override {
		if _, exists := seen[item]; !exists {
			seen[item] = struct{}{}
			result = append(result, item)
		}
	}
	return result
}

func mergeStringMaps(base, override map[string]string) map[string]string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	res := make(map[string]string, len(base)+len(override))
	for k, v := range base {
		res[k] = v
	}
	for k, v := range override {
		res[k] = v
	}
	return res
}

func mergeSubstituteMaps(base, override map[string][]string) map[string][]string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	res := make(map[string][]string, len(base)+len(override))
	for k, v := range base {
		res[k] = cloneSlice(v)
	}
	for k, v := range override {
		res[k] = cloneSlice(v)
	}
	return res
}

func mergeConflicts(base, override [][]string) [][]string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	res := make([][]string, 0, len(base)+len(override))
	res = append(res, cloneConflicts(base)...)
	res = append(res, cloneConflicts(override)...)
	return res
}

type DependencySource struct {
	Project string `yaml:"project,omitempty"`
	Target  string `yaml:"target,omitempty"`
	URL     string `yaml:"url,omitempty"`
	Type    string `yaml:"type,omitempty"`
}

type TargetFlags struct {
	Build     bool   `yaml:"build"`
	Publish   bool   `yaml:"publish"`
	Debuginfo bool   `yaml:"debuginfo"`
	Rebuild   string `yaml:"rebuild,omitempty"` // "transitive" (default), "direct", "local"
	Block     string `yaml:"block,omitempty"`   // "all" (default), "local", "never"
}

// EffectiveRebuild returns the normalized rebuild policy.
func (f TargetFlags) EffectiveRebuild() string {
	switch strings.ToLower(strings.TrimSpace(f.Rebuild)) {
	case "direct":
		return "direct"
	case "local":
		return "local"
	default:
		return "transitive"
	}
}

// EffectiveBlock returns the normalized dependency blocking policy.
func (f TargetFlags) EffectiveBlock() string {
	switch strings.ToLower(strings.TrimSpace(f.Block)) {
	case "local":
		return "local"
	case "never":
		return "never"
	default:
		return "all"
	}
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
