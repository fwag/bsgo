package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
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
	SchemaVersion   string                    `yaml:"schema_version"`
	Project         ProjectInfo               `yaml:"project"`
	PublicConfig    *ProjectConfig            `yaml:"public_config,omitempty"`
	Config          *ProjectConfig            `yaml:"config,omitempty"` // Alias for PublicConfig
	ProtectedConfig *ProjectConfig            `yaml:"protected_config,omitempty"`
	Protected       *ProjectConfig            `yaml:"protected,omitempty"`        // Alias for ProtectedConfig
	NamespaceScoped *ProjectConfig            `yaml:"namespace_scoped,omitempty"` // Alias for ProtectedConfig
	PrivateConfig   *ProjectConfig            `yaml:"private_config,omitempty"`
	Private         *ProjectConfig            `yaml:"private,omitempty"` // Alias for PrivateConfig
	ArchConfig      map[string]*ProjectConfig `yaml:"arch_config,omitempty"`
	Targets         map[string]BuildTarget    `yaml:"targets"`
}

// GetPublicConfig returns the public project configuration.
func (prj *ProjectManifest) GetPublicConfig() *ProjectConfig {
	if prj.PublicConfig != nil {
		return prj.PublicConfig
	}
	return prj.Config
}

// GetProtectedConfig returns the namespace-scoped / protected configuration.
func (prj *ProjectManifest) GetProtectedConfig() *ProjectConfig {
	if prj.ProtectedConfig != nil {
		return prj.ProtectedConfig
	}
	if prj.Protected != nil {
		return prj.Protected
	}
	return prj.NamespaceScoped
}

// GetPrivateConfig returns the project-private configuration.
func (prj *ProjectManifest) GetPrivateConfig() *ProjectConfig {
	if prj.PrivateConfig != nil {
		return prj.PrivateConfig
	}
	return prj.Private
}

type ProjectInfo struct {
	Name        string   `yaml:"name"`
	Title       string   `yaml:"title,omitempty"`
	Description string   `yaml:"description,omitempty"`
	Namespace   string   `yaml:"namespace,omitempty"`
	SCMSync     string   `yaml:"scm_sync,omitempty"`
	Maintainers []string `yaml:"maintainers,omitempty"`
}

type BuildTarget struct {
	Architectures []string                  `yaml:"architectures"`
	Dependencies  []DependencySource        `yaml:"dependencies"`
	Config        *ProjectConfig            `yaml:"config,omitempty"`
	ArchConfig    map[string]*ProjectConfig `yaml:"arch_config,omitempty"`
	Flags         TargetFlags               `yaml:"flags,omitempty"`
}

// ProjectConfig defines project-wide or target-specific build, solver, and publication settings.
type ProjectConfig struct {
	// Defines holds top-level %define lines in source order (ADR 0010). The
	// parser evaluates them, so directives below may reference them.
	Defines      Defines             `yaml:"defines,omitempty"`
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
	Support      []string            `yaml:"support,omitempty"`
	Required     []string            `yaml:"required,omitempty"`
	ExpandFlags  []string            `yaml:"expand_flags,omitempty"`
	ExportFilter []string            `yaml:"export_filter,omitempty"`
	Constraints  []string            `yaml:"constraints,omitempty"`
	FileProvides map[string][]string `yaml:"file_provides,omitempty"`
	Order        []string            `yaml:"order,omitempty"`
	BuildFlags   []string            `yaml:"build_flags,omitempty"`
	PublishFlags []string            `yaml:"publish_flags,omitempty"`
	Optflags     map[string]string   `yaml:"optflags,omitempty"`
	// Release is the Release: directive value, for example "<CI_CNT>.<B_CNT>".
	Release string `yaml:"release,omitempty"`
	// Macros is the opaque rpm Macros: body, evaluated by rpm at build time.
	Macros string `yaml:"macros,omitempty"`
	// RawConfig is a verbatim prjconf fragment for constructs without a
	// structured field. It is rendered after the structured directives of the
	// same scope and before Macros.
	RawConfig string `yaml:"raw_config,omitempty"`
	// MacrosFile is deprecated (ADR 0010); Render rejects it.
	MacrosFile string `yaml:"macros_file,omitempty"`
}

// Define is one top-level %define of a prjconf.
type Define struct {
	Name  string
	Value string
}

// Defines is an ordered list of %define entries. In YAML it is a mapping so
// that it reads naturally, and the mapping order is preserved on load and
// save (ordering matters for %define).
type Defines []Define

// UnmarshalYAML decodes a mapping node keeping the key order.
func (d *Defines) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: defines must be a mapping of name to value", node.Line)
	}
	out := make(Defines, 0, len(node.Content)/2)
	seen := make(map[string]bool, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		k, v := node.Content[i], node.Content[i+1]
		if v.Kind != yaml.ScalarNode {
			return fmt.Errorf("line %d: define %q must have a scalar value", v.Line, k.Value)
		}
		if seen[k.Value] {
			return fmt.Errorf("line %d: duplicate define %q", k.Line, k.Value)
		}
		seen[k.Value] = true
		out = append(out, Define{Name: k.Value, Value: v.Value})
	}
	*d = out
	return nil
}

// MarshalYAML encodes the list as an ordered mapping.
func (d Defines) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, e := range d {
		n.Content = append(n.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: e.Name},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: e.Value})
	}
	return n, nil
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
	cloned.Defines = slices.Clone(c.Defines)
	cloned.BuildTypes = cloneSlice(c.BuildTypes)
	cloned.RepoTypes = cloneSlice(c.RepoTypes)
	cloned.Prefer = cloneSlice(c.Prefer)
	cloned.Ignore = cloneSlice(c.Ignore)
	cloned.Preinstall = cloneSlice(c.Preinstall)
	cloned.VMinstall = cloneSlice(c.VMinstall)
	cloned.Support = cloneSlice(c.Support)
	cloned.Required = cloneSlice(c.Required)
	cloned.ExpandFlags = cloneSlice(c.ExpandFlags)
	cloned.ExportFilter = cloneSlice(c.ExportFilter)
	cloned.Constraints = cloneSlice(c.Constraints)
	cloned.FileProvides = cloneSubstituteMap(c.FileProvides)
	cloned.Order = cloneSlice(c.Order)
	cloned.BuildFlags = cloneSlice(c.BuildFlags)
	cloned.PublishFlags = cloneSlice(c.PublishFlags)
	cloned.Substitute = cloneSubstituteMap(c.Substitute)
	cloned.Conflict = cloneConflicts(c.Conflict)
	cloned.Optflags = cloneStringMap(c.Optflags)
	return &cloned
}

// Merge returns a new ProjectConfig combining base (c) with override.
//
// Legacy path: ADR 0010 layers configuration textually (buildconfig.Layer), not
// by merging structs. Merge ignores Defines, Release and RawConfig. It remains
// only for the scheduler's legacy ResolveTargetConfig until WP4 replaces it.
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

	merged.Prefer = mergePreferSlices(merged.Prefer, override.Prefer)
	merged.Ignore = mergeSlices(merged.Ignore, override.Ignore)
	merged.Preinstall = mergeSlices(merged.Preinstall, override.Preinstall)
	merged.VMinstall = mergeSlices(merged.VMinstall, override.VMinstall)
	merged.Support = mergeSlices(merged.Support, override.Support)
	merged.Required = mergeSlices(merged.Required, override.Required)
	merged.ExpandFlags = mergeSlices(merged.ExpandFlags, override.ExpandFlags)
	merged.ExportFilter = mergeSlices(merged.ExportFilter, override.ExportFilter)
	merged.Constraints = mergeSlices(merged.Constraints, override.Constraints)
	merged.FileProvides = mergeSubstituteMaps(merged.FileProvides, override.FileProvides)
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

// ProjectNamespace returns the explicit or inferred namespace of the project (e.g. "SUSE:SLFO").
func (prj *ProjectManifest) ProjectNamespace() string {
	if prj.Project.Namespace != "" {
		return prj.Project.Namespace
	}
	parts := strings.Split(prj.Project.Name, ":")
	if len(parts) >= 2 {
		return strings.Join(parts[:2], ":")
	}
	return prj.Project.Name
}

// IsInNamespace reports whether another project belongs to this project's namespace hierarchy.
func (prj *ProjectManifest) IsInNamespace(otherProject string) bool {
	if otherProject == "" {
		return false
	}
	if otherProject == prj.Project.Name {
		return true
	}
	if strings.HasPrefix(otherProject, prj.Project.Name+":") {
		return true
	}
	if strings.HasPrefix(prj.Project.Name, otherProject+":") {
		return true
	}
	ns := prj.ProjectNamespace()
	return ns != "" && (otherProject == ns || strings.HasPrefix(otherProject, ns+":"))
}

// EffectiveConfigForConsumer returns the resolved target configuration for a specific consumer project,
// enforcing public, namespace-scoped, and private boundaries.
func (prj *ProjectManifest) EffectiveConfigForConsumer(targetName, consumerProject string) *ProjectConfig {
	tgt, hasTgt := prj.Targets[targetName]
	if hasTgt && tgt.Config != nil && tgt.Config.FromScratch {
		return tgt.Config.Clone()
	}

	pubCfg := prj.GetPublicConfig()
	var base *ProjectConfig
	if pubCfg != nil {
		base = pubCfg.Clone()
	} else {
		base = &ProjectConfig{}
	}

	// Apply protected (namespace-scoped) configuration if consumer is in the same namespace
	protCfg := prj.GetProtectedConfig()
	if protCfg != nil && (consumerProject == "" || prj.IsInNamespace(consumerProject)) {
		base = base.Merge(protCfg)
	}

	// Apply private configuration only if consumer is this exact project
	privCfg := prj.GetPrivateConfig()
	if privCfg != nil && (consumerProject == "" || consumerProject == prj.Project.Name) {
		base = base.Merge(privCfg)
	}

	// Merge target-specific overrides
	if hasTgt && tgt.Config != nil {
		base = base.Merge(tgt.Config)
	}

	return base
}

// EffectiveConfigForArch returns the resolved target configuration for a specific architecture and consumer project,
// enforcing public, namespace-scoped, and private boundaries, as well as arch-specific overrides.
func (prj *ProjectManifest) EffectiveConfigForArch(targetName, arch, consumerProject string) *ProjectConfig {
	base := prj.EffectiveConfigForConsumer(targetName, consumerProject)

	// Merge project-level arch config if present
	if arch != "" && prj.ArchConfig != nil {
		if archCfg, ok := prj.ArchConfig[arch]; ok && archCfg != nil {
			base = base.Merge(archCfg)
		}
	}

	// Merge target-level arch config if present
	if arch != "" {
		if tgt, ok := prj.Targets[targetName]; ok && tgt.ArchConfig != nil {
			if tgtArchCfg, ok := tgt.ArchConfig[arch]; ok && tgtArchCfg != nil {
				base = base.Merge(tgtArchCfg)
			}
		}
	}

	return base
}

// EffectiveTargetConfig returns the resolved configuration for a target within this project itself.
func (prj *ProjectManifest) EffectiveTargetConfig(targetName string) *ProjectConfig {
	return prj.EffectiveConfigForArch(targetName, "", prj.Project.Name)
}

// EffectiveTargetArchConfig returns the resolved configuration for a target and architecture within this project itself.
func (prj *ProjectManifest) EffectiveTargetArchConfig(targetName, arch string) *ProjectConfig {
	return prj.EffectiveConfigForArch(targetName, arch, prj.Project.Name)
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

func cleanItemPrefix(item string) string {
	return strings.TrimPrefix(strings.TrimPrefix(item, "-"), "!")
}

func findCancelledItems(base, override []string) map[string]bool {
	basePos := make(map[string]bool, len(base))
	for _, item := range base {
		if !strings.HasPrefix(item, "-") && !strings.HasPrefix(item, "!") {
			basePos[item] = true
		}
	}
	cancelled := make(map[string]bool)
	for _, item := range override {
		if strings.HasPrefix(item, "-") || strings.HasPrefix(item, "!") {
			target := cleanItemPrefix(item)
			if basePos[target] {
				cancelled[target] = true
			}
		}
	}
	return cancelled
}

func appendPreferItems(result []string, items []string, cancelled map[string]bool, seen map[string]struct{}) []string {
	for _, item := range items {
		target := cleanItemPrefix(item)
		if cancelled[target] {
			continue
		}
		if _, exists := seen[item]; !exists {
			seen[item] = struct{}{}
			result = append(result, item)
		}
	}
	return result
}

func mergePreferSlices(base, override []string) []string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	cancelled := findCancelledItems(base, override)
	seen := make(map[string]struct{}, len(base)+len(override))
	var result []string
	result = appendPreferItems(result, base, cancelled, seen)
	return appendPreferItems(result, override, cancelled, seen)
}

func mergeSlices(base, override []string) []string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	removals := make(map[string]struct{})
	for _, item := range base {
		if strings.HasPrefix(item, "-") || strings.HasPrefix(item, "!") {
			removals[strings.TrimPrefix(strings.TrimPrefix(item, "-"), "!")] = struct{}{}
		}
	}
	for _, item := range override {
		if strings.HasPrefix(item, "-") || strings.HasPrefix(item, "!") {
			removals[strings.TrimPrefix(strings.TrimPrefix(item, "-"), "!")] = struct{}{}
		}
	}

	seen := make(map[string]struct{}, len(base)+len(override))
	var result []string
	for _, item := range base {
		clean := strings.TrimPrefix(strings.TrimPrefix(item, "-"), "!")
		if _, removed := removals[clean]; removed {
			continue
		}
		if _, exists := seen[item]; !exists {
			seen[item] = struct{}{}
			result = append(result, item)
		}
	}
	for _, item := range override {
		clean := strings.TrimPrefix(strings.TrimPrefix(item, "-"), "!")
		if _, removed := removals[clean]; removed {
			continue
		}
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
	// Name is the stable "project/repository" name of a url entry, used as
	// _project/_repository when layering configs (for example
	// "SUSE:SLFO:1.1/standard").
	Name string `yaml:"name,omitempty"`
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
	// Recipe is optional. When empty the recipe is auto-detected from the
	// files at the pinned commit (see recipe.Expand and recipe.FindRecipe).
	Recipe string `yaml:"recipe,omitempty"`
}

type PackageBuild struct {
	Enabled       bool            `yaml:"enabled"`
	Targets       map[string]bool `yaml:"targets,omitempty"`
	Architectures ArchFilter      `yaml:"architectures,omitempty"`
	Requires      []string        `yaml:"requires,omitempty"`
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

	normalizeAliases(&prj)

	return &prj, nil
}

// normalizeAliases moves values loaded through the deprecated alias keys into
// the canonical fields and clears the aliases, so they are never written back.
func normalizeAliases(prj *ProjectManifest) {
	prj.PublicConfig = prj.GetPublicConfig()
	prj.ProtectedConfig = prj.GetProtectedConfig()
	prj.PrivateConfig = prj.GetPrivateConfig()
	prj.Config, prj.Protected, prj.NamespaceScoped, prj.Private = nil, nil, nil, nil
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
