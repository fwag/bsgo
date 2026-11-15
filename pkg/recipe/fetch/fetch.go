// Package fetch retrieves the recipe files of a package at a pinned git
// commit. The scheduler needs only the *.spec files, _multibuild and
// _constraints, not the (possibly huge) source tarballs, so the fetch is
// shallow and blob-filtered, and the result is cached by (url, commit).
package fetch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Files maps a top-level file name to its content.
type Files map[string][]byte

// Cache stores fetched recipe files by repository URL and commit id. A commit
// id identifies immutable content, so entries never need invalidation.
type Cache interface {
	// Get returns the cached files; ok is false on a miss.
	Get(ctx context.Context, url, commit string) (files Files, ok bool, err error)
	// Put stores the files of a complete fetch.
	Put(ctx context.Context, url, commit string, files Files) error
}

// maxFileSize bounds a single recipe file; larger files are rejected.
const maxFileSize = 16 << 20

var (
	commitRe = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	// recipeNameRe selects the files the scheduler reads.
	recipeNameRe = regexp.MustCompile(`^(?:[^/]+\.spec|_multibuild|_constraints)$`)
)

// Fetcher fetches recipe files, consulting an optional cache first.
type Fetcher struct {
	// Cache is consulted before and updated after a fetch; nil disables it.
	Cache Cache
	// GitBin is the git executable; empty means "git".
	GitBin string
}

// Fetch returns the recipe files of repository url at exactly commit. It
// fails when the commit cannot be fetched; it never falls back to a branch
// tip. commit must be a full 40 or 64 digit lowercase hex object id.
func (f *Fetcher) Fetch(ctx context.Context, url, commit string) (Files, error) {
	if err := validate(url, commit); err != nil {
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
	files, err := f.fetchGit(ctx, url, commit)
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

func validate(url, commit string) error {
	if url == "" || strings.HasPrefix(url, "-") || strings.ContainsAny(url, "\x00\n") {
		return fmt.Errorf("invalid repository url %q", url)
	}
	if !commitRe.MatchString(commit) {
		return fmt.Errorf("invalid commit id %q: need a full hex object id", commit)
	}
	return nil
}

func (f *Fetcher) fetchGit(ctx context.Context, url, commit string) (Files, error) {
	dir, err := os.MkdirTemp("", "bsgo-recipe-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	g := &gitRunner{bin: f.GitBin, dir: dir}
	if g.bin == "" {
		g.bin = "git"
	}
	if _, err := g.run(ctx, "init", "--quiet"); err != nil {
		return nil, err
	}
	if _, err := g.run(ctx, "fetch", "--quiet", "--depth", "1", "--filter=blob:none", "--no-tags", "--", url, commit); err != nil {
		return nil, fmt.Errorf("fetch commit %s from %s: %w", commit, url, err)
	}
	return g.readRecipes(ctx, commit)
}

// readRecipes reads the top-level recipe files of commit.
func (g *gitRunner) readRecipes(ctx context.Context, commit string) (Files, error) {
	out, err := g.run(ctx, "ls-tree", "-z", "--long", commit+"^{tree}")
	if err != nil {
		return nil, err
	}
	files := Files{}
	for _, ent := range bytes.Split(out, []byte{0}) {
		name, size, ok := parseTreeEntry(string(ent))
		if !ok || !recipeNameRe.MatchString(name) {
			continue
		}
		if size > maxFileSize {
			return nil, fmt.Errorf("recipe file %s is too large (%d bytes)", name, size)
		}
		data, err := g.run(ctx, "cat-file", "blob", commit+":"+name)
		if err != nil {
			return nil, err
		}
		files[name] = data
	}
	return files, nil
}

// parseTreeEntry parses a "git ls-tree --long" record for a blob:
// "<mode> blob <oid> <size>\t<name>".
func parseTreeEntry(ent string) (name string, size int64, ok bool) {
	meta, name, found := strings.Cut(ent, "\t")
	if !found {
		return "", 0, false
	}
	f := strings.Fields(meta)
	if len(f) != 4 || f[1] != "blob" || path.Base(name) != name {
		return "", 0, false
	}
	size, err := strconv.ParseInt(f[3], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return name, size, true
}

type gitRunner struct {
	bin string
	dir string
}

func (g *gitRunner) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, g.bin, args...)
	cmd.Dir = g.dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_LFS_SKIP_SMUDGE=1", "GIT_CONFIG_NOSYSTEM=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
