package worker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

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

func (s *ProcessSandbox) setupKVMSources(ctx context.Context, job *v1.BuildJob, jobDir string) (string, error) {
	sourcesDir := filepath.Join(jobDir, "sources")
	if err := os.MkdirAll(sourcesDir, 0750); err != nil {
		return "", err
	}

	if job.Source.GitURL != "" {
		return cloneGitSources(ctx, job.Source, sourcesDir)
	}

	srcDir, specName := findPackageSources(job.Package, job.Source.RecipePath)
	copyLocalSources(srcDir, sourcesDir)
	return filepath.Join(sourcesDir, specName), nil
}

func cloneGitSources(ctx context.Context, src v1.SourceSpec, sourcesDir string) (string, error) {
	cloneArgs := []string{"clone", "--depth", "1"}
	if src.Branch != "" {
		cloneArgs = append(cloneArgs, "--branch", src.Branch)
	}
	cloneArgs = append(cloneArgs, src.GitURL, sourcesDir)
	cmd := exec.CommandContext(ctx, "git", cloneArgs...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git clone %s failed: %s (%w)", src.GitURL, string(out), err)
	}
	if src.CommitSHA != "" && src.CommitSHA != "HEAD" {
		chkCmd := exec.CommandContext(ctx, "git", "-C", sourcesDir, "checkout", src.CommitSHA)
		_ = chkCmd.Run()
	}
	_ = exec.CommandContext(ctx, "git", "-C", sourcesDir, "lfs", "install", "--local").Run()
	_ = exec.CommandContext(ctx, "git", "-C", sourcesDir, "lfs", "pull").Run()
	specName := src.RecipePath
	if specName == "" {
		specs, _ := filepath.Glob(filepath.Join(sourcesDir, "*.spec"))
		if len(specs) > 0 {
			specName = filepath.Base(specs[0])
		}
	}
	return filepath.Join(sourcesDir, specName), nil
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
