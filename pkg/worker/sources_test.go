package worker

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// bareRepoWithHistory creates a bare repo with three commits on main and
// returns its path plus the commit ids, oldest first.
func bareRepoWithHistory(t *testing.T) (string, []string) {
	t.Helper()
	work := t.TempDir()
	gitT(t, work, "init", "-q", "-b", "main")
	var ids []string
	for _, content := range []string{"one", "two", "three"} {
		if err := os.WriteFile(filepath.Join(work, "pkg.spec"), []byte("Name: "+content+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitT(t, work, "add", ".")
		gitT(t, work, "commit", "-q", "-m", content)
		ids = append(ids, gitT(t, work, "rev-parse", "HEAD"))
	}
	bare := filepath.Join(t.TempDir(), "repo.git")
	gitT(t, work, "clone", "-q", "--bare", work, bare)
	return bare, ids
}

func TestCheckoutCommitExactNonTip(t *testing.T) {
	bare, ids := bareRepoWithHistory(t)
	for i, id := range ids {
		dir := t.TempDir()
		got, err := checkoutCommit(context.Background(), bare, id, dir, io.Discard)
		if err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
		if got != id {
			t.Errorf("resolved %s, want %s", got, id)
		}
		data, _ := os.ReadFile(filepath.Join(dir, "pkg.spec"))
		want := []string{"Name: one\n", "Name: two\n", "Name: three\n"}[i]
		if string(data) != want {
			t.Errorf("commit %d checked out %q, want %q", i, data, want)
		}
	}
}

func TestCheckoutCommitFailures(t *testing.T) {
	bare, _ := bareRepoWithHistory(t)
	missing := strings.Repeat("0", 40)
	tests := []struct {
		name, url, ref string
	}{
		{"unknown commit", bare, missing},
		{"unreachable repository", filepath.Join(t.TempDir(), "nope.git"), missing},
		{"option-like url", "--upload-pack=touch /tmp/x", missing},
		{"option-like ref", bare, "--all"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := checkoutCommit(context.Background(), tc.url, tc.ref, t.TempDir(), io.Discard); err == nil {
				t.Fatal("expected failure")
			}
		})
	}
}

func TestSetupKVMSourcesBuildInfoPinsCommit(t *testing.T) {
	bare, ids := bareRepoWithHistory(t)
	s := &ProcessSandbox{WorkDir: t.TempDir()}
	job := &v1.BuildJob{
		JobID:     "j1",
		Source:    v1.SourceSpec{GitURL: bare, Branch: "main", CommitSHA: ids[2], RecipePath: "pkg.spec"},
		BuildInfo: &v1.BuildInfo{SourceCommit: ids[0]},
	}
	spec, commit, err := s.setupKVMSources(context.Background(), job, t.TempDir(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if commit != ids[0] {
		t.Errorf("commit = %s, want BuildInfo.SourceCommit %s", commit, ids[0])
	}
	if data, _ := os.ReadFile(spec); string(data) != "Name: one\n" {
		t.Errorf("spec = %q", data)
	}
}

func TestSetupKVMSourcesFailsOnBadCommit(t *testing.T) {
	bare, _ := bareRepoWithHistory(t)
	s := &ProcessSandbox{WorkDir: t.TempDir()}
	job := &v1.BuildJob{
		Source:    v1.SourceSpec{GitURL: bare, RecipePath: "pkg.spec"},
		BuildInfo: &v1.BuildInfo{SourceCommit: strings.Repeat("1", 40)},
	}
	if _, _, err := s.setupKVMSources(context.Background(), job, t.TempDir(), io.Discard); err == nil {
		t.Fatal("expected checkout failure to fail the job")
	}
}

func commitFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "add", ".")
	gitT(t, dir, "commit", "-q", "-m", "c")
}

func TestPullLFS(t *testing.T) {
	plain := t.TempDir()
	gitT(t, plain, "init", "-q")
	commitFile(t, plain, "pkg.spec", "Name: x\n")
	if err := pullLFS(context.Background(), plain, io.Discard); err != nil {
		t.Errorf("repo without lfs config must be skipped cleanly: %v", err)
	}
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Skip("git-lfs not installed")
	}
	// With an LFS pointer whose object cannot be fetched the failure must surface
	// instead of being ignored.
	withLFS := t.TempDir()
	gitT(t, withLFS, "init", "-q")
	commitFile(t, withLFS, ".gitattributes", "*.bin filter=lfs diff=lfs merge=lfs -text\n")
	pointer := "version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat("ab", 32) + "\nsize 5\n"
	commitFile(t, withLFS, "a.bin", pointer)
	gitT(t, withLFS, "remote", "add", "origin", filepath.Join(t.TempDir(), "missing.git"))
	if err := pullLFS(context.Background(), withLFS, io.Discard); err == nil {
		t.Error("lfs pull of an unfetchable object must fail, not be swallowed")
	}
}
