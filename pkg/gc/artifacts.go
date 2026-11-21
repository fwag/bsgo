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
	// ListBuildStates lists the build state rows of a project; a unit with a
	// row still exists even if its package manifest is not listed.
	ListBuildStates(ctx context.Context, project string, filter storage.BuildStateFilter) ([]*storage.PackageBuildState, error)
}

// metadataFiles are the single objects a build overwrites in its unit
// directory.
var metadataFiles = map[string]struct{}{
	bininfo.FileName:        {},
	"_slsa_provenance.json": {},
	"_statistics":           {},
	"build.log":             {},
}

// Refs computes what the build repository indexes reference. Entry lists are
// content-addressed and immutable, so they are cached by key; computing the
// set again after a pointer swap only reads the entry lists that are new.
type Refs struct {
	Pointers Pointers
	Objects  Objects
	// Now and Grace make index versions that were superseded less than Grace
	// ago count as referenced: queued jobs and publisher runs that loaded the
	// previous pointer still need their binaries. Grace zero disables that.
	Now   func() time.Time
	Grace time.Duration

	mu      sync.Mutex
	entries map[string][]refEntry
}

type refEntry struct{ key, vr string }

// Snapshot is the set of referenced objects.
type Snapshot struct {
	// Keys are the referenced RPM object keys.
	Keys map[string]struct{}
	// VR maps a unit directory to the "version-release" strings of its
	// referenced binaries.
	VR map[string]map[string]struct{}
}

func (r *Refs) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Keys returns the referenced RPM object keys; see Snapshot.
func (r *Refs) Keys(ctx context.Context) (map[string]struct{}, error) {
	snap, err := r.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return snap.Keys, nil
}

// Snapshot returns what the current entries.json of every prpa lists, plus
// what the index versions superseded less than Grace ago list. That includes
// binaries exported to other architectures and the import units that receive
// them: an entry's Key is the object the binary was uploaded to, wherever its
// own unit directory is.
func (r *Refs) Snapshot(ctx context.Context) (*Snapshot, error) {
	list, err := r.Pointers.ListBuildRepoIndexes(ctx)
	if err != nil {
		return nil, err
	}
	snap := &Snapshot{Keys: map[string]struct{}{}, VR: map[string]map[string]struct{}{}}
	for _, p := range list {
		if err := r.add(ctx, snap, p.EntriesKey); err != nil {
			return nil, fmt.Errorf("%s/%s/%s: %w", p.Project, p.Repo, p.Arch, err)
		}
	}
	if r.Grace <= 0 {
		return snap, nil
	}
	all, err := r.Objects.List(ctx, PrefixBuildRepo+"/")
	if err != nil {
		return nil, err
	}
	for _, k := range recentlySupersededEntries(all, r.now(), r.Grace) {
		if err := r.add(ctx, snap, k); err != nil {
			return nil, err
		}
	}
	return snap, nil
}

func (r *Refs) add(ctx context.Context, snap *Snapshot, entriesKey string) error {
	entries, err := r.load(ctx, entriesKey)
	if err != nil {
		return err
	}
	for _, e := range entries {
		snap.Keys[e.key] = struct{}{}
		dir := path.Dir(e.key)
		if snap.VR[dir] == nil {
			snap.VR[dir] = map[string]struct{}{}
		}
		snap.VR[dir][e.vr] = struct{}{}
	}
	return nil
}

func (r *Refs) load(ctx context.Context, entriesKey string) ([]refEntry, error) {
	r.mu.Lock()
	cached, ok := r.entries[entriesKey]
	r.mu.Unlock()
	if ok {
		return cached, nil
	}
	raw, err := r.Objects.Get(ctx, entriesKey)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", entriesKey, err)
	}
	var entries []buildrepo.Entry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", entriesKey, err)
	}
	out := make([]refEntry, len(entries))
	for i, e := range entries {
		out[i] = refEntry{key: e.Key, vr: e.Version + "-" + e.Release}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries == nil || len(r.entries) > 4096 {
		r.entries = map[string][]refEntry{}
	}
	r.entries[entriesKey] = out
	return out, nil
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
	vr       map[string]map[string]struct{}
	liveDirs map[string]struct{}
	pkgs     map[string]map[string]struct{}
	pointed  map[string]bool
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
	snap, err := refs.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	ap := &artifactPlanner{objs: refs.Objects, projects: projects, refs: snap.Keys, vr: snap.VR, liveDirs: map[string]struct{}{},
		pkgs: map[string]map[string]struct{}{}, pointed: map[string]bool{}, now: now, opts: opts}
	for _, p := range ptrs {
		ap.pointed[p.Project] = true
	}
	for k := range snap.Keys {
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
				plans[prpa].abort(err)
				break
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
		var err error
		if set, err = ap.knownUnits(ctx, k.project); err != nil {
			return false, err
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
		if referenced || young(o, ap.now, ap.opts.Grace) {
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

// matchesIndexed reports whether file name carries the version-release of a
// binary that an index still references in dir. The files of an indexed build
// (debuginfo, debugsource, source RPM) stay even when a newer build that never
// reached the index has already replaced bininfo.json.
func (ap *artifactPlanner) matchesIndexed(dir, name string) bool {
	for vr := range ap.vr[dir] {
		if strings.Contains(name, "-"+vr+".") {
			return true
		}
	}
	return false
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
		if ap.matchesIndexed(dir, name) {
			return true
		}
		return !strings.HasSuffix(name, ".rpm") && !o.LastModified.Before(info.LastModified)
	}, nil
}

// knownUnits returns the package names of a project: the packages of its
// manifest and every package that has a build state row. A project that has
// build repository pointers but no package at all is an error, not a project
// whose units were all removed: the package listing is probably incomplete.
func (ap *artifactPlanner) knownUnits(ctx context.Context, project string) (map[string]struct{}, error) {
	list, err := ap.projects.ListPackages(ctx, project)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return nil, fmt.Errorf("listing packages of %s: %w", project, err)
	}
	set := map[string]struct{}{}
	for _, p := range list {
		set[p.Package.Name] = struct{}{}
	}
	states, err := ap.projects.ListBuildStates(ctx, project, storage.BuildStateFilter{})
	if err != nil {
		return nil, fmt.Errorf("listing build states of %s: %w", project, err)
	}
	for _, st := range states {
		base, _, _ := strings.Cut(st.Package, ":")
		set[base] = struct{}{}
	}
	if len(set) == 0 && ap.pointed[project] {
		return nil, fmt.Errorf("project %s has build repositories but no packages; refusing to treat its units as removed", project)
	}
	return set, nil
}
