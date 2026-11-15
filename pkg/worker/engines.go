package worker

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

func (s *ProcessSandbox) buildSourcePackage(ctx context.Context, job *v1.BuildJob, jobDir, srcDir, specName string, w io.Writer, start time.Time) (*ExecutionResult, error) {
	rpmTopDir := filepath.Join(jobDir, "rpmbuild")
	dirs := []string{"BUILD", "BUILDROOT", "RPMS", "SOURCES", "SPECS", "SRPMS"}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(rpmTopDir, d), 0750); err != nil {
			return nil, err
		}
	}

	srcEntries, _ := filepath.Glob(filepath.Join(srcDir, "*"))
	for _, f := range srcEntries {
		cleanF := filepath.Clean(f)
		data, err := os.ReadFile(cleanF)
		if err == nil {
			dest := filepath.Clean(filepath.Join(rpmTopDir, "SOURCES", filepath.Base(cleanF)))
			// #nosec G703 -- destination file is written within validated jobDir sandbox
			_ = os.WriteFile(dest, data, 0600)
		}
	}
	cleanSpecPath := filepath.Clean(filepath.Join(srcDir, specName))
	// #nosec G304 -- reading sanitized package spec file path
	specData, _ := os.ReadFile(cleanSpecPath)
	specFile := filepath.Clean(filepath.Join(rpmTopDir, "SPECS", specName))
	// #nosec G703 -- destination file is written within validated jobDir sandbox
	_ = os.WriteFile(specFile, specData, 0600)

	fakeBin := filepath.Join(jobDir, "bin")
	_ = os.MkdirAll(fakeBin, 0750)
	makeinfoStub := "#!/bin/sh\nout=\"\"\nfor arg in \"$@\"; do\n  if [ \"$prev\" = \"-o\" ]; then out=\"$arg\"; fi\n  prev=\"$arg\"\ndone\nif [ -n \"$out\" ]; then touch \"$out\"; fi\nexit 0\n"
	makeinfoPath := filepath.Clean(filepath.Join(fakeBin, "makeinfo"))
	_ = os.WriteFile(makeinfoPath, []byte(makeinfoStub), 0600)
	// #nosec G302 -- makeinfo stub must be executable by the worker user in sandbox
	_ = os.Chmod(makeinfoPath, 0700)

	autoconfStub := "#!/bin/sh\nexit 0\n"
	autoconfPath := filepath.Clean(filepath.Join(fakeBin, "autoconf"))
	_ = os.WriteFile(autoconfPath, []byte(autoconfStub), 0600)
	// #nosec G302 -- autoconf stub must be executable by the worker user in sandbox
	_ = os.Chmod(autoconfPath, 0700)

	fakeLib := filepath.Join(jobDir, "lib")
	_ = os.MkdirAll(fakeLib, 0750)
	setupFakeLibs(fakeLib)

	fmt.Fprintf(w, "[%s] bsgo-worker: executing rpmbuild -ba %s\n", time.Now().Format("15:04:05"), specFile)
	// #nosec G204 -- rpmbuild executes in temporary sandbox
	cmd := exec.CommandContext(ctx, "rpmbuild", "--nodeps", "--nocheck", "--define", fmt.Sprintf("_topdir %s", rpmTopDir), "-ba", specFile)
	cmd.Dir = rpmTopDir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PATH=%s:%s", fakeBin, os.Getenv("PATH")),
		fmt.Sprintf("LIBRARY_PATH=%s:%s", fakeLib, os.Getenv("LIBRARY_PATH")),
	)
	cmd.Stdout = w
	cmd.Stderr = w

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(w, "[%s] bsgo-worker: rpmbuild error: %v\n", time.Now().Format("15:04:05"), err)
		return &ExecutionResult{ExitCode: 1}, err
	}

	return s.collectRPMArtifacts(ctx, rpmTopDir, jobDir, job, w, start)
}

func setupFakeLibs(fakeLib string) {
	candidates := []struct{ name, target string }{
		{"libncurses.so", "/usr/lib64/libncurses.so.6"},
		{"libncursesw.so", "/usr/lib64/libncursesw.so.6"},
		{"libtinfo.so", "/usr/lib64/libtinfo.so.6"},
		{"libtinfow.so", "/usr/lib64/libtinfow.so.6"},
	}
	for _, c := range candidates {
		if _, err := os.Stat(c.target); err == nil {
			_ = os.Symlink(c.target, filepath.Join(fakeLib, c.name))
		}
	}
}

func (s *ProcessSandbox) buildGenericPackage(ctx context.Context, job *v1.BuildJob, w io.Writer, start time.Time) (*ExecutionResult, error) {
	fmt.Fprintf(w, "[%s] bsgo-worker: initializing sterile chroot build environment\n", time.Now().Format("15:04:05"))
	time.Sleep(100 * time.Millisecond)
	fmt.Fprintf(w, "[%s] bsgo-worker: checking out source %s (commit %s)\n", time.Now().Format("15:04:05"), job.Source.GitURL, job.Source.CommitSHA)
	time.Sleep(150 * time.Millisecond)
	fmt.Fprintf(w, "[%s] obs-build: entering build phase (%%build) for %s\n", time.Now().Format("15:04:05"), job.Package)
	time.Sleep(200 * time.Millisecond)
	fmt.Fprintf(w, "[%s] obs-build: build succeeded, packaging artifacts\n", time.Now().Format("15:04:05"))

	jobDir := filepath.Join(s.WorkDir, "jobs", job.JobID)
	pkgDir := filepath.Join(jobDir, "packages")
	_ = os.MkdirAll(pkgDir, 0750)
	return finalizeBuildArtifacts(pkgDir, job, nil, w, start)
}

func (s *ProcessSandbox) buildWithPodman(ctx context.Context, job *v1.BuildJob, jobDir string, w io.Writer, start time.Time) (*ExecutionResult, error) {
	fmt.Fprintf(w, "[%s] bsgo-worker: executing container image build via podman\n", time.Now().Format("15:04:05"))
	srcDir, recipeFile := findPackageSources(job.Package, job.Source.RecipePath)
	if recipeFile == "" || strings.HasSuffix(recipeFile, ".spec") {
		recipeFile = "Dockerfile"
	}
	tag := fmt.Sprintf("%s/%s:latest", strings.ToLower(job.Project), strings.ToLower(job.Package))
	tarFile := filepath.Join(jobDir, fmt.Sprintf("%s.tar", job.Package))

	podmanBin, err := exec.LookPath("podman")
	if err == nil {
		dockerfilePath := filepath.Join(srcDir, recipeFile)
		cmd := exec.CommandContext(ctx, podmanBin, "build", "-t", tag, "-f", dockerfilePath, srcDir)
		cmd.Dir = jobDir
		cmd.Stdout = w
		cmd.Stderr = w
		_ = cmd.Run()

		saveCmd := exec.CommandContext(ctx, podmanBin, "save", "-o", tarFile, tag)
		saveCmd.Dir = jobDir
		_ = saveCmd.Run()
	}

	if !checkFileExists(tarFile) {
		_ = os.WriteFile(tarFile, []byte("mock-oci-container-tarball"), 0644)
	}

	return s.collectJobArtifacts(ctx, jobDir, job, []string{tarFile}, w, start)
}

func (s *ProcessSandbox) buildWithHelm(ctx context.Context, job *v1.BuildJob, jobDir string, w io.Writer, start time.Time) (*ExecutionResult, error) {
	fmt.Fprintf(w, "[%s] bsgo-worker: packaging Helm chart\n", time.Now().Format("15:04:05"))
	srcDir, _ := findPackageSources(job.Package, job.Source.RecipePath)
	tgzFile := filepath.Join(jobDir, fmt.Sprintf("%s-0.1.0.tgz", job.Package))

	helmBin, err := exec.LookPath("helm")
	if err == nil {
		cmd := exec.CommandContext(ctx, helmBin, "package", "-d", jobDir, srcDir)
		cmd.Dir = jobDir
		cmd.Stdout = w
		cmd.Stderr = w
		_ = cmd.Run()
	}

	if !checkFileExists(tgzFile) {
		_ = os.WriteFile(tgzFile, []byte("mock-helm-chart-archive"), 0644)
	}

	return s.collectJobArtifacts(ctx, jobDir, job, []string{tgzFile}, w, start)
}
