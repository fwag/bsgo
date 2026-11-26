// Package gittest builds synthetic git repositories for tests: a work tree
// that serves partial clones over a file:// URL, in either object format.
package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Formats are the object formats the tests run against.
var Formats = []string{"sha1", "sha256"}

// Repo is a work repository on branch main.
type Repo struct {
	t    *testing.T
	Dir  string
	Fmt  string
	last string
}

// Each runs fn as a subtest per object format; a format the local git does
// not support is skipped with a message.
func Each(t *testing.T, fn func(t *testing.T, format string)) {
	t.Helper()
	for _, f := range Formats {
		t.Run(f, func(t *testing.T) {
			if err := exec.Command("git", "init", "--quiet", "--object-format="+f, t.TempDir()).Run(); err != nil {
				t.Skipf("git does not support --object-format=%s: %v", f, err)
			}
			fn(t, f)
		})
	}
}

// New creates an empty repository that allows filtered and by-id fetches.
func New(t *testing.T, format string) *Repo {
	t.Helper()
	r := &Repo{t: t, Dir: t.TempDir(), Fmt: format}
	r.Git("init", "-q", "-b", "main", "--object-format="+format)
	r.Git("config", "uploadpack.allowFilter", "true")
	r.Git("config", "uploadpack.allowAnySHA1InWant", "true")
	return r
}

// URL is the file:// URL of the repository.
func (r *Repo) URL() string { return "file://" + r.Dir }

// Git runs git in the work tree and returns its trimmed output.
func (r *Repo) Git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = r.Dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Commit applies files (an empty content deletes the file) and commits all
// changes, allowing an empty commit; it returns the commit id.
func (r *Repo) Commit(msg string, files map[string]string) string {
	r.t.Helper()
	for name, content := range files {
		p := filepath.Join(r.Dir, name)
		if content == "" {
			if err := os.RemoveAll(p); err != nil {
				r.t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			r.t.Fatal(err)
		}
	}
	r.Git("add", "-A")
	r.Git("commit", "-q", "--allow-empty", "-m", msg)
	r.last = r.Git("rev-parse", "HEAD")
	return r.last
}

// Merge creates a side branch with sideCommits commits (each adding a
// side-N.txt file and, when specs is non-empty, rewriting those files), then
// merges it into main with a merge commit; it returns the merge commit id.
func (r *Repo) Merge(sideCommits int, specs map[string]string) string {
	r.t.Helper()
	r.Git("checkout", "-q", "-b", "side")
	for i := 0; i < sideCommits; i++ {
		files := map[string]string{"side-" + string(rune('a'+i)) + ".txt": "x"}
		if i == 0 {
			for k, v := range specs {
				files[k] = v
			}
		}
		r.Commit("side", files)
	}
	r.Git("checkout", "-q", "main")
	r.Git("merge", "-q", "--no-ff", "-m", "merge", "side")
	r.Git("branch", "-q", "-D", "side")
	r.last = r.Git("rev-parse", "HEAD")
	return r.last
}
