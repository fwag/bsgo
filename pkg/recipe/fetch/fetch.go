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

// commitRef is the local ref that receives the fetched commit.
const commitRef = "refs/bsgo/commit"

var (
	commitRe = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	// recipeNameRe selects the files the scheduler reads.
	recipeNameRe = regexp.MustCompile(`^(?:[^/\n]+\.spec|_multibuild|_constraints)$`)
)

// Fetcher fetches recipe files, consulting an optional cache first.
type Fetcher struct {
	// Cache is consulted before and updated after a fetch; nil disables it.
	Cache Cache
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

func validate(url, commit string) error {
	if url == "" || strings.HasPrefix(url, "-") || strings.ContainsAny(url, "\x00\n") {
		return fmt.Errorf("invalid repository url %q", url)
	}
	if !commitRe.MatchString(commit) {
		return fmt.Errorf("invalid commit id %q: need a full hex object id", commit)
	}
	return nil
}

func fetchRecipes(ctx context.Context, url, commit string) (Files, error) {
	dir, err := os.MkdirTemp("", "bsgo-recipe-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	g := &repo{dir: dir, url: url, commit: commit}
	if err := g.init(ctx); err != nil {
		return nil, err
	}
	if err := g.fetch(ctx); err != nil {
		return nil, fmt.Errorf("fetch commit %s from %s: %w", commit, url, err)
	}
	return g.readRecipes(ctx)
}

// repo is a throwaway repository holding one shallow, blob-filtered commit.
// The repository URL and commit id never appear on a command line: they are
// handed to the VCS tool through its environment configuration (a remote with
// a refspec that names the commit), so every command below has fixed
// arguments.
type repo struct {
	dir, url, commit string
}

// env builds the process environment. The protocol allow-list excludes the
// ext transport and other helper programs.
func (g *repo) env() []string {
	return append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0", "GIT_LFS_SKIP_SMUDGE=1", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_ALLOW_PROTOCOL=file:git:http:https:ssh",
		"GIT_CONFIG_COUNT=4",
		"GIT_CONFIG_KEY_0=remote.src.url", "GIT_CONFIG_VALUE_0="+g.url,
		"GIT_CONFIG_KEY_1=remote.src.fetch", "GIT_CONFIG_VALUE_1="+g.commit+":"+commitRef,
		// "branches" fetches only the branch heads (fetchReachable).
		"GIT_CONFIG_KEY_2=remote.branches.url", "GIT_CONFIG_VALUE_2="+g.url,
		"GIT_CONFIG_KEY_3=remote.branches.fetch", "GIT_CONFIG_VALUE_3=+refs/heads/*:refs/remotes/branches/*",
	)
}

func (g *repo) exec(cmd *exec.Cmd, stdin string) ([]byte, error) {
	cmd.Dir = g.dir
	cmd.Env = g.env()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", cmd.Args[1], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func (g *repo) init(ctx context.Context) error {
	_, err := g.exec(exec.CommandContext(ctx, "git", "init", "--quiet", "--object-format="+ObjectFormat(g.commit)), "")
	return err
}

// ObjectFormat returns the git object format implied by a full commit id:
// "sha256" for 64 hex digits (SHA-256 repositories such as src.opensuse.org
// pool repos), "sha1" otherwise. A repository initialised with the wrong
// format cannot fetch the commit at all.
func ObjectFormat(commit string) string {
	if len(commit) == 64 {
		return "sha256"
	}
	return "sha1"
}

// fetch downloads exactly g.commit; the command fails when the server does
// not have, or will not serve, that object.
func (g *repo) fetch(ctx context.Context) error {
	_, err := g.exec(exec.CommandContext(ctx, "git", "fetch", "--quiet", "--depth", "1", "--filter=blob:none", "--no-tags", "src"), "")
	if err != nil {
		if ferr := g.fetchReachable(ctx); ferr != nil {
			return fmt.Errorf("%w; fetching the branches: %w", err, ferr)
		}
	}
	return g.verify(ctx)
}

// fetchReachable handles servers that only serve advertised refs ("not our
// ref" for a pinned commit that is no longer a branch tip): it fetches the
// branch histories and points commitRef at exactly g.commit,
// which must be reachable from one of them. There is no fallback to a tip.
// g.commit is a validated full hex id, so it is safe as an argument.
func (g *repo) fetchReachable(ctx context.Context) error {
	// No blob filter: a server that refuses unadvertised commits also refuses
	// the lazy blob fetches a partial clone would need later.
	if _, err := g.exec(exec.CommandContext(ctx, "git", "fetch", "--quiet", "--no-tags", "branches"), ""); err != nil {
		return err
	}
	_, err := g.exec(exec.CommandContext(ctx, "git", "update-ref", commitRef, g.commit), "")
	return err
}

// verify checks that commitRef resolves to exactly g.commit.
func (g *repo) verify(ctx context.Context) error {
	out, err := g.exec(exec.CommandContext(ctx, "git", "rev-parse", "--verify", commitRef+"^{commit}"), "")
	if err != nil {
		return err
	}
	if got := strings.TrimSpace(string(out)); got != g.commit {
		return fmt.Errorf("fetched %s, want %s", got, g.commit)
	}
	return nil
}

// readRecipes reads the top-level recipe files of the fetched commit.
func (g *repo) readRecipes(ctx context.Context) (Files, error) {
	out, err := g.exec(exec.CommandContext(ctx, "git", "ls-tree", "-z", "--long", commitRef+"^{tree}"), "")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, ent := range bytes.Split(out, []byte{0}) {
		name, size, ok := parseTreeEntry(string(ent))
		if !ok || !recipeNameRe.MatchString(name) {
			continue
		}
		if size > maxFileSize {
			return nil, fmt.Errorf("recipe file %s is too large (%d bytes)", name, size)
		}
		names = append(names, name)
	}
	return g.readBlobs(ctx, names)
}

// readBlobs reads the named blobs with one cat-file process; blobs missing
// from a partial clone are fetched from the remote on demand.
func (g *repo) readBlobs(ctx context.Context, names []string) (Files, error) {
	files := Files{}
	if len(names) == 0 {
		return files, nil
	}
	var in strings.Builder
	for _, n := range names {
		in.WriteString(commitRef + ":" + n + "\n")
	}
	out, err := g.exec(exec.CommandContext(ctx, "git", "cat-file", "--batch"), in.String())
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		var data []byte
		if data, out, err = nextBlob(out, n); err != nil {
			return nil, err
		}
		files[n] = data
	}
	return files, nil
}

// nextBlob splits one "<oid> blob <size>\n<content>\n" record off out.
func nextBlob(out []byte, name string) (data, rest []byte, err error) {
	hdr, rest, ok := bytes.Cut(out, []byte{'\n'})
	f := strings.Fields(string(hdr))
	if !ok || len(f) != 3 || f[1] != "blob" {
		return nil, nil, fmt.Errorf("unexpected cat-file output for %s: %q", name, hdr)
	}
	size, err := strconv.Atoi(f[2])
	if err != nil || size < 0 || len(rest) < size+1 {
		return nil, nil, fmt.Errorf("bad cat-file size for %s: %q", name, hdr)
	}
	return append([]byte(nil), rest[:size]...), rest[size+1:], nil
}

// parseTreeEntry parses a "ls-tree --long" record for a blob:
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
