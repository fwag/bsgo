package solv

import (
	"regexp"
	"sort"
	"strings"
)

var substVerRe = regexp.MustCompile(`(?s)\s*[<=>].*$`)

// doSubst ports Build::do_subst: expand Substitute entries, depth first.
func doSubst(subst map[string][]string, deps []string) []string {
	var res []string
	done := map[string]bool{}
	queue := append([]string(nil), deps...)
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		if done[d] {
			continue
		}
		ds := substVerRe.ReplaceAllString(d, "")
		repl, ok := subst[ds]
		repl = cleanSubst(repl)
		if ok {
			queue = append(append([]string(nil), repl...), queue...)
			if containsStr(repl, ds) {
				res = append(res, d)
			}
		} else {
			res = append(res, d)
		}
		done[d] = true
	}
	return res
}

// cleanSubst unifies a Substitute value and strips the trailing "=" markers
// (version passthrough) as Build::read_config does.
func cleanSubst(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range unifyStrings(in) {
		out = append(out, strings.TrimSuffix(s, "="))
	}
	return out
}

func filterOut(in []string, drop func(string) bool) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !drop(s) {
			out = append(out, s)
		}
	}
	return out
}

func toSet(in []string) map[string]bool {
	m := make(map[string]bool, len(in))
	for _, s := range in {
		m[s] = true
	}
	return m
}

// expandPreinstalls ports Build::expandpreinstalls and returns a config whose
// Preinstall and VMInstall are expanded. cfg is not modified.
func (p *Pool) expandPreinstalls(cfg *ExpandConfig) (*ExpandConfig, error) {
	if !hasFlag(cfg, "preinstallexpand") {
		return cfg, nil
	}
	par := newParams(cfg)
	var pre, vm []string
	if len(cfg.Preinstall) > 0 {
		r, err := p.expandLocked(par, cfg.Preinstall)
		if err != nil {
			return nil, prefixError("preinstalls: ", err)
		}
		pre = sortedNames(r)
	}
	if len(cfg.VMInstall) > 0 {
		preSet := toSet(pre)
		vmx := map[string]bool{}
		for _, v := range cfg.VMInstall {
			vmx["-"+v] = true
		}
		args := append([]string(nil), pre...)
		args = append(args, filterOut(cfg.Preinstall, func(s string) bool { return !strings.HasPrefix(s, "-") || vmx[s] })...)
		args = append(args, cfg.VMInstall...)
		r, err := p.expandLocked(par, args)
		if err != nil {
			return nil, prefixError("vminstalls: ", err)
		}
		vm = sortedNames(&ExpansionResult{Packages: filterPkgs(r.Packages, func(n string) bool { return !preSet[n] })})
	}
	out := *cfg
	out.Preinstall = pre
	out.VMInstall = vm
	out.ExpandFlags = filterOut(cfg.ExpandFlags, func(f string) bool { return f == "preinstallexpand" })
	return &out, nil
}

func prefixError(prefix string, err error) error {
	if ee, ok := err.(*ExpandError); ok && len(ee.Messages) > 0 {
		return &ExpandError{Messages: []string{prefix + ee.Messages[0]}}
	}
	return err
}

func filterPkgs(pkgs []ResolvedPackage, keep func(string) bool) []ResolvedPackage {
	var out []ResolvedPackage
	for _, p := range pkgs {
		if keep(p.Name) {
			out = append(out, p)
		}
	}
	return out
}

func sortedNames(r *ExpansionResult) []string {
	names := r.Names()
	sort.Strings(names)
	return names
}

// applyKeep ports the Keep/subpacks handling of get_build and get_deps. It
// returns the negative deps to add and the (possibly filtered) extra list.
func applyKeep(cfg *ExpandConfig, subpacks, deps, extra []string) (ndeps, newExtra []string) {
	if len(cfg.Keep) > 0 {
		keep := toSet(append(append(append([]string(nil), deps...), cfg.Keep...), cfg.Preinstall...))
		for _, s := range subpacks {
			if !keep[s] {
				ndeps = append(ndeps, "-"+s)
			}
		}
		return ndeps, extra
	}
	sp := toSet(subpacks)
	return nil, filterOut(extra, func(s string) bool { return sp[s] })
}

func negatives(deps []string) []string {
	return filterOut(deps, func(s string) bool { return !strings.HasPrefix(s, "-") })
}

// splitDirectDepsEnd splits deps at the --directdepsend-- marker.
func splitDirectDepsEnd(deps, ndeps []string) (head, dds []string) {
	if !containsStr(ndeps, "--directdepsend--") {
		return deps, nil
	}
	for i, d := range deps {
		if d == "--directdepsend--" {
			return deps[:i], filterOut(deps[i+1:], func(s string) bool { return strings.HasPrefix(s, "-") })
		}
	}
	return deps, nil
}

func (p *Pool) getBuildLocked(cfg *ExpandConfig, subpacks, deps []string) (*ExpansionResult, error) {
	if cfg == nil {
		cfg = &ExpandConfig{}
	}
	cfg, err := p.expandPreinstalls(cfg)
	if err != nil {
		return nil, err
	}
	deps = append([]string(nil), deps...)
	deps = append(deps, cfg.Substitute["build-packages:"+cfg.BuildType]...)
	noBase := cfg.BuildType == "docker" || cfg.BuildType == "mkosi"
	ndeps := negatives(deps)
	var dds []string
	deps, dds = splitDirectDepsEnd(deps, ndeps)
	extra := append(append([]string(nil), cfg.Required...), cfg.Support...)
	keepN, extra := applyKeep(cfg, subpacks, deps, extra)
	ndeps = append(ndeps, keepN...)
	nset := toSet(ndeps)
	deps = filterOut(deps, func(s string) bool { return nset[s] })
	if !noBase {
		deps = append(append(deps, cfg.Preinstall...), extra...)
		deps = filterOut(deps, func(s string) bool { return nset["-"+s] })
	}
	deps = doSubst(cfg.Substitute, deps)
	deps = filterOut(deps, func(s string) bool { return nset["-"+s] })
	if len(dds) > 0 {
		dds = filterOut(doSubst(cfg.Substitute, dds), func(s string) bool { return nset["-"+s] })
		if len(dds) > 0 {
			dds = append([]string{"--directdepsend--"}, dds...)
		}
	}
	args := append(append(append([]string(nil), deps...), ndeps...), dds...)
	return p.expandLocked(newParams(cfg), args)
}

func (p *Pool) getDepsLocked(cfg *ExpandConfig, subpacks, deps []string) (*ExpansionResult, error) {
	if cfg == nil {
		cfg = &ExpandConfig{}
	}
	cfg, err := p.expandPreinstalls(cfg)
	if err != nil {
		return nil, err
	}
	ndeps := negatives(deps)
	keepN, extra := applyKeep(cfg, subpacks, deps, append([]string(nil), cfg.Required...))
	ndeps = append(ndeps, keepN...)
	nset := toSet(ndeps)
	deps = filterOut(deps, func(s string) bool { return nset[s] })
	deps = append(append([]string(nil), deps...), extra...)
	deps = filterOut(deps, func(s string) bool { return nset["-"+s] })
	deps = doSubst(cfg.Substitute, deps)
	deps = filterOut(deps, func(s string) bool { return nset["-"+s] })
	bdeps := toSet(append(append([]string(nil), cfg.Preinstall...), cfg.Support...))
	for _, d := range deps {
		delete(bdeps, d)
	}
	for _, r := range cfg.IgnoreRebuild {
		ndeps = append(ndeps, "-"+r)
	}
	if hasFlag(cfg, "ignorerebuildforsupport") {
		for _, r := range append(append([]string(nil), cfg.Preinstall...), cfg.Support...) {
			ndeps = append(ndeps, "-"+r)
		}
	}
	res, err := p.expandLocked(newParams(cfg), append(deps, ndeps...))
	if err != nil {
		return nil, err
	}
	res.Packages = filterPkgs(res.Packages, func(n string) bool { return !bdeps[n] })
	return res, nil
}
