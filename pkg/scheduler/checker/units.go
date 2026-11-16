package checker

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/recipe"
	"github.com/openSUSE/bsgo/pkg/recipe/fetch"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// sourceRetry is the delay before a pass is repeated after sources could not
// be fetched.
const sourceRetry = time.Minute

// loadUnits enumerates the package units of the prpa: it fetches the recipe
// files at every package's pinned commit, expands _multibuild into flavor
// units and parses each recipe with the worker variant of the configuration.
func (ps *pass) loadUnits() error {
	pkgs, err := ps.c.d.Store.ListPackages(ps.ctx, ps.prpa.Project)
	if err != nil {
		return fmt.Errorf("list packages: %w", err)
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].Package.Name < pkgs[j].Package.Name })
	results := make([][]*unit, len(pkgs))
	errs := make([]error, len(pkgs))
	sem := make(chan struct{}, ps.c.d.FetchWorkers)
	var wg sync.WaitGroup
	for i, pkg := range pkgs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i], errs[i] = ps.unitsOf(pkg)
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return err
	}
	for _, us := range results {
		for _, u := range us {
			ps.units = append(ps.units, u)
			ps.byName[u.name] = u
		}
	}
	return nil
}

func (ps *pass) fixedUnit(pkg *manifest.PackageManifest, state, details string) []*unit {
	return []*unit{{
		name: pkg.Package.Name, pkg: pkg.Package.Name, manifest: pkg,
		commit: pkg.Source.Commit, fixed: &verdict{state, details},
	}}
}

// manifestVerdict applies the package manifest's own build switches.
func (ps *pass) manifestVerdict(pkg *manifest.PackageManifest) *verdict {
	if !pkg.Build.Enabled {
		return &verdict{storage.StateDisabled, ""}
	}
	if enabled, ok := pkg.Build.Targets[ps.prpa.Repo]; ok && !enabled {
		return &verdict{storage.StateDisabled, ""}
	}
	f := pkg.Build.Architectures
	if len(f.Include) > 0 && !hasArch(f.Include, ps.prpa.Arch) || hasArch(f.Exclude, ps.prpa.Arch) {
		return &verdict{storage.StateExcluded, ""}
	}
	return nil
}

func (ps *pass) unitsOf(pkg *manifest.PackageManifest) ([]*unit, error) {
	if v := ps.manifestVerdict(pkg); v != nil {
		return ps.fixedUnit(pkg, v.state, v.details), nil
	}
	if v := ps.packageFlagExclusion(pkg.Package.Name); v != nil {
		return ps.fixedUnit(pkg, v.state, v.details), nil
	}
	if pkg.Source.Git == "" || pkg.Source.Commit == "" {
		return ps.fixedUnit(pkg, storage.StateBroken, "no pinned source commit"), nil
	}
	files, err := ps.c.d.Recipes.Fetch(ps.ctx, pkg.Source.Git, pkg.Source.Commit)
	if err != nil {
		if ps.ctx.Err() != nil {
			return nil, ps.ctx.Err()
		}
		ps.noteRetry(sourceRetry)
		return ps.fixedUnit(pkg, storage.StateBlocked, "source unavailable: "+err.Error()), nil
	}
	var mb *recipe.Multibuild
	if data, ok := files[recipe.MultibuildFile]; ok {
		if mb, err = recipe.ParseMultibuild(data); err != nil {
			return ps.fixedUnit(pkg, storage.StateBroken, err.Error()), nil
		}
	}
	var out []*unit
	for _, ru := range recipe.Expand(pkg.Package.Name, fileNames(files), mb, ps.prpa.Repo) {
		out = append(out, ps.parseUnit(pkg, ru, files))
	}
	return out, nil
}

func fileNames(files fetch.Files) []string {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (ps *pass) noteRetry(d time.Duration) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.retry == 0 || d < ps.retry {
		ps.retry = d
	}
}

// parseUnit parses the recipe of one unit and applies the recipe-level and
// BuildFlags exclusions.
func (ps *pass) parseUnit(pkg *manifest.PackageManifest, ru recipe.Unit, files fetch.Files) *unit {
	u := &unit{name: ru.Name, pkg: ru.Package, flavor: ru.Flavor, manifest: pkg, commit: pkg.Source.Commit}
	if v := ps.flagExclusion(u.name, u.pkg); v != nil {
		u.fixed = v
		return u
	}
	file := ru.RecipeFile
	if pinned := pkg.Source.Recipe; pinned != "" && ru.Flavor == "" {
		if _, ok := files[pinned]; ok {
			file = pinned
		}
	}
	if file == "" {
		u.fixed = &verdict{storage.StateBroken, "no recipe file"}
		return u
	}
	u.recipeFile = file
	info, err := ps.recipeInfo(u, files[file])
	if err != nil {
		u.fixed = &verdict{storage.StateBroken, err.Error()}
		return u
	}
	u.info = info
	if data, ok := files[recipe.ServiceFile]; ok {
		if err := ps.serviceDeps(u, data); err != nil {
			u.fixed = &verdict{storage.StateBroken, err.Error()}
			return u
		}
	}
	u.fixed = ps.exclusion(u)
	return u
}

// serviceDeps records the packages of the buildtime services of a _service
// file as additional build dependencies of u (BSSched::BuildJob btdeps).
func (ps *pass) serviceDeps(u *unit, data []byte) error {
	names, err := recipe.BuildtimeServices(data)
	if err != nil {
		return err
	}
	u.svcDeps = recipe.ServiceDeps(names, ps.exp.Substitute, ps.worker.BinaryType)
	return nil
}

// recipeInfo parses a recipe, caching the result by (commit, recipe, flavor,
// configuration).
func (ps *pass) recipeInfo(u *unit, content []byte) (*recipe.Info, error) {
	key := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s", u.commit, u.recipeFile, u.pkg, u.flavor, ps.configHash)
	ps.c.mu.Lock()
	info, ok := ps.c.infos[key]
	ps.c.mu.Unlock()
	if ok {
		return info, nil
	}
	info, err := recipe.Parse(ps.worker, u.recipeFile, content, recipe.Options{Package: u.pkg, Flavor: u.flavor})
	if err != nil {
		return nil, err
	}
	if info.Error != "" {
		return nil, errors.New(info.Error)
	}
	ps.c.mu.Lock()
	if len(ps.c.infos) > maxInfoCache {
		clear(ps.c.infos)
	}
	ps.c.infos[key] = info
	ps.c.mu.Unlock()
	return info, nil
}

// maxInfoCache bounds the parsed recipe cache.
const maxInfoCache = 50000

// exclusion returns the excluded verdict of a unit, or nil. The texts are the
// ones OBS reports.
func (ps *pass) exclusion(u *unit) *verdict {
	info := u.info
	if info.ExclArch != nil && !hasArch(info.ExclArch, ps.prpa.Arch) || hasArch(info.BadArch, ps.prpa.Arch) {
		return &verdict{storage.StateExcluded, ""}
	}
	return nil
}

// flagExclusion applies the BuildFlags excludebuild:/onlybuild: lists to a
// unit like BSSched/Checker.pm:checkpkgs: before any recipe error, matching
// the unit name ("pkg:flavor") or its release name (the base package).
func (ps *pass) flagExclusion(name, releaseName string) *verdict {
	if ps.excludeBuild[name] || ps.excludeBuild[releaseName] {
		return &verdict{storage.StateExcluded, "package blacklist"}
	}
	if ps.onlyBuild != nil && !ps.onlyBuild[name] && !ps.onlyBuild[releaseName] {
		return &verdict{storage.StateExcluded, "package whitelist"}
	}
	return nil
}

// packageFlagExclusion excludes a whole package before its sources are
// fetched when no unit of it can pass the BuildFlags lists: the package is
// blacklisted, or whitelisting is active and neither the package nor any of
// its flavors ("pkg:flavor") is listed.
func (ps *pass) packageFlagExclusion(pkg string) *verdict {
	if ps.excludeBuild[pkg] {
		return &verdict{storage.StateExcluded, "package blacklist"}
	}
	if ps.onlyBuild == nil || ps.onlyBuild[pkg] {
		return nil
	}
	for name := range ps.onlyBuild {
		if strings.HasPrefix(name, pkg+":") {
			return nil
		}
	}
	return &verdict{storage.StateExcluded, "package whitelist"}
}
