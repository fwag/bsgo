// Package gitrepo drives the git command line for the recipe packages: a
// throwaway repository that fetches one pinned commit (or its first-parent
// history) with a blob filter and reads recipe files from it.
//
// Safety model: the repository URL and commit id never appear on a command
// line. They are handed to git through GIT_CONFIG_* environment entries (a
// remote whose refspec names the commit), so every command has fixed
// arguments. The transport allow-list excludes ext and other helper programs,
// LFS smudging is off and git never prompts.
package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
)

// CommitRef is the local ref that receives the fetched commit.
const CommitRef = "refs/bsgo/commit"

// MaxBlobSize bounds a single blob read; larger blobs are rejected.
const MaxBlobSize = 16 << 20

// maxOutput bounds the captured stdout of one git command.
const maxOutput = 64 << 20

var idRe = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// ValidID reports whether s is a full lowercase hex object id (SHA-1 or
// SHA-256).
func ValidID(s string) bool { return idRe.MatchString(s) }

// ValidateSource checks a repository url and commit id before they are used.
func ValidateSource(url, commit string) error {
	if url == "" || strings.HasPrefix(url, "-") || strings.ContainsAny(url, "\x00\n") {
		return fmt.Errorf("invalid repository url %q", url)
	}
	if !ValidID(commit) {
		return fmt.Errorf("invalid commit id %q: need a full hex object id", commit)
	}
	return nil
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

// Repo is a throwaway repository in Dir for one remote URL and commit.
type Repo struct {
	dir, url, commit string
}

// New returns a Repo; call Init before anything else. url and commit must
// have passed ValidateSource.
func New(dir, url, commit string) *Repo {
	return &Repo{dir: dir, url: url, commit: commit}
}

// AllowProtocols is the GIT_ALLOW_PROTOCOL of every git command. Production
// fetches only over http(s); tests that serve repositories from disk widen it.
var AllowProtocols = "https:http"

// env builds the process environment.
func (g *Repo) env(noLazy bool) []string {
	env := append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0", "GIT_LFS_SKIP_SMUDGE=1", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_ALLOW_PROTOCOL="+AllowProtocols,
		"GIT_CONFIG_COUNT=4",
		"GIT_CONFIG_KEY_0=remote.src.url", "GIT_CONFIG_VALUE_0="+g.url,
		"GIT_CONFIG_KEY_1=remote.src.fetch", "GIT_CONFIG_VALUE_1="+g.commit+":"+CommitRef,
		// "branches" fetches only the branch heads (fetchReachable).
		"GIT_CONFIG_KEY_2=remote.branches.url", "GIT_CONFIG_VALUE_2="+g.url,
		"GIT_CONFIG_KEY_3=remote.branches.fetch", "GIT_CONFIG_VALUE_3=+refs/heads/*:refs/remotes/branches/*",
	)
	if noLazy {
		// A missing blob of a partial clone is an error, never a silent
		// per-object round trip (git >= 2.44).
		env = append(env, "GIT_NO_LAZY_FETCH=1")
	}
	return env
}

type limitedBuffer struct {
	bytes.Buffer
	over bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxOutput {
		b.over = true
		return 0, errors.New("git output too large")
	}
	return b.Buffer.Write(p)
}

// Exec runs cmd in the repository with the safe environment and returns its
// stdout. Lazy object fetches are allowed (see ReadObjects).
func (g *Repo) Exec(cmd *exec.Cmd, stdin string) ([]byte, error) {
	return g.run(cmd, stdin, false)
}

func (g *Repo) run(cmd *exec.Cmd, stdin string, noLazy bool) ([]byte, error) {
	cmd.Dir = g.dir
	cmd.Env = g.env(noLazy)
	var stdout limitedBuffer
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", cmd.Args[1], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// git runs a fixed git command with lazy fetches forbidden.
func (g *Repo) git(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	return g.run(exec.CommandContext(ctx, "git", args...), stdin, true)
}

// Init creates the repository with the object format of the commit.
func (g *Repo) Init(ctx context.Context) error {
	_, err := g.Exec(exec.CommandContext(ctx, "git", "init", "--quiet", "--object-format="+ObjectFormat(g.commit)), "")
	return err
}

// Fetch downloads exactly the commit, shallow and without blobs; the command
// fails when the server does not have, or will not serve, that object.
func (g *Repo) Fetch(ctx context.Context) error {
	return g.fetchPinned(ctx, "--depth", "1", "--filter=blob:none")
}

// FetchHistory downloads the commit with its complete history but without
// blobs (trees and commits only), so the history can be walked and recipe
// blobs fetched selectively with FetchBlobs.
func (g *Repo) FetchHistory(ctx context.Context) error {
	return g.fetchPinned(ctx, "--filter=blob:none")
}

func (g *Repo) fetchPinned(ctx context.Context, opts ...string) error {
	args := append([]string{"fetch", "--quiet"}, opts...)
	args = append(args, "--no-tags", "src")
	if _, err := g.Exec(exec.CommandContext(ctx, "git", args...), ""); err != nil {
		if ferr := g.fetchReachable(ctx, slices.Contains(opts, "--filter=blob:none") && !slices.Contains(opts, "--depth")); ferr != nil {
			return fmt.Errorf("%w; fetching the branches: %w", err, ferr)
		}
	}
	return g.verify(ctx)
}

// fetchReachable handles servers that only serve advertised refs ("not our
// ref" for a pinned commit that is no longer a branch tip): it fetches the
// branch histories and points CommitRef at exactly the commit, which must be
// reachable from one of them. There is no fallback to a tip. The commit is a
// validated full hex id, so it is safe as an argument.
//
// A history fetch (blobless) keeps the filter, so the fallback does not
// download the content of every branch. A single-commit fetch does not: a
// server that refuses unadvertised commits also refuses the lazy blob fetches
// a partial clone would need later.
func (g *Repo) fetchReachable(ctx context.Context, blobless bool) error {
	args := []string{"fetch", "--quiet", "--no-tags"}
	if blobless {
		args = append(args, "--filter=blob:none")
	}
	if _, err := g.Exec(exec.CommandContext(ctx, "git", append(args, "branches")...), ""); err != nil {
		return err
	}
	_, err := g.Exec(exec.CommandContext(ctx, "git", "update-ref", CommitRef, g.commit), "")
	return err
}

// verify checks that CommitRef resolves to exactly the commit.
func (g *Repo) verify(ctx context.Context) error {
	out, err := g.Exec(exec.CommandContext(ctx, "git", "rev-parse", "--verify", CommitRef+"^{commit}"), "")
	if err != nil {
		return err
	}
	if got := strings.TrimSpace(string(out)); got != g.commit {
		return fmt.Errorf("fetched %s, want %s", got, g.commit)
	}
	return nil
}
