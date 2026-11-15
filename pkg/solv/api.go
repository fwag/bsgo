package solv

import (
	"fmt"
	"sort"
	"strings"
)

// ExpandConfig carries the parts of an OBS project configuration that the
// dependency expander reads (Build.pm read_config). Entries use the OBS prjconf
// syntax: Prefer "-foo" is a negative prefer, "a:b" scopes it to dependencies
// of package a; Ignore "a:b" ignores b only for a; Conflict "a:b" makes a and b
// mutually exclusive, a bare "a" forbids a entirely.
type ExpandConfig struct {
	Prefer       []string
	Ignore       []string
	Conflict     []string
	ExpandFlags  []string // "keepfilerequires", "ignoreconflicts", "dorecommends", "preinstallexpand", "k:v"
	Substitute   map[string][]string
	FileProvides map[string][]string

	Preinstall    []string
	VMInstall     []string
	Required      []string
	Support       []string
	Keep          []string
	IgnoreRebuild []string
	BuildType     string // recipe type: spec, kiwi, docker, ...
}

// ResolvedPackage is one package of an expansion.
type ResolvedPackage struct {
	Name string
	EVR  string
	Arch string
	// SHA256 is the sha256 of the RPM file as recorded in the repository
	// metadata. It is empty when the repository carries no sha256 checksum.
	SHA256 string
	// Location is the package location of rpm-md repositories (the href of
	// primary.xml, relative to the repository base URL); empty for repositories
	// without that information.
	Location string
	// Repo is the index of the repository (path position, 0 = highest priority).
	Repo int
	// Why records the first reason the package was selected.
	Why string
}

// ExpansionResult is an ordered package list: providers come before the
// packages requiring them (ties and cycles are broken by name).
type ExpansionResult struct {
	Packages []ResolvedPackage
}

// Names returns the package names in result order.
func (r *ExpansionResult) Names() []string {
	out := make([]string, len(r.Packages))
	for i, p := range r.Packages {
		out[i] = p.Name
	}
	return out
}

// ExpansionRequest is the legacy request shape. When Config is nil the
// request is expanded with just Prefer, Ignore and FileProvides; otherwise it
// follows Build::get_build including Substitute and the Required, Support and
// Preinstall sets of the config.
type ExpansionRequest struct {
	BuildRequires []string
	Subpacks      []string
	Config        *ExpandConfig
	Prefer        []string
	Ignore        []string
	FileProvides  map[string][]string
	Architecture  string
	RPMMDRepos    map[string][]byte
}

// NoProviderError reports the first unmet requirement of an ExpandError.
type NoProviderError struct {
	Requirement string
}

func (e *NoProviderError) Error() string {
	return fmt.Sprintf("nothing provides %s", e.Requirement)
}

// AmbiguityError reports the first unresolved choice of an ExpandError.
type AmbiguityError struct {
	Requirement string
}

func (e *AmbiguityError) Error() string {
	return fmt.Sprintf("have choice for %s", e.Requirement)
}

// ExpandError is an unresolvable expansion. Messages use the exact OBS texts.
type ExpandError struct {
	Messages []string
}

func (e *ExpandError) Error() string {
	return strings.Join(e.Messages, ", ")
}

// As lets callers extract the first problem as NoProviderError or AmbiguityError.
func (e *ExpandError) As(target any) bool {
	for _, m := range e.Messages {
		switch t := target.(type) {
		case **NoProviderError:
			if req, ok := strings.CutPrefix(m, "nothing provides "); ok {
				*t = &NoProviderError{Requirement: strings.SplitN(req, " needed by ", 2)[0]}
				return true
			}
		case **AmbiguityError:
			if req, ok := strings.CutPrefix(m, "have choice for "); ok {
				*t = &AmbiguityError{Requirement: strings.SplitN(strings.SplitN(req, " needed by ", 2)[0], ": ", 2)[0]}
				return true
			}
		}
	}
	return false
}

func newParams(cfg *ExpandConfig) *expandParams {
	if cfg == nil {
		cfg = &ExpandConfig{}
	}
	par := &expandParams{
		prefer:       map[string]bool{},
		preferList:   cfg.Prefer,
		ignore:       map[string]bool{},
		conflict:     map[string][]string{},
		fileProvides: cfg.FileProvides,
	}
	for _, p := range cfg.Prefer {
		par.prefer[p] = true
	}
	for _, ig := range cfg.Ignore {
		addIgnore(par.ignore, ig)
	}
	for _, c := range cfg.Conflict {
		addConflict(par, c)
	}
	for _, f := range cfg.ExpandFlags {
		name, _, _ := strings.Cut(f, ":")
		switch name {
		case "keepfilerequires":
			par.keepFileReq = true
		case "ignoreconflicts":
			par.ignoreConflicts = true
		case "dorecommends":
			par.doRecommends = true
		}
	}
	return par
}

func splitScope(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ':' })
}

func addIgnore(m map[string]bool, entry string) {
	if !strings.Contains(entry, ":") {
		m[entry] = true
		return
	}
	parts := splitScope(entry)
	for _, o := range parts[min(1, len(parts)):] {
		m[parts[0]+":"+o] = true
	}
}

func addConflict(par *expandParams, entry string) {
	parts := splitScope(entry)
	if len(parts) == 0 {
		return
	}
	first, rest := parts[0], parts[1:]
	if len(rest) == 0 {
		par.conflictAll = append(par.conflictAll, first)
		return
	}
	par.conflict[first] = appendUnique(par.conflict[first], rest...)
	for _, o := range rest {
		par.conflict[o] = appendUnique(par.conflict[o], first)
	}
}

func appendUnique(dst []string, add ...string) []string {
	for _, a := range add {
		if !containsStr(dst, a) {
			dst = append(dst, a)
		}
	}
	return dst
}

func containsStr(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func unifyStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func hasFlag(cfg *ExpandConfig, name string) bool {
	for _, f := range cfg.ExpandFlags {
		if f == name || strings.HasPrefix(f, name+":") {
			return true
		}
	}
	return false
}

// expandLocked runs the expander for args (dependencies plus "-x" ignores and
// "!x" conflicts). The pool must be locked.
func (p *Pool) expandLocked(par *expandParams, args []string) (*ExpansionResult, error) {
	if err := p.prepareLocked(); err != nil {
		return nil, err
	}
	e := newExpander(p, par)
	if !e.run(args) {
		return nil, &ExpandError{Messages: e.errs}
	}
	return e.result(), nil
}

func (e *expander) result() *ExpansionResult {
	order := e.sortInstalled()
	res := &ExpansionResult{Packages: make([]ResolvedPackage, len(order))}
	for i, s := range order {
		info := e.pool.solvInfo(s)
		res.Packages[i] = ResolvedPackage{
			Name:     info.name,
			EVR:      info.evr,
			Arch:     info.arch,
			SHA256:   info.checksum,
			Location: info.location,
			Repo:     info.repo,
			Why:      e.why[s],
		}
	}
	return res
}

// Expand runs Build::Expand::expand over args with the given configuration.
func (p *Pool) Expand(cfg *ExpandConfig, args ...string) (*ExpansionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.expandLocked(newParams(cfg), args)
}

// GetBuild ports Build::get_build: the packages installed into a build
// environment for a recipe with the given build dependencies.
func (p *Pool) GetBuild(cfg *ExpandConfig, subpacks, deps []string) (*ExpansionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.getBuildLocked(cfg, subpacks, deps)
}

// GetDeps ports Build::get_deps: like GetBuild but without support packages.
func (p *Pool) GetDeps(cfg *ExpandConfig, subpacks, deps []string) (*ExpansionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.getDepsLocked(cfg, subpacks, deps)
}

// Resolve expands a legacy ExpansionRequest. Repositories passed as RPMMDRepos
// are parsed once per content and stay in the pool.
func (p *Pool) Resolve(req ExpansionRequest) (*ExpansionResult, error) {
	if err := p.loadLegacyRepos(req.RPMMDRepos); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if req.Config != nil {
		return p.getBuildLocked(req.Config, req.Subpacks, req.BuildRequires)
	}
	cfg := &ExpandConfig{Prefer: req.Prefer, Ignore: req.Ignore, FileProvides: req.FileProvides}
	return p.expandLocked(newParams(cfg), req.BuildRequires)
}

func (p *Pool) loadLegacyRepos(repos map[string][]byte) error {
	names := make([]string, 0, len(repos))
	for n := range repos {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		key := n + "\x00" + blobHash(repos[n])
		p.mu.Lock()
		_, done := p.loaded[key]
		p.mu.Unlock()
		if done {
			continue
		}
		if err := p.loadBlob(RepoBlob{Name: n, Data: repos[n]}); err != nil {
			return err
		}
		p.mu.Lock()
		p.loaded[key] = struct{}{}
		p.mu.Unlock()
	}
	return nil
}
