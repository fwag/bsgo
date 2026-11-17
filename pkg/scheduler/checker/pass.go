package checker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/openSUSE/bsgo/pkg/constraints"
	"strings"
	"sync"
	"time"

	"github.com/openSUSE/bsgo/pkg/buildconfig"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/recipe"
	"github.com/openSUSE/bsgo/pkg/solv"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// unit is one buildable package identity of the prpa: a package, or one
// flavor of a multibuild package.
type unit struct {
	name, pkg, flavor string
	manifest          *manifest.PackageManifest
	commit            string
	recipeFile        string
	info              *recipe.Info
	svcDeps           []string // obs-service packages of buildtime services
	constraints       constraints.Constraints

	// fixed is set when the state was decided before dependency expansion
	// (excluded, disabled, broken, source unavailable).
	fixed *verdict

	edeps   *solv.ExpansionResult
	expErr  *solv.ExpandError
	deps    map[string]bool // names of directly required packages
	group   *group
	ndepend int // number of units depending on this one
	// level is the depth at which the meta of a cycle member changed (see
	// diffDepth); zero when unknown.
	level int
}

// verdict is the decided state of a unit.
type verdict struct {
	state   string
	details string
}

// pass is the state of one run over a prpa.
type pass struct {
	c    *Checker
	ctx  context.Context
	prpa Prpa

	projects map[string]*manifest.ProjectManifest
	prj      *manifest.ProjectManifest
	target   manifest.BuildTarget
	path     []buildconfig.PathElem

	sched      *buildconfig.Config
	worker     *buildconfig.Config
	workerText string
	configHash string
	exp        *solv.ExpandConfig

	excludeBuild map[string]bool
	onlyBuild    map[string]bool

	units  []*unit
	byName map[string]*unit
	states map[string]*storage.PackageBuildState
	forced []storage.ForcedRebuild

	pool    *solv.PathPool
	sources []poolSource
	own     map[string][]string // unit -> binary names in the own build repo
	prod    map[string]*unit    // binary name -> producing unit

	groups []*group
	out    *outcomes
	err    error

	mu    sync.Mutex
	retry time.Duration
}

func (c *Checker) newPass(ctx context.Context, p Prpa) (*pass, error) {
	if !p.Valid() {
		return nil, fmt.Errorf("invalid prpa %+v", p)
	}
	return &pass{
		c: c, ctx: ctx, prpa: p,
		projects: map[string]*manifest.ProjectManifest{},
		byName:   map[string]*unit{},
		out:      newOutcomes(),
	}, nil
}

// project loads a project manifest; an unknown project yields nil.
func (ps *pass) project(name string) (*manifest.ProjectManifest, error) {
	if prj, ok := ps.projects[name]; ok {
		return prj, nil
	}
	prj, err := ps.c.d.Store.GetProject(ps.ctx, name)
	if errors.Is(err, storage.ErrNotFound) {
		prj, err = nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load project %s: %w", name, err)
	}
	ps.projects[name] = prj
	return prj, nil
}

// loadConfig layers the project configurations along the build path and
// parses them for the scheduler (Macros stripped) and for recipe parsing
// (Macros kept, the text the worker receives).
func (ps *pass) loadConfig() error {
	prj, err := ps.project(ps.prpa.Project)
	if err != nil {
		return err
	}
	if prj == nil {
		return fmt.Errorf("%w: project %s does not exist", ErrUnknownRepository, ps.prpa.Project)
	}
	tgt, ok := prj.Targets[ps.prpa.Repo]
	if !ok || !hasArch(tgt.Architectures, ps.prpa.Arch) {
		return fmt.Errorf("%w: %s", ErrUnknownRepository, ps.prpa)
	}
	ps.prj, ps.target = prj, tgt
	if err := ps.parseConfig(); err != nil {
		return &configError{err}
	}
	return nil
}

// parseConfig layers and parses the build configuration of the prpa.
func (ps *pass) parseConfig() error {
	prj := ps.prj
	var err error
	if ps.path, err = buildconfig.PathFromTarget(prj, ps.prpa.Repo); err != nil {
		return err
	}
	schedText, workerText, err := buildconfig.Layer(prj.Project.Name, ps.prpa.Repo, ps.path, ps.project)
	if err != nil {
		return err
	}
	if ps.sched, err = buildconfig.Parse(schedText, ps.prpa.Arch); err != nil {
		return fmt.Errorf("project configuration of %s: %w", ps.prpa, err)
	}
	if ps.worker, err = buildconfig.Parse(workerText, ps.prpa.Arch); err != nil {
		return fmt.Errorf("worker configuration of %s: %w", ps.prpa, err)
	}
	ps.workerText = workerText
	sum := sha256.Sum256([]byte(ps.prpa.Arch + "\x00" + workerText))
	ps.configHash = hex.EncodeToString(sum[:])
	ps.exp = expandConfig(ps.sched)
	ps.excludeBuild, ps.onlyBuild = buildFlagSets(ps.sched.BuildFlags)
	return nil
}

// configError marks an unusable build configuration: the repository is
// broken, not just failing to be checked.
type configError struct{ err error }

func (e *configError) Error() string { return e.err.Error() }
func (e *configError) Unwrap() error { return e.err }

func hasArch(list []string, arch string) bool {
	for _, a := range list {
		if a == arch {
			return true
		}
	}
	return false
}

// expandConfig maps a parsed project configuration to the expander input.
func expandConfig(cfg *buildconfig.Config) *solv.ExpandConfig {
	return &solv.ExpandConfig{
		Prefer: cfg.Prefer, Ignore: cfg.Ignore, Conflict: cfg.Conflict,
		ExpandFlags: cfg.ExpandFlags, Substitute: cfg.Substitute, FileProvides: cfg.FileProvides,
		Preinstall: cfg.Preinstall, VMInstall: cfg.Vminstall, Required: cfg.Required,
		Support: cfg.Support, Keep: cfg.Keep, IgnoreRebuild: cfg.IgnoreRebuild,
		BuildType: cfg.Type,
	}
}

// buildFlagSets extracts the excludebuild: and onlybuild: entries of the
// BuildFlags directive. onlyBuild is nil when no onlybuild flag exists.
func buildFlagSets(flags []string) (exclude, only map[string]bool) {
	exclude = map[string]bool{}
	for _, f := range flags {
		if v, ok := strings.CutPrefix(f, "excludebuild:"); ok {
			exclude[v] = true
		}
		if v, ok := strings.CutPrefix(f, "onlybuild:"); ok {
			if only == nil {
				only = map[string]bool{}
			}
			only[v] = true
		}
	}
	return exclude, only
}

// loadStates reads the current build states and pending forced rebuilds.
func (ps *pass) loadStates() error {
	list, err := ps.c.d.Store.ListBuildStates(ps.ctx, ps.prpa.Project,
		storage.BuildStateFilter{Repo: ps.prpa.Repo, Arch: ps.prpa.Arch})
	if err != nil {
		return fmt.Errorf("list build states: %w", err)
	}
	ps.states = make(map[string]*storage.PackageBuildState, len(list))
	for _, st := range list {
		ps.states[unitKey(st.Package, st.Flavor)] = st
	}
	ps.forced, err = ps.c.d.Store.ListForcedRebuilds(ps.ctx, ps.prpa.Project)
	if err != nil {
		return fmt.Errorf("list forced rebuilds: %w", err)
	}
	return nil
}

func unitKey(pkg, flavor string) string {
	if flavor == "" {
		return pkg
	}
	return pkg + ":" + flavor
}

func (ps *pass) isForced(u *unit) bool {
	ref := storage.UnitRef{Package: u.pkg, Flavor: u.flavor}
	for _, f := range ps.forced {
		if f.Applies(ps.prpa.Repo, ps.prpa.Arch, ref) {
			return true
		}
	}
	return false
}
