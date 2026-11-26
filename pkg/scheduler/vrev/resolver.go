// Package vrev resolves the release numbers (vrev, the CI_CNT of builds) of
// package revisions in the background. The checker never computes them: it
// reads the stored result and, while a revision is still pending, tells the
// resolver which ones units wait for (storage.WantRevisions). The resolver
// works those first (on-demand priority), then backfills the rest.
//
// The resolver is deliberately slow and polite to the git host: few workers,
// a pause after every resolution, exponential backoff on errors and a global
// cool-down when the server pushes back. A failure only delays the revision;
// no unit is ever marked broken or failed because of it.
package vrev

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/openSUSE/bsgo/pkg/buildconfig"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/recipe/history"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// Defaults of Options.
const (
	DefaultWorkers = 2
	DefaultDelay   = time.Second
	DefaultPoll    = 10 * time.Second
	// DefaultSummary is the interval of the progress line while revisions are
	// pending.
	DefaultSummary = 5 * time.Minute
	// rateLimitPause stops every worker after the server pushed back, and
	// rateLimitRetry is the least wait before the revision is tried again.
	rateLimitPause = time.Minute
	rateLimitRetry = 5 * time.Minute
	minLease       = 10 * time.Minute
)

// Store is the persistent state the resolver needs; storage.Store satisfies it.
type Store interface {
	GetProject(ctx context.Context, name string) (*manifest.ProjectManifest, error)
	GetPackage(ctx context.Context, project, pkg string) (*manifest.PackageManifest, error)
	ClaimRevisions(ctx context.Context, owner string, limit int, lease time.Duration) ([]storage.PackageRevision, error)
	FinishRevision(ctx context.Context, rev storage.PackageRevision, res storage.RevisionResult) (*storage.PackageRevision, error)
	FailRevision(ctx context.Context, rev storage.PackageRevision, cause string, minRetry time.Duration) (*storage.PackageRevision, error)
	RevisionCounts(ctx context.Context) (storage.RevisionCounts, error)
	NearestHistory(ctx context.Context, key storage.HistoryKey, commits []string) (*storage.SourceHistory, error)
	PutHistory(ctx context.Context, h storage.SourceHistory) error
}

// ResolveFunc computes the history of one commit; history.Resolve is the
// production one.
type ResolveFunc func(ctx context.Context, req history.Request, memo history.Memo) (*history.Result, error)

// Options configure a Resolver.
type Options struct {
	// Owner identifies this process in revision leases.
	Owner string
	// Workers is the number of concurrent resolutions; zero or less disables Run.
	Workers int
	// Timeout bounds one resolution; zero means history.DefaultTimeout.
	Timeout time.Duration
	// Delay is the pause of a worker after every resolution, so the git host
	// sees a slow, steady trickle instead of a burst.
	Delay time.Duration
	// Poll is the wait of an idle worker; zero means DefaultPoll.
	Poll time.Duration
	// Summary is the interval of the progress line; zero means DefaultSummary.
	Summary time.Duration
	// Logf receives one plain line per event; nil discards.
	Logf func(format string, args ...any)
	// Resolve is the history engine; nil means history.Resolve.
	Resolve ResolveFunc
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Resolver resolves pending package revisions.
type Resolver struct {
	store Store
	opts  Options

	mu         sync.Mutex
	pauseUntil time.Time
	configs    map[string]projectConfig
}

type projectConfig struct {
	hash string
	cfg  *buildconfig.Config
}

// New returns a Resolver.
func New(store Store, opts Options) *Resolver {
	if opts.Resolve == nil {
		opts.Resolve = history.Resolve
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Poll <= 0 {
		opts.Poll = DefaultPoll
	}
	if opts.Summary <= 0 {
		opts.Summary = DefaultSummary
	}
	if opts.Timeout <= 0 {
		opts.Timeout = history.DefaultTimeout
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	return &Resolver{store: store, opts: opts, configs: map[string]projectConfig{}}
}

// Run resolves revisions until ctx is done. It returns at once when Workers
// is zero.
func (r *Resolver) Run(ctx context.Context) {
	if r.opts.Workers <= 0 {
		return
	}
	var wg sync.WaitGroup
	for range r.opts.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.worker(ctx)
		}()
	}
	r.summaryLoop(ctx)
	wg.Wait()
}

func (r *Resolver) worker(ctx context.Context) {
	for ctx.Err() == nil {
		wait := r.opts.Poll
		if d := r.pauseLeft(); d > 0 {
			wait = d
		} else if r.Step(ctx) {
			wait = r.opts.Delay
		}
		if !sleep(ctx, wait) {
			return
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (r *Resolver) pauseLeft() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pauseUntil.Sub(r.opts.Now())
}

func (r *Resolver) summaryLoop(ctx context.Context) {
	t := time.NewTicker(r.opts.Summary)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c, err := r.store.RevisionCounts(ctx)
			if err == nil && c.Pending > 0 {
				r.opts.Logf("pending %d, resolved %d, skipped %d", c.Pending, c.Resolved, c.Skipped)
			}
		}
	}
}

// Step claims one pending revision, wanted ones first, and resolves it. It
// reports whether a revision was worked on.
func (r *Resolver) Step(ctx context.Context) bool {
	claimed, err := r.store.ClaimRevisions(ctx, r.opts.Owner, 1, max(2*r.opts.Timeout, minLease))
	if err != nil {
		if ctx.Err() == nil {
			r.opts.Logf("claim failed: %v", err)
		}
		return false
	}
	if len(claimed) == 0 {
		return false
	}
	r.resolve(ctx, claimed[0])
	return true
}

func (r *Resolver) resolve(ctx context.Context, rev storage.PackageRevision) {
	label := fmt.Sprintf("%s/%s %s", rev.Project, rev.Package, short(rev.Commit))
	start := r.opts.Now()
	res, err := r.compute(ctx, rev)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		r.fail(ctx, rev, label, err)
		return
	}
	done, err := r.store.FinishRevision(ctx, rev, storage.RevisionResult{Version: res.Version, GitVrev: res.GitVrev})
	switch {
	case errors.Is(err, storage.ErrConflict):
		return
	case err != nil:
		r.opts.Logf("%s: store failed: %v", label, err)
		return
	}
	if res.Truncated {
		r.opts.Logf("%s: history longer than %d commits, older part ignored", label, history.MaxWalk)
	}
	r.opts.Logf("%s: version %s vrev %d (git %d) walked %d blobs %d in %s",
		label, res.Version, done.Vrev, res.GitVrev, res.Walked, res.BlobsFetched,
		r.opts.Now().Sub(start).Round(10*time.Millisecond))
}

func (r *Resolver) fail(ctx context.Context, rev storage.PackageRevision, label string, cause error) {
	var minRetry time.Duration
	if rateLimited(cause) {
		minRetry = rateLimitRetry
		r.mu.Lock()
		r.pauseUntil = r.opts.Now().Add(rateLimitPause)
		r.mu.Unlock()
	}
	row, err := r.store.FailRevision(ctx, rev, cause.Error(), minRetry)
	if err != nil {
		if !errors.Is(err, storage.ErrConflict) {
			r.opts.Logf("%s: store failed: %v", label, err)
		}
		return
	}
	if row.State == storage.RevisionSkipped {
		r.opts.Logf("%s: skipped after %d attempts (superseded): %v", label, row.Attempts, cause)
		return
	}
	r.opts.Logf("%s: failed (attempt %d, retry in %s): %v", label, row.Attempts,
		row.NextTryAt.Sub(r.opts.Now()).Round(time.Second), cause)
}

// rateLimited reports whether a git error looks like the server asking to slow
// down.
func rateLimited(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "429") || strings.Contains(msg, "too many requests") ||
		strings.Contains(msg, "rate limit") || strings.Contains(msg, "503")
}

func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// compute runs the history engine for rev and memoizes the commit.
func (r *Resolver) compute(ctx context.Context, rev storage.PackageRevision) (*history.Result, error) {
	prj, err := r.store.GetProject(ctx, rev.Project)
	if err != nil {
		return nil, fmt.Errorf("project %s: %w", rev.Project, err)
	}
	cfg, hash, err := r.config(prj)
	if err != nil {
		return nil, err
	}
	req := history.Request{
		URL: rev.GitURL, Commit: rev.Commit, Lookup: rev.Package, Config: cfg, ConfigHash: hash,
		Timeout: r.opts.Timeout,
	}
	if pm, err := r.store.GetPackage(ctx, rev.Project, rev.Package); err == nil {
		req.Pinned = pm.Source.Recipe
	}
	res, err := r.opts.Resolve(ctx, req, memo{r.store})
	if err != nil {
		return nil, err
	}
	key := req.Key()
	err = r.store.PutHistory(ctx, storage.SourceHistory{
		URL: key.URL, Commit: res.Commit, Lookup: key.Lookup, ConfigHash: key.ConfigHash, AlgoRev: key.AlgoRev,
		MainRecipe: res.MainRecipe, Version: res.Version, ReleaseStart: res.ReleaseStart,
		VT: res.VT, Walked: res.Walked,
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// config returns the project's own configuration for arch noarch (what OBS
// reads at commit time) and the hash of its text.
func (r *Resolver) config(prj *manifest.ProjectManifest) (*buildconfig.Config, string, error) {
	name := prj.Project.Name
	text, err := buildconfig.Render(prj, "", "", name)
	if err != nil {
		return nil, "", fmt.Errorf("render project configuration of %s: %w", name, err)
	}
	sum := sha256.Sum256([]byte(text))
	hash := hex.EncodeToString(sum[:])
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.configs[name]; ok && c.hash == hash {
		return c.cfg, hash, nil
	}
	cfg, err := buildconfig.Parse(text, "noarch")
	if err != nil {
		return nil, "", fmt.Errorf("parse project configuration of %s: %w", name, err)
	}
	r.configs[name] = projectConfig{hash: hash, cfg: cfg}
	return cfg, hash, nil
}

// memo adapts the store to history.Memo.
type memo struct{ store Store }

func (m memo) Nearest(ctx context.Context, key history.Key, commits []string) (*history.Point, error) {
	h, err := m.store.NearestHistory(ctx, storage.HistoryKey{
		URL: key.URL, Lookup: key.Lookup, ConfigHash: key.ConfigHash, AlgoRev: key.AlgoRev,
	}, commits)
	if err != nil || h == nil {
		return nil, err
	}
	return &history.Point{
		Commit: h.Commit, MainRecipe: h.MainRecipe, Version: h.Version,
		ReleaseStart: h.ReleaseStart, VT: h.VT,
	}, nil
}
