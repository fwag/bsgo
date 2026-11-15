package checker

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openSUSE/bsgo/pkg/solv"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// newJob ports BSSched::BuildJob::create for one unit: it expands the full
// build environment, orders the bdeps with their preinstall/vminstall/
// runscripts/notmeta flags, allocates the release counters and assembles the
// JobSpec. A non-nil verdict means the job cannot be created.
func (ps *pass) newJob(u *unit, meta, explain string) (*JobSpec, *verdict) {
	build, err := ps.pool.GetBuild(ps.exp, u.info.Subpacks, u.info.Deps)
	var ee *solv.ExpandError
	if errors.As(err, &ee) {
		return nil, &verdict{storage.StateUnresolvable, ee.Error()}
	}
	if err != nil {
		ps.fail(fmt.Errorf("expand build environment of %s: %w", u.name, err))
		return nil, &verdict{storage.StateBroken, err.Error()}
	}
	bdeps, v := ps.collectBdeps(u, build)
	if v != nil {
		return nil, v
	}
	attempt := attemptID(meta, ps.forcedAt(u))
	counters, err := ps.c.d.Store.NextBuildCounters(ps.ctx, storage.CounterKey{
		Project: ps.prpa.Project, Repo: ps.prpa.Repo, Arch: ps.prpa.Arch,
		Package: u.pkg, Flavor: u.flavor, SourceCommit: u.commit, Meta: attempt,
	})
	if err != nil {
		ps.fail(fmt.Errorf("allocate build counters of %s: %w", u.name, err))
		return nil, &verdict{storage.StateBroken, err.Error()}
	}
	return ps.jobSpec(u, meta, explain, attempt, counters, bdeps), nil
}

func (ps *pass) jobSpec(u *unit, meta, explain, attempt string, c storage.BuildCounters, bdeps []Bdep) *JobSpec {
	src := u.manifest.Source
	return &JobSpec{
		JobID:   ps.jobID(u, attempt),
		Project: ps.prpa.Project, Repo: ps.prpa.Repo, Arch: ps.prpa.Arch,
		Package: u.name, BasePackage: u.pkg, Flavor: u.flavor,
		SourceURL: src.Git, SourceCommit: u.commit, RecipeFile: u.recipeFile, Branch: src.Branch,
		BuildType: recipeType(u.recipeFile),
		Config:    ps.workerText,
		Bdeps:     bdeps,
		Archpath:  ps.archpath(),
		Release:   ps.release(c.CI, c.B),
		CICnt:     c.CI, BCnt: c.B,
		DistURL:      "bsgo://" + ps.prpa.Project + "/" + ps.prpa.Repo + "/" + u.commit + "-" + u.name,
		Meta:         meta,
		Explain:      explain,
		Priority:     50 + int32(min(u.ndepend, 10000)), // #nosec G115 -- bounded above
		LogIdleLimit: ps.logIdleLimit(),
		Resources:    u.manifest.Resources,
	}
}

func recipeType(file string) string {
	if i := strings.LastIndexByte(file, '.'); i >= 0 {
		return file[i+1:]
	}
	return ""
}

// archpath is the --arch value: the architecture, the configured host
// architecture when it differs, and noarch.
func (ps *pass) archpath() string {
	parts := []string{ps.prpa.Arch}
	if h := ps.sched.HostArch; h != "" && h != ps.prpa.Arch {
		parts = append(parts, h)
	}
	return strings.Join(append(parts, "noarch"), ":")
}

func (ps *pass) logIdleLimit() int32 {
	n, err := strconv.ParseInt(ps.sched.BuildFlagsH["logidlelimit"], 10, 32)
	if err != nil || n < 0 {
		return 0
	}
	return int32(n)
}

func (ps *pass) fail(err error) {
	if ps.err == nil {
		ps.err = err
	}
}

// collectBdeps orders the bdeps like create(): preinstalls, vminstalls, the
// expanded dependencies, then the rest of the build environment.
func (ps *pass) collectBdeps(u *unit, build *solv.ExpansionResult) ([]Bdep, *verdict) {
	byName := map[string]solv.ResolvedPackage{}
	for _, p := range build.Packages {
		byName[p.Name] = p
	}
	pre, vm := ps.sched.Preinstall, ps.sched.Vminstall
	var missing []string
	for _, n := range append(append([]string(nil), pre...), vm...) {
		if _, ok := byName[n]; ok {
			continue
		}
		if p, ok := ps.pool.PackageByName(n); ok {
			byName[n] = p
		} else {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, &verdict{storage.StateUnresolvable, "missing pre/vminstalls: " + strings.Join(unique(missing), ", ")}
	}
	order := unique(concat(pre, vm, u.edeps.Names(), build.Names()))
	flags := ps.flagSets(u)
	out := make([]Bdep, 0, len(order))
	for _, n := range order {
		b, err := ps.bdep(byName[n], flags)
		if err != nil {
			return nil, &verdict{storage.StateBroken, err.Error()}
		}
		out = append(out, b)
	}
	return out, nil
}

func concat(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

type flagSets struct {
	pre, vm, run, meta map[string]bool
}

func (ps *pass) flagSets(u *unit) flagSets {
	set := func(l []string) map[string]bool {
		m := make(map[string]bool, len(l))
		for _, s := range l {
			m[s] = true
		}
		return m
	}
	return flagSets{
		pre: set(ps.sched.Preinstall), vm: set(ps.sched.Vminstall),
		run: set(ps.sched.Runscripts), meta: set(u.edeps.Names()),
	}
}

// bdep describes one resolved package as a build dependency.
func (ps *pass) bdep(p solv.ResolvedPackage, f flagSets) (Bdep, error) {
	src, size, err := ps.source(p)
	if err != nil {
		return Bdep{}, err
	}
	epoch, version, release := splitEVR(p.EVR)
	return Bdep{
		Name: p.Name, Epoch: epoch, Version: version, Release: release, Arch: p.Arch,
		SHA256: p.SHA256, Size: size, Source: src,
		Preinstall: f.pre[p.Name], VMInstall: f.vm[p.Name], RunScripts: f.run[p.Name],
		NotMeta: !f.meta[p.Name],
	}, nil
}

// splitEVR splits "[epoch:]version-release".
func splitEVR(evr string) (epoch, version, release string) {
	if i := strings.IndexByte(evr, ':'); i >= 0 {
		epoch, evr = evr[:i], evr[i+1:]
	}
	if i := strings.LastIndexByte(evr, '-'); i >= 0 {
		return epoch, evr[:i], evr[i+1:]
	}
	return epoch, evr, ""
}

// source locates the binary of p: the S3 key in a build repository or the
// absolute URL in a DoD repository.
func (ps *pass) source(p solv.ResolvedPackage) (BdepSource, int64, error) {
	if p.SHA256 == "" {
		return BdepSource{}, 0, fmt.Errorf("no sha256 checksum for %s", p.Name)
	}
	if p.Repo < 0 || p.Repo >= len(ps.sources) {
		return BdepSource{}, 0, fmt.Errorf("package %s comes from an unknown repository", p.Name)
	}
	src := ps.sources[p.Repo]
	if src.index != nil {
		e, ok := src.index.Lookup(p.Name, p.EVR, p.Arch, p.SHA256)
		if !ok {
			return BdepSource{}, 0, fmt.Errorf("%s-%s.%s is not in build repository %s/%s",
				p.Name, p.EVR, p.Arch, src.elem.Project, src.elem.Repo)
		}
		return BdepSource{S3Key: e.Key}, e.Size, nil
	}
	if p.Location == "" {
		return BdepSource{}, 0, fmt.Errorf("no location for %s in repository %s", p.Name, src.url)
	}
	url := strings.TrimSuffix(src.url, "/") + "/" + strings.TrimPrefix(p.Location, "/")
	return BdepSource{URL: url}, 0, nil
}

func (ps *pass) forcedAt(u *unit) (t time.Time) {
	ref := storage.UnitRef{Package: u.pkg, Flavor: u.flavor}
	for _, f := range ps.forced {
		if f.Applies(ps.prpa.Repo, ps.prpa.Arch, ref) && f.RequestedAt.After(t) {
			t = f.RequestedAt
		}
	}
	return t
}
