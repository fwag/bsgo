// Package history computes the OBS "vrev" of a git commit: the revision
// counter OBS keeps per package, derived here from the first-parent history
// of the commit alone (no database, no object store).
//
// The history is fetched without blobs, the main recipe (spec) of every
// first-parent commit is located from trees, only the distinct recipe blobs
// are fetched in one batch, and the OBS rule is folded over the chain: the
// counter of a version restarts at 1 for a new version and continues when an
// old version returns; the Release leading number of the recipe raises it.
package history

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"time"

	"github.com/openSUSE/bsgo/pkg/buildconfig"
	"github.com/openSUSE/bsgo/pkg/recipe/internal/gitrepo"
)

const (
	// AlgoRev identifies the folding rules; a Memo must ignore entries written
	// under another value. Bump it when Resolve would compute different
	// results for the same input.
	AlgoRev = 1
	// MaxWalk bounds the first-parent commits folded; older history counts as
	// absent (Result.Truncated).
	MaxWalk = 10000
	// DefaultTimeout bounds one Resolve call (network and parsing).
	DefaultTimeout = 5 * time.Minute
	// readChunk is the number of recipe blobs held in memory at once.
	readChunk = 256
)

// Request names the commit to resolve.
type Request struct {
	URL, Commit string
	// Lookup is the package name (OBS packid): the main recipe is the one
	// named after it, never a multibuild flavor recipe.
	Lookup string
	// Pinned is the manifest's source.recipe; when present in a commit it is
	// the main recipe there. May be empty.
	Pinned string
	// Config is the project's own configuration parsed for arch "noarch"; nil
	// means an empty configuration.
	Config *buildconfig.Config
	// ConfigHash identifies Config for memoization (for example the sha256 of
	// the configuration text).
	ConfigHash string
	// Timeout bounds the whole call; zero means DefaultTimeout.
	Timeout time.Duration
	// MaxWalk bounds the first-parent commits folded; zero means MaxWalk.
	MaxWalk int
}

// Key identifies the inputs of a fold besides the commit: every Point memoized
// under one Key is comparable. Lookup is the pinned recipe when set, else the
// package name.
type Key struct {
	URL, Lookup, ConfigHash string
	AlgoRev                 int
}

// Key returns the memo key of the request.
func (r Request) Key() Key {
	lookup := r.Lookup
	if r.Pinned != "" {
		lookup = r.Pinned
	}
	return Key{URL: r.URL, Lookup: lookup, ConfigHash: r.ConfigHash, AlgoRev: AlgoRev}
}

// Memo gives access to previously computed Points (the store adapter of the
// resolver, or an in-memory map in tests).
type Memo interface {
	// Nearest returns the memoized Point of the first commit of commits (newest
	// first) that has one under key, or nil when none has. The Point must be
	// the one stored by the caller for that commit; Resolve does not modify it.
	Nearest(ctx context.Context, key Key, commits []string) (*Point, error)
}

// Result is the outcome of Resolve.
type Result struct {
	// Point is the memo of the requested commit; store it for later calls.
	Point
	// GitVrev is the counter of the commit's version: VT[Version].
	GitVrev int
	// Walked is the number of first-parent commits folded in this call
	// (excluding the memoized base); BlobsFetched the recipe blobs requested
	// from the server.
	Walked, BlobsFetched int
	// Truncated reports that the history was longer than MaxWalk and the
	// oldest part was treated as absent.
	Truncated bool
}

// Resolve computes the Result for req.Commit. memo may be nil. The history
// is fetched into a throwaway repository removed before returning. Errors are
// returned without partial results.
func Resolve(ctx context.Context, req Request, memo Memo) (*Result, error) {
	if err := gitrepo.ValidateSource(req.URL, req.Commit); err != nil {
		return nil, err
	}
	if req.Lookup == "" {
		return nil, fmt.Errorf("history: empty package name for %s", req.Commit)
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dir, err := os.MkdirTemp("", tempPrefix+"*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	g := gitrepo.New(dir, req.URL, req.Commit)
	res, err := resolve(ctx, g, req, memo)
	if err != nil {
		return nil, fmt.Errorf("history of commit %s from %s: %w", req.Commit, req.URL, err)
	}
	return res, nil
}

func resolve(ctx context.Context, g *gitrepo.Repo, req Request, memo Memo) (*Result, error) {
	if err := g.Init(ctx); err != nil {
		return nil, err
	}
	if err := g.FetchHistory(ctx); err != nil {
		return nil, err
	}
	list, truncated, err := g.FirstParent(ctx, walkLimit(req.MaxWalk))
	if err != nil {
		return nil, err
	}
	base, err := nearest(ctx, memo, req, list)
	if err != nil {
		return nil, err
	}
	todo := foldList(list, base)
	if len(todo) == 0 {
		p := *base
		p.VT = maps.Clone(base.VT)
		return &Result{Point: p, GitVrev: p.VT[p.Version], Truncated: truncated}, nil
	}
	steps, fetched, err := buildSteps(ctx, g, req, todo)
	if err != nil {
		return nil, err
	}
	tip := fold(base, steps)
	return &Result{Point: tip, GitVrev: tip.VT[tip.Version], Walked: len(steps), BlobsFetched: fetched, Truncated: truncated}, nil
}

// nearest asks the memo for a base among the listed commits; a Point for a
// commit outside the list is ignored.
func nearest(ctx context.Context, memo Memo, req Request, list []string) (*Point, error) {
	if memo == nil {
		return nil, nil
	}
	p, err := memo.Nearest(ctx, req.Key(), list)
	if err != nil {
		return nil, fmt.Errorf("memo: %w", err)
	}
	if p == nil || !slices.Contains(list, p.Commit) {
		return nil, nil
	}
	return p, nil
}

// foldList returns the commits to fold, oldest first: those newer than base.
func foldList(list []string, base *Point) []string {
	end := len(list)
	if base != nil {
		end = slices.Index(list, base.Commit)
	}
	out := slices.Clone(list[:end])
	slices.Reverse(out)
	return out
}

// buildSteps determines the main recipe version of every commit to fold.
func buildSteps(ctx context.Context, g *gitrepo.Repo, req Request, commits []string) ([]step, int, error) {
	perCommit, all, err := recipeSets(ctx, g, req, commits)
	if err != nil {
		return nil, 0, err
	}
	cfg := req.Config
	if cfg == nil {
		if cfg, err = buildconfig.Parse("", "noarch"); err != nil {
			return nil, 0, err
		}
	}
	verdicts, fetched, err := readVerdicts(ctx, g, cfg, req.Lookup, all)
	if err != nil {
		return nil, 0, err
	}
	steps := make([]step, len(commits))
	for i, c := range commits {
		steps[i] = stepFor(c, perCommit[i], verdicts)
	}
	return steps, fetched, nil
}

// recipeSets returns the recipe candidates of each commit (oldest first) and
// the set of all distinct candidates. The file set of the first commit comes
// from its tree; later commits inherit it and apply the recorded changes.
func recipeSets(ctx context.Context, g *gitrepo.Repo, req Request, commits []string) ([][]candidate, map[candidate]bool, error) {
	ents, err := g.Tree(ctx, commits[0])
	if err != nil {
		return nil, nil, err
	}
	files := map[string]string{}
	for _, e := range ents {
		if candidateRe.MatchString(e.Name) {
			files[e.Name] = e.OID
		}
	}
	changes, err := g.RecipeChanges(ctx, commits[0])
	if err != nil {
		return nil, nil, err
	}
	byCommit := make(map[string][]gitrepo.FileChange, len(changes))
	for _, ch := range changes {
		byCommit[ch.Commit] = ch.Files
	}
	all := map[candidate]bool{}
	out := make([][]candidate, len(commits))
	var cur []candidate
	for i, c := range commits {
		if fc, ok := byCommit[c]; ok || i == 0 {
			applyChanges(files, fc)
			cur = candidates(files, req.Lookup, req.Pinned)
			for _, cand := range cur {
				all[cand] = true
			}
		}
		out[i] = cur
	}
	return out, all, nil
}

func applyChanges(files map[string]string, changes []gitrepo.FileChange) {
	for _, fc := range changes {
		if fc.Deleted {
			delete(files, fc.Name)
			continue
		}
		files[fc.Name] = fc.OID
	}
}

func walkLimit(n int) int {
	if n <= 0 {
		return MaxWalk
	}
	return n
}
