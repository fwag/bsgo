// Package checker is the scheduler of bsgo, the equivalent of the OBS
// bs_sched "checker". One pass handles one prpa (project, repository,
// architecture): it expands the build dependencies of every package unit
// against the build path, orders the units by their in-prpa dependencies,
// decides for each unit whether it is excluded, blocked, unresolvable, up to
// date or must be built, persists all states in one transaction and hands the
// jobs of newly scheduled units to a JobSink.
//
// The pass is a pure function of the stored state (manifests, build states,
// build repository indexes); it keeps no state of its own besides caches, so
// any server replica that holds the prpa lease can run it (package coord).
package checker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/recipe"
	"github.com/openSUSE/bsgo/pkg/recipe/fetch"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/solv"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// Prpa identifies one build repository.
type Prpa = buildrepo.Prpa

// Store is the persistent state the checker reads and writes. storage.Store
// satisfies it.
type Store interface {
	GetProject(ctx context.Context, name string) (*manifest.ProjectManifest, error)
	ListPackages(ctx context.Context, project string) ([]*manifest.PackageManifest, error)
	ListBuildStates(ctx context.Context, project string, filter storage.BuildStateFilter) ([]*storage.PackageBuildState, error)
	UpsertBuildStates(ctx context.Context, states []*storage.PackageBuildState) error
	NextBuildCounters(ctx context.Context, key storage.CounterKey) (storage.BuildCounters, error)
	RevisionFor(ctx context.Context, project, pkg, commit string) (*storage.PackageRevision, error)
	WantRevisions(ctx context.Context, refs []storage.RevisionRef) error
	ListForcedRebuilds(ctx context.Context, project string) ([]storage.ForcedRebuild, error)
	GetPrpaState(ctx context.Context, project, repo, arch string) (*storage.PrpaState, error)
	SetPrpaState(ctx context.Context, st *storage.PrpaState) error
	ClearForcedRebuilds(ctx context.Context, project, repo, arch string, units []storage.UnitRef) error
	SaveBuildMetas(ctx context.Context, project, repo, arch string, metas []storage.BuildMeta) error
	GetBuildMetas(ctx context.Context, project, repo, arch string, hashes []string) ([]storage.BuildMeta, error)
	PruneBuildMetas(ctx context.Context, project, repo, arch string, keep []string, before time.Time) error
}

// Recipes provides the recipe files of a package at a pinned commit.
// *fetch.Fetcher satisfies it.
type Recipes interface {
	Fetch(ctx context.Context, url, commit string) (fetch.Files, error)
}

// Indexes provides the build repository index of a prpa. An unknown prpa
// yields an empty index. *buildrepo.Reader satisfies it.
type Indexes interface {
	Index(ctx context.Context, p Prpa) (*buildrepo.Index, error)
}

// DoD provides the solv blob of a remote (URL) path element.
// *dod.RepoCache satisfies it.
type DoD interface {
	Get(ctx context.Context, url string) (solv.RepoBlob, error)
}

// JobSink receives the jobs of newly scheduled units and the cancellations of
// jobs that are no longer wanted. Emit must be idempotent per JobSpec.JobID:
// a pass that crashed after emitting is repeated with the same IDs.
type JobSink interface {
	Emit(ctx context.Context, job *JobSpec) error
	Cancel(ctx context.Context, jobID, reason string) error
}

// Deps are the collaborators of a Checker.
type Deps struct {
	Store   Store
	Recipes Recipes
	Indexes Indexes
	DoD     DoD
	Sink    JobSink
	// Pools caches path pools; nil creates a small private cache.
	Pools *solv.PoolCache
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// FetchWorkers bounds concurrent recipe fetches; zero means 16.
	FetchWorkers int
	// Logf receives plain diagnostic lines (no decoration); nil discards.
	Logf func(format string, args ...any)
}

// Checker runs prpa passes.
type Checker struct {
	d Deps

	mu    sync.Mutex
	infos map[string]*recipe.Info
	// trees caches the dependency metas by hash (content-addressed, so never
	// stale), metas the generated metas by their input key (the OBS meta
	// shortcut).
	trees map[string][]string
	metas map[string]string
}

// New returns a Checker.
func New(d Deps) *Checker {
	if d.Pools == nil {
		d.Pools = solv.NewPoolCache(8)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.FetchWorkers <= 0 {
		d.FetchWorkers = 16
	}
	return &Checker{d: d, infos: map[string]*recipe.Info{}, trees: map[string][]string{}, metas: map[string]string{}}
}

// Close releases the cached path pools.
func (c *Checker) Close() { c.d.Pools.Close() }

// Result summarizes a pass.
type Result struct {
	// Counts is the number of units per resulting state.
	Counts map[string]int
	// Scheduled lists the units for which a job was emitted.
	Scheduled []string
	// Cancelled lists the job IDs that were cancelled.
	Cancelled []string
	// RetryAfter asks for another pass after this delay, for example because
	// sources could not be fetched; zero means none.
	RetryAfter time.Duration
}

// Run performs one pass over p.
func (c *Checker) Run(ctx context.Context, p Prpa) (*Result, error) {
	ps, err := c.newPass(ctx, p)
	if err != nil {
		return nil, err
	}
	if err := ps.prepare(); err != nil {
		return nil, ps.recordBroken(err)
	}
	ps.decideAll()
	return ps.finish()
}

// ErrUnknownRepository is returned for a prpa the project does not define.
var ErrUnknownRepository = errors.New("unknown repository")

func (ps *pass) prepare() error {
	if err := ps.loadConfig(); err != nil {
		return err
	}
	if err := ps.loadUnits(); err != nil {
		return err
	}
	if err := ps.loadStates(); err != nil {
		return err
	}
	if err := ps.buildPool(); err != nil {
		return err
	}
	if err := ps.expandUnits(); err != nil {
		return err
	}
	ps.buildGraph()
	return ps.loadMetaTrees()
}

func (ps *pass) finish() (*Result, error) {
	if err := ps.emitAndStore(); err != nil {
		return nil, fmt.Errorf("pass %s: %w", ps.prpa, err)
	}
	res := ps.result()
	if err := ps.recordRepoState(res.Counts, ""); err != nil {
		return nil, fmt.Errorf("pass %s: %w", ps.prpa, err)
	}
	return res, nil
}
