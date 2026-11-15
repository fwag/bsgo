package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

func findPackageSources(pkgName, recipePath string) (string, string) {
	candidates := []string{
		filepath.Join("test/fixtures/demo-fast", pkgName),
		filepath.Join("test/fixtures/demo", pkgName),
		filepath.Join("test/fixtures", pkgName),
		filepath.Join("../../test/fixtures/demo-fast", pkgName),
		filepath.Join("../../test/fixtures/demo", pkgName),
		filepath.Join("../../test/fixtures", pkgName),
	}
	for _, dir := range candidates {
		if stat, err := os.Stat(dir); err == nil && stat.IsDir() {
			spec := recipePath
			if spec == "" {
				spec = pkgName + ".spec"
			}
			specPath := filepath.Join(dir, spec)
			if _, err := os.Stat(specPath); err == nil {
				return dir, spec
			}
			specs, _ := filepath.Glob(filepath.Join(dir, "*.spec"))
			if len(specs) > 0 {
				return dir, filepath.Base(specs[0])
			}
			return dir, spec
		}
	}
	return "test/fixtures/hello", "hello.spec"
}

// setupKVMSources prepares jobDir/sources and returns the recipe path and the
// exact source commit (empty for local fixture sources).
func (s *ProcessSandbox) setupKVMSources(ctx context.Context, job *v1.BuildJob, jobDir string, w io.Writer) (specFile, commit string, err error) {
	sourcesDir := filepath.Join(jobDir, "sources")
	if err := os.MkdirAll(sourcesDir, 0o750); err != nil {
		return "", "", err
	}

	if job.Source.GitURL != "" {
		return checkoutGitSources(ctx, job, sourcesDir, w)
	}

	srcDir, specName := findPackageSources(job.Package, job.Source.RecipePath)
	copyLocalSources(srcDir, sourcesDir)
	return filepath.Join(sourcesDir, specName), "", nil
}

// requestedCommit is the commit a job must be built at: BuildInfo.SourceCommit
// for hermetic jobs, the legacy Source.CommitSHA otherwise.
func requestedCommit(job *v1.BuildJob) string {
	if job.BuildInfo != nil {
		return job.BuildInfo.SourceCommit
	}
	return job.Source.CommitSHA
}

func checkoutGitSources(ctx context.Context, job *v1.BuildJob, sourcesDir string, w io.Writer) (specFile, commit string, err error) {
	src := job.Source
	ref := requestedCommit(job)
	if job.BuildInfo == nil && (ref == "" || ref == "HEAD") {
		// Legacy jobs without a pinned commit: fetch the branch tip.
		ref = src.Branch
		if ref == "" {
			ref = "HEAD"
		}
	}
	commit, err = checkoutCommit(ctx, src.GitURL, ref, sourcesDir, w)
	if err != nil {
		return "", "", err
	}
	if err := pullLFS(ctx, sourcesDir, w); err != nil {
		return "", "", err
	}
	specName := src.RecipePath
	if specName == "" {
		specs, _ := filepath.Glob(filepath.Join(sourcesDir, "*.spec"))
		if len(specs) > 0 {
			specName = filepath.Base(specs[0])
		}
	}
	return filepath.Join(sourcesDir, specName), commit, nil
}

// runGit runs one git command in dir. Protocols are restricted so a job can
// not smuggle in transports like ext:: and nothing ever prompts.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	// #nosec G204 -- fixed binary; url and ref are rejected when option-like in checkoutCommit
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ALLOW_PROTOCOL=file:git:http:https:ssh",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %s (%w)", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// checkoutCommit populates dir with exactly the requested commit: init, a
// shallow fetch of that single object id (no branch tip assumption), and a
// checkout of FETCH_HEAD. Every step is fatal. It returns the resolved HEAD.
func checkoutCommit(ctx context.Context, gitURL, ref, dir string, w io.Writer) (string, error) {
	if strings.HasPrefix(gitURL, "-") || strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("refusing option-like git url or ref: %q %q", gitURL, ref)
	}
	steps := [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", gitURL},
		{"fetch", "-q", "--depth", "1", "origin", ref},
		{"checkout", "-q", "FETCH_HEAD"},
	}
	for _, args := range steps {
		if _, err := runGit(ctx, dir, args...); err != nil {
			return "", err
		}
	}
	head, err := runGit(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if commitIDPattern.MatchString(ref) && head != ref {
		return "", fmt.Errorf("checked out %s but %s was requested", head, ref)
	}
	fmt.Fprintf(w, "[%s] bsgo-worker: checked out %s at %s\n", time.Now().Format("15:04:05"), gitURL, head)
	return head, nil
}

// usesLFS reports whether any .gitattributes of the checked out commit routes
// files through the LFS filter. git grep exits 1 when nothing matches.
func usesLFS(ctx context.Context, dir string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "grep", "-l", "-e", "filter=lfs", "HEAD", "--", ":(glob)**/.gitattributes")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git grep for lfs config failed: %s (%w)", strings.TrimSpace(string(out)), err)
}

// pullLFS fetches LFS objects when the repository uses LFS and does nothing
// otherwise. Failures are fatal: a build must not see LFS pointer files.
func pullLFS(ctx context.Context, dir string, w io.Writer) error {
	lfs, err := usesLFS(ctx, dir)
	if err != nil {
		return fmt.Errorf("scanning for lfs config: %w", err)
	}
	if !lfs {
		return nil
	}
	for _, args := range [][]string{{"lfs", "install", "--local"}, {"lfs", "pull"}} {
		if _, err := runGit(ctx, dir, args...); err != nil {
			return err
		}
	}
	fmt.Fprintf(w, "[%s] bsgo-worker: lfs objects pulled\n", time.Now().Format("15:04:05"))
	return nil
}

func copyLocalSources(srcDir, sourcesDir string) {
	srcEntries, _ := filepath.Glob(filepath.Join(srcDir, "*"))
	for _, f := range srcEntries {
		cleanF := filepath.Clean(f)
		data, err := os.ReadFile(cleanF)
		if err == nil {
			destFile := filepath.Clean(filepath.Join(sourcesDir, filepath.Base(cleanF)))
			// #nosec G703 -- destination file is written within validated jobDir/sources sandbox
			_ = os.WriteFile(destFile, data, 0600)
		}
	}
}
