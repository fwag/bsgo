package buildconfig

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/openSUSE/bsgo/pkg/manifest"
)

var defineNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Render renders the Build Configuration that project prj contributes to a
// path element (repository repo) for the given Consumer Project, as prjconf
// text (ADR 0010, spec 01 "Build Configuration Semantics" item 1).
//
// Scope selection follows OBS getconfig: public_config always;
// protected_config when consumer is the project or one of its "<project>:*"
// subprojects; private_config when consumer is the project itself. Then the
// target overlay targets.<repo>.config and the arch overlays follow. Each
// scope renders defines, directives, raw_config and the Macros: block in this
// order.
//
// With a non-empty arch only the matching arch overlays are emitted. With an
// empty arch every overlay is emitted inside an "%ifarch <arch>" guard, in
// sorted arch order, so that the text can be parsed for any architecture.
// The output is deterministic.
func Render(prj *manifest.ProjectManifest, repo, arch, consumer string) (string, error) {
	if prj == nil {
		return "", errors.New("render: nil project manifest")
	}
	scopes := selectScopes(prj, repo, arch, consumer)
	var b strings.Builder
	for _, s := range scopes {
		if s.cfg.FromScratch && s.guard == "" {
			b.WriteString("FromScratch:\n")
			break
		}
	}
	for _, s := range scopes {
		text, err := RenderScope(s.cfg)
		if err != nil {
			return "", fmt.Errorf("render %s (%s): %w", prj.Project.Name, s.name, err)
		}
		if text == "" {
			continue
		}
		if s.guard != "" {
			text = "%ifarch " + s.guard + "\n" + text + "%endif\n"
		}
		b.WriteString(text)
	}
	return b.String(), nil
}

// scope is one selected configuration block. guard is the architecture an
// overlay is wrapped in, or empty when it is emitted unconditionally.
type scope struct {
	name  string
	cfg   *manifest.ProjectConfig
	guard string
}

func selectScopes(prj *manifest.ProjectManifest, repo, arch, consumer string) []scope {
	name := prj.Project.Name
	var out []scope
	add := func(n string, c *manifest.ProjectConfig) {
		if c != nil {
			out = append(out, scope{name: n, cfg: c})
		}
	}
	add("public_config", prj.GetPublicConfig())
	if consumer == name || strings.HasPrefix(consumer, name+":") {
		add("protected_config", prj.GetProtectedConfig())
	}
	if consumer == name {
		add("private_config", prj.GetPrivateConfig())
	}
	tgt, hasTarget := prj.Targets[repo]
	if hasTarget {
		add("targets."+repo+".config", tgt.Config)
	}
	out = append(out, archScopes("arch_config", prj.ArchConfig, arch)...)
	if hasTarget {
		out = append(out, archScopes("targets."+repo+".arch_config", tgt.ArchConfig, arch)...)
	}
	return out
}

func archScopes(label string, m map[string]*manifest.ProjectConfig, arch string) []scope {
	if arch != "" {
		if c := m[arch]; c != nil {
			return []scope{{name: label + "." + arch, cfg: c}}
		}
		return nil
	}
	keys := make([]string, 0, len(m))
	for k, c := range m {
		if c != nil {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := make([]scope, 0, len(keys))
	for _, k := range keys {
		out = append(out, scope{name: label + "." + k, cfg: m[k], guard: k})
	}
	return out
}

// lineWriter collects prjconf lines and remembers the first invalid value.
type lineWriter struct {
	b   strings.Builder
	err error
}

func (w *lineWriter) line(s string) {
	if w.err != nil {
		return
	}
	if strings.ContainsAny(s, "\r\n") {
		w.err = fmt.Errorf("value %q contains a line break", s)
		return
	}
	w.b.WriteString(s)
	w.b.WriteByte('\n')
}

func (w *lineWriter) directive(kw string, args ...string) {
	for _, a := range args {
		if strings.TrimSpace(a) == "" {
			w.fail(fmt.Errorf("empty value for %s", kw))
			return
		}
	}
	w.line(kw + ": " + strings.Join(args, " "))
}

func (w *lineWriter) fail(err error) {
	if w.err == nil {
		w.err = err
	}
}

func (w *lineWriter) each(kw string, items []string) {
	for _, it := range items {
		w.directive(kw, it)
	}
}

// RenderScope renders one scope (one ProjectConfig) to prjconf text: defines,
// directives, raw_config and the Macros: block, in this order. FromScratch is
// handled by Render because it must precede everything else of a path element.
func RenderScope(cfg *manifest.ProjectConfig) (string, error) {
	if cfg == nil {
		return "", nil
	}
	if cfg.MacrosFile != "" {
		return "", errors.New("macros_file is deprecated (ADR 0010); convert it to structured fields and raw_config")
	}
	w := &lineWriter{}
	renderDefines(w, cfg.Defines)
	renderScalars(w, cfg)
	renderLists(w, cfg)
	renderMaps(w, cfg)
	if w.err == nil && cfg.RawConfig != "" {
		w.b.WriteString(withNewline(cfg.RawConfig))
	}
	renderMacros(w, cfg.Macros)
	if w.err != nil {
		return "", w.err
	}
	return w.b.String(), nil
}

func withNewline(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}

func renderDefines(w *lineWriter, defs manifest.Defines) {
	for _, d := range defs {
		if !defineNameRe.MatchString(d.Name) {
			w.fail(fmt.Errorf("invalid define name %q", d.Name))
			return
		}
		if d.Value == "" {
			w.line("%define " + d.Name)
			continue
		}
		w.line("%define " + d.Name + " " + d.Value)
	}
}

func renderScalars(w *lineWriter, cfg *manifest.ProjectConfig) {
	if cfg.Release != "" {
		w.directive("Release", cfg.Release)
	}
	if len(cfg.BuildTypes) > 0 {
		// obs-build honours a single Type: value.
		w.directive("Type", cfg.BuildTypes[0])
	}
	if cfg.BuildEngine != "" {
		w.directive("BuildEngine", cfg.BuildEngine)
	}
	if len(cfg.RepoTypes) > 0 {
		w.directive("Repotype", cfg.RepoTypes...)
	}
}

func renderLists(w *lineWriter, cfg *manifest.ProjectConfig) {
	w.each("Preinstall", cfg.Preinstall)
	w.each("VMinstall", cfg.VMinstall)
	w.each("Support", cfg.Support)
	w.each("Required", cfg.Required)
	w.each("Order", cfg.Order)
	w.each("Prefer", cfg.Prefer)
	w.each("Ignore", cfg.Ignore)
	for _, row := range cfg.Conflict {
		if len(row) > 0 {
			w.directive("Conflict", row...)
		}
	}
	w.each("BuildFlags", cfg.BuildFlags)
	w.each("PublishFlags", cfg.PublishFlags)
	w.each("ExpandFlags", cfg.ExpandFlags)
	w.each("ExportFilter", cfg.ExportFilter)
	w.each("Constraint", cfg.Constraints)
}

func renderMaps(w *lineWriter, cfg *manifest.ProjectConfig) {
	for _, k := range sortedKeys(cfg.Substitute) {
		w.directive("Substitute", append([]string{k}, cfg.Substitute[k]...)...)
	}
	for _, k := range sortedKeys(cfg.FileProvides) {
		w.directive("FileProvides", append([]string{k}, cfg.FileProvides[k]...)...)
	}
	for _, k := range sortedKeys(cfg.Optflags) {
		w.directive("Optflags", k, cfg.Optflags[k])
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func renderMacros(w *lineWriter, body string) {
	if w.err != nil || strings.TrimSpace(body) == "" {
		return
	}
	for _, l := range splitLines(body) {
		if macrosOpenRe.MatchString(l) || macrosCloseRe.MatchString(l) {
			w.fail(errors.New("macros body must not contain Macros:/:Macros delimiter lines"))
			return
		}
	}
	w.b.WriteString("Macros:\n" + withNewline(body) + ":Macros\n")
}
