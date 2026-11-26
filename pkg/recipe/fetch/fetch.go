// Package fetch retrieves the recipe files of a package at a pinned git
// commit. The scheduler needs only the *.spec files, _multibuild and
// _constraints, not the (possibly huge) source tarballs, so the fetch is
// shallow and blob-filtered, and the result is cached by (url, commit).
package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strconv"

	"github.com/openSUSE/bsgo/pkg/recipe/internal/gitrepo"
)

// Files maps a top-level file name to its content.
type Files map[string][]byte

// Cache stores fetched recipe files by repository URL and commit id. A commit
// id identifies immutable content, but the set of files kept per entry depends
// on the selection rule (selectionID): an implementation must treat entries
// written under another rule as misses.
type Cache interface {
	// Get returns the cached files; ok is false on a miss.
	Get(ctx context.Context, url, commit string) (files Files, ok bool, err error)
	// Put stores the files of a complete fetch.
	Put(ctx context.Context, url, commit string, files Files) error
}

// maxFileSize bounds a single recipe file; larger files are rejected.
const maxFileSize = gitrepo.MaxBlobSize

var (
	// recipeNameRe selects the files the scheduler reads.
	recipeNameRe = regexp.MustCompile(`^(?:[^/\n]+\.spec|_multibuild|_constraints|_service)$`)
)

// selectionRev must be bumped by hand when the file selection changes in a way
// recipeNameRe does not capture (size limits, directory handling, ...).
const selectionRev = 1

// selectionID names the file selection rule: it is derived from recipeNameRe
// and selectionRev, so editing the pattern invalidates every cache entry
// written under the old rule without anyone having to remember it. The S3
// cache embeds it in the key prefix; entries of other rules are never read.
func selectionID() string {
	h := sha256.Sum256([]byte(strconv.Itoa(selectionRev) + "\x00" + recipeNameRe.String()))
	return "v" + hex.EncodeToString(h[:4])
}

// Fetcher fetches recipe files, consulting an optional cache first.
type Fetcher struct {
	// Cache is consulted before and updated after a fetch; nil disables it.
	Cache Cache
}

// Fetch returns the recipe files of repository url at exactly commit. It
// fails when the commit cannot be fetched; it never falls back to a branch
// tip. commit must be a full 40 or 64 digit lowercase hex object id.
func (f *Fetcher) Fetch(ctx context.Context, url, commit string) (Files, error) {
	if err := gitrepo.ValidateSource(url, commit); err != nil {
		return nil, err
	}
	if f.Cache != nil {
		files, ok, err := f.Cache.Get(ctx, url, commit)
		if err != nil {
			return nil, fmt.Errorf("recipe cache get: %w", err)
		}
		if ok {
			return files, nil
		}
	}
	files, err := fetchRecipes(ctx, url, commit)
	if err != nil {
		return nil, err
	}
	if f.Cache != nil {
		if err := f.Cache.Put(ctx, url, commit, files); err != nil {
			return nil, fmt.Errorf("recipe cache put: %w", err)
		}
	}
	return files, nil
}

func fetchRecipes(ctx context.Context, url, commit string) (Files, error) {
	dir, err := os.MkdirTemp("", "bsgo-recipe-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	g := gitrepo.New(dir, url, commit)
	if err := g.Init(ctx); err != nil {
		return nil, err
	}
	if err := g.Fetch(ctx); err != nil {
		return nil, fmt.Errorf("fetch commit %s from %s: %w", commit, url, err)
	}
	return readRecipes(ctx, g)
}

// ObjectFormat returns the git object format implied by a full commit id:
// "sha256" for 64 hex digits, "sha1" otherwise.
func ObjectFormat(commit string) string { return gitrepo.ObjectFormat(commit) }

// readRecipes reads the top-level recipe files of the fetched commit; blobs
// missing from the partial clone are fetched on demand.
func readRecipes(ctx context.Context, g *gitrepo.Repo) (Files, error) {
	ents, err := g.Tree(ctx, gitrepo.CommitRef)
	if err != nil {
		return nil, err
	}
	var names, specs []string
	for _, e := range ents {
		if !recipeNameRe.MatchString(e.Name) {
			continue
		}
		if e.Size > maxFileSize {
			return nil, fmt.Errorf("recipe file %s is too large (%d bytes)", e.Name, e.Size)
		}
		names = append(names, e.Name)
		specs = append(specs, gitrepo.CommitRef+":"+e.Name)
	}
	data, err := g.ReadObjects(ctx, specs)
	if err != nil {
		return nil, err
	}
	files := Files{}
	for i, n := range names {
		files[n] = data[i]
	}
	return files, nil
}
