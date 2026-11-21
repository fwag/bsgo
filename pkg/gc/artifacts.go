package gc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// Projects lists the packages of a project; storage.Store satisfies it.
type Projects interface {
	ListPackages(ctx context.Context, projectName string) ([]*manifest.PackageManifest, error)
}

// metadataFiles are the single objects a build overwrites in its unit
// directory.
var metadataFiles = map[string]struct{}{
	bininfo.FileName:        {},
	"_slsa_provenance.json": {},
	"_statistics":           {},
	"build.log":             {},
}

// Refs computes the keys of the RPMs that the current build repository
// indexes reference. Entry lists are content-addressed and immutable, so they
// are cached by key; computing the set again after a pointer swap only reads
// the entry lists that are new.
type Refs struct {
	Pointers Pointers
	Objects  Objects

	mu      sync.Mutex
	entries map[string][]string
}

// Keys returns the object keys listed by the current entries.json of every
// prpa. That includes binaries exported to other architectures and the import
// units that receive them: an entry's Key is the object the binary was
// uploaded to, wherever its own unit directory is.
func (r *Refs) Keys(ctx context.Context) (map[string]struct{}, error) {
	list, err := r.Pointers.ListBuildRepoIndexes(ctx)
	if err != nil {
		return nil, err
	}
	set := map[string]struct{}{}
	for _, p := range list {
		keys, err := r.rpmKeys(ctx, p.EntriesKey)
		if err != nil {
			return nil, fmt.Errorf("%s/%s/%s: %w", p.Project, p.Repo, p.Arch, err)
		}
		for _, k := range keys {
			set[k] = struct{}{}
		}
	}
	return set, nil
}

func (r *Refs) rpmKeys(ctx context.Context, entriesKey string) ([]string, error) {
	r.mu.Lock()
	keys, ok := r.entries[entriesKey]
	r.mu.Unlock()
	if ok {
		return keys, nil
	}
	raw, err := r.Objects.Get(ctx, entriesKey)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", entriesKey, err)
	}
	var entries []buildrepo.Entry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", entriesKey, err)
	}
	keys = make([]string, len(entries))
	for i, e := range entries {
		keys[i] = e.Key
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries == nil || len(r.entries) > 4096 {
		r.entries = map[string][]string{}
	}
	r.entries[entriesKey] = keys
	return keys, nil
}

// artifactKey is a parsed artifacts/<project>/<repo>/<arch>/<unit>/<file>.
type artifactKey struct {
	project, prpa, unit, file string
}

func splitArtifactKey(key string) (artifactKey, bool) {
	parts := strings.SplitN(key, "/", 6)
	if len(parts) != 6 || parts[0] != PrefixArtifacts || parts[5] == "" {
		return artifactKey{}, false
	}
	return artifactKey{project: parts[1], prpa: strings.Join(parts[1:4], "/"), unit: parts[4], file: parts[5]}, true
}

// artifactPlanner carries the state of one PlanArtifactGC run.
type artifactPlanner struct {
	objs     Objects
	projects Projects
	refs     map[string]struct{}
	liveDirs map[string]struct{}
	pkgs     map[string]map[string]struct{}
	now      time.Time
	opts     Options
}

// PlanArtifactGC decides, per prpa, which objects below artifacts/ to delete.
// An object is kept when
//   - it is younger than opts.Grace;
//   - an entries.json of any prpa references it (own units, exports, imports);
//   - its unit still exists (it has referenced binaries or its package is in
//     the project) and it is one of the files of the unit's current build:
//     listed in the current bininfo.json (including debuginfo, debugsource and
//     source RPMs), one of the metadata files bininfo.json, _slsa_provenance.json,
//     _statistics and build.log, or a non-RPM result uploaded together with or
//     after the current bininfo.json.
//
// A live unit without bininfo.json is kept as a whole. Everything else, and
// all old files of units that no longer exist, is deleted. Keys that do not
// have the artifacts/<project>/<repo>/<arch>/<unit>/<file> shape are not
// touched. Jobs that are queued or running resolved their build dependencies
// from the index when they were scheduled; the grace period covers that
// window, the dispatcher is not consulted.
func PlanArtifactGC(ctx context.Context, refs *Refs, projects Projects, now time.Time, opts Options) ([]Plan, error) {
	ptrs, err := refs.Pointers.ListBuildRepoIndexes(ctx)
	if err != nil {
		return nil, err
	}
	all, err := refs.Objects.List(ctx, listPrefix(PrefixArtifacts, opts.Project))
	if err != nil {
		return nil, err
	}
	if len(ptrs) == 0 && len(all) > 0 {
		return nil, errNoPointers
	}
	keys, err := refs.Keys(ctx)
	if err != nil {
		return nil, err
	}
	ap := &artifactPlanner{objs: refs.Objects, projects: projects, refs: keys, liveDirs: map[string]struct{}{},
		pkgs: map[string]map[string]struct{}{}, now: now, opts: opts}
	for k := range keys {
		ap.liveDirs[path.Dir(k)] = struct{}{}
	}
	return ap.plan(ctx, all)
}

func (ap *artifactPlanner) plan(ctx context.Context, all []Object) ([]Plan, error) {
	plans := map[string]*Plan{}
	units := map[string]map[string][]Object{}
	for _, o := range all {
		k, ok := splitArtifactKey(o.Key)
		if !ok {
			continue
		}
		planOf(plans, PrefixArtifacts, k.prpa)
		if units[k.prpa] == nil {
			units[k.prpa] = map[string][]Object{}
		}
		dir := path.Dir(o.Key)
		units[k.prpa][dir] = append(units[k.prpa][dir], o)
	}
	for prpa, dirs := range units {
		for dir, files := range dirs {
			if err := ap.planUnit(ctx, plans[prpa], dir, files); err != nil {
				return nil, err
			}
		}
	}
	return sortedPlans(plans), nil
}

// live reports whether the unit directory belongs to a unit that still exists.
func (ap *artifactPlanner) live(ctx context.Context, dir string) (bool, error) {
	if _, ok := ap.liveDirs[dir]; ok {
		return true, nil
	}
	k, ok := splitArtifactKey(dir + "/x")
	if !ok {
		return false, nil
	}
	set, ok := ap.pkgs[k.project]
	if !ok {
		list, err := ap.projects.ListPackages(ctx, k.project)
		if err != nil && !errors.Is(err, storage.ErrNotFound) {
			return false, fmt.Errorf("listing packages of %s: %w", k.project, err)
		}
		set = map[string]struct{}{}
		for _, p := range list {
			set[p.Package.Name] = struct{}{}
		}
		ap.pkgs[k.project] = set
	}
	base, _, _ := strings.Cut(k.unit, ":")
	_, exists := set[base]
	return exists, nil
}

func (ap *artifactPlanner) planUnit(ctx context.Context, p *Plan, dir string, files []Object) error {
	var candidates []Object
	for _, o := range files {
		_, referenced := ap.refs[o.Key]
		if referenced || ap.now.Sub(o.LastModified) < ap.opts.Grace {
			p.keep(o)
			continue
		}
		candidates = append(candidates, o)
	}
	if len(candidates) == 0 {
		return nil
	}
	live, err := ap.live(ctx, dir)
	if err != nil {
		return err
	}
	if !live {
		for _, o := range candidates {
			p.drop(o)
		}
		return nil
	}
	current, err := ap.currentBuild(ctx, dir, files)
	if err != nil {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%s: kept as a whole: %v", dir, err))
		current = func(Object) bool { return true }
	}
	for _, o := range candidates {
		if current(o) {
			p.keep(o)
		} else {
			p.drop(o)
		}
	}
	return nil
}

// currentBuild returns the predicate of the files that belong to the current
// build of the unit in dir.
func (ap *artifactPlanner) currentBuild(ctx context.Context, dir string, files []Object) (func(Object) bool, error) {
	infoKey := dir + "/" + bininfo.FileName
	var info *Object
	for i := range files {
		if files[i].Key == infoKey {
			info = &files[i]
		}
	}
	if info == nil {
		return func(Object) bool { return true }, nil
	}
	raw, err := ap.objs.Get(ctx, infoKey)
	if err != nil {
		return nil, err
	}
	infos, err := bininfo.Unmarshal(raw)
	if err != nil {
		return nil, err
	}
	listed := map[string]struct{}{}
	for _, b := range infos {
		listed[b.Filename] = struct{}{}
	}
	return func(o Object) bool {
		name := strings.TrimPrefix(o.Key, dir+"/")
		if _, ok := metadataFiles[name]; ok {
			return true
		}
		if _, ok := listed[name]; ok {
			return true
		}
		return !strings.HasSuffix(name, ".rpm") && !o.LastModified.Before(info.LastModified)
	}, nil
}
