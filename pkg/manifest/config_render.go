package manifest

import (
	"fmt"
	"strings"
)

// RenderBuildConfig serializes a manifest.ProjectConfig into legacy obs-build .build.config format.
//
// Deprecated: use buildconfig.Render and buildconfig.Layer (ADR 0010). This
// renderer ignores Defines, Release and RawConfig and remains only for the
// legacy scheduler and worker paths until WP4.
func RenderBuildConfig(cfg *ProjectConfig) string {
	if cfg == nil {
		return ""
	}
	var b strings.Builder
	renderEngineAndPackages(&b, cfg)
	renderSolverDirectives(&b, cfg)
	renderEnvironmentAndMacros(&b, cfg)
	return b.String()
}

func renderEngineAndPackages(b *strings.Builder, cfg *ProjectConfig) {
	if len(cfg.Preinstall) > 0 {
		fmt.Fprintf(b, "Preinstall: %s\n", strings.Join(cfg.Preinstall, " "))
	}
	if len(cfg.VMinstall) > 0 {
		fmt.Fprintf(b, "VMinstall: %s\n", strings.Join(cfg.VMinstall, " "))
	}
	for _, s := range cfg.Support {
		fmt.Fprintf(b, "Support: %s\n", s)
	}
	for _, r := range cfg.Required {
		fmt.Fprintf(b, "Required: %s\n", r)
	}
	if len(cfg.Order) > 0 {
		fmt.Fprintf(b, "Order: %s\n", strings.Join(cfg.Order, " "))
	}
	if cfg.BuildEngine != "" {
		fmt.Fprintf(b, "BuildEngine: %s\n", cfg.BuildEngine)
	}
	if len(cfg.BuildTypes) > 0 {
		fmt.Fprintf(b, "Type: %s\n", cfg.BuildTypes[0])
	}
	if len(cfg.RepoTypes) > 0 {
		fmt.Fprintf(b, "Repotype: %s\n", strings.Join(cfg.RepoTypes, " "))
	}
}

func renderSolverDirectives(b *strings.Builder, cfg *ProjectConfig) {
	for _, p := range cfg.Prefer {
		fmt.Fprintf(b, "Prefer: %s\n", p)
	}
	for _, ign := range cfg.Ignore {
		fmt.Fprintf(b, "Ignore: %s\n", ign)
	}
	for k, v := range cfg.Substitute {
		fmt.Fprintf(b, "Substitute: %s %s\n", k, strings.Join(v, " "))
	}
	for _, pair := range cfg.Conflict {
		if len(pair) > 0 {
			fmt.Fprintf(b, "Conflict: %s\n", strings.Join(pair, " "))
		}
	}
	for file, pkgs := range cfg.FileProvides {
		fmt.Fprintf(b, "FileProvides: %s %s\n", file, strings.Join(pkgs, " "))
	}
}

func renderEnvironmentAndMacros(b *strings.Builder, cfg *ProjectConfig) {
	for arch, opt := range cfg.Optflags {
		fmt.Fprintf(b, "Optflags: %s %s\n", arch, opt)
	}
	for _, f := range cfg.BuildFlags {
		fmt.Fprintf(b, "buildflags: %s\n", f)
	}
	for _, f := range cfg.PublishFlags {
		fmt.Fprintf(b, "publishflags: %s\n", f)
	}
	for _, ef := range cfg.ExpandFlags {
		fmt.Fprintf(b, "ExpandFlags: %s\n", ef)
	}
	for _, exp := range cfg.ExportFilter {
		fmt.Fprintf(b, "ExportFilter: %s\n", exp)
	}
	for _, c := range cfg.Constraints {
		fmt.Fprintf(b, "Constraint: %s\n", c)
	}
	if cfg.Macros != "" {
		if cleaned := cleanMacroBody(cfg.Macros); cleaned != "" {
			fmt.Fprintf(b, "Macros:\n%s\n:macros\n", cleaned)
		}
	}
}

func cleanMacroBody(m string) string {
	lines := strings.Split(m, "\n")
	var result []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if lower == "macros:" || lower == ":macros" {
			continue
		}
		result = append(result, line)
	}
	return strings.TrimSpace(strings.Join(result, "\n"))
}
