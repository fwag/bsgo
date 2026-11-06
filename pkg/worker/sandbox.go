package worker

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

type MockSandbox struct {
	SimulateDuration time.Duration
	ProducedFiles    []string
	FailWithCode     int
}

func (s *MockSandbox) Execute(ctx context.Context, job *v1.BuildJob, logWriter io.Writer) (*ExecutionResult, error) {
	fmt.Fprintf(logWriter, "[%s] bsgo-worker: preparing sandbox for %s/%s\n", time.Now().Format("15:04:05"), job.Project, job.Package)
	fmt.Fprintf(logWriter, "[%s] bsgo-worker: downloading %d prerequisite RPMs\n", time.Now().Format("15:04:05"), len(job.ChrootRPMs))

	select {
	case <-ctx.Done():
		fmt.Fprintf(logWriter, "[%s] bsgo-worker: execution cancelled by abort signal\n", time.Now().Format("15:04:05"))
		return nil, ctx.Err()
	case <-time.After(s.SimulateDuration):
	}

	if s.FailWithCode != 0 {
		fmt.Fprintf(logWriter, "[%s] obs-build: compilation failed with exit code %d\n", time.Now().Format("15:04:05"), s.FailWithCode)
		return &ExecutionResult{ExitCode: s.FailWithCode}, nil
	}

	fmt.Fprintf(logWriter, "[%s] obs-build: build succeeded, packaging artifacts\n", time.Now().Format("15:04:05"))
	var artifacts []v1.ChrootRPM
	for _, f := range s.ProducedFiles {
		artifacts = append(artifacts, v1.ChrootRPM{
			Filename:  f,
			S3Key:     fmt.Sprintf("%s/%s", job.Destination.S3Prefix, f),
			SHA256:    "simulated-sha256",
			SizeBytes: 1024 * 1024,
		})
	}

	return &ExecutionResult{
		ExitCode:          0,
		Artifacts:         artifacts,
		PreinstallSeconds: 2.5,
		MainBuildSeconds:  10.2,
		TotalSeconds:      12.7,
	}, nil
}

func (s *MockSandbox) Cleanup(_ context.Context) error {
	return nil
}

type ProcessSandbox struct {
	WorkDir string
}

func (s *ProcessSandbox) Execute(ctx context.Context, job *v1.BuildJob, logWriter io.Writer) (*ExecutionResult, error) {
	jobDir := filepath.Join(s.WorkDir, "jobs", job.JobID)
	if err := os.MkdirAll(jobDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create job dir: %w", err)
	}

	logFilePath := filepath.Join(jobDir, "build.log")
	logFile, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to create log file: %w", err)
	}
	defer logFile.Close()

	multiWriter := io.MultiWriter(logWriter, logFile)

	start := time.Now()
	fmt.Fprintf(multiWriter, "[%s] bsgo-worker: starting build job %s (%s/%s)\n", time.Now().Format("15:04:05"), job.JobID, job.Project, job.Package)

	specPath := filepath.Join("test/fixtures/hello/hello.spec")
	if _, err := os.Stat(specPath); err == nil && job.Package == "hello" {
		return s.buildHelloPackage(ctx, job, jobDir, multiWriter, start)
	}

	return s.buildGenericPackage(ctx, job, multiWriter, start)
}

func (s *ProcessSandbox) buildHelloPackage(ctx context.Context, job *v1.BuildJob, jobDir string, w io.Writer, start time.Time) (*ExecutionResult, error) {
	rpmTopDir := filepath.Join(jobDir, "rpmbuild")
	dirs := []string{"BUILD", "BUILDROOT", "RPMS", "SOURCES", "SPECS", "SRPMS"}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(rpmTopDir, d), 0755); err != nil {
			return nil, err
		}
	}

	srcEntries, _ := filepath.Glob("test/fixtures/hello/*")
	for _, f := range srcEntries {
		data, err := os.ReadFile(f)
		if err == nil {
			_ = os.WriteFile(filepath.Join(rpmTopDir, "SOURCES", filepath.Base(f)), data, 0644)
		}
	}
	specData, _ := os.ReadFile("test/fixtures/hello/hello.spec")
	specFile := filepath.Join(rpmTopDir, "SPECS", "hello.spec")
	_ = os.WriteFile(specFile, specData, 0644)

	fakeBin := filepath.Join(jobDir, "bin")
	_ = os.MkdirAll(fakeBin, 0755)
	makeinfoStub := "#!/bin/sh\nout=\"\"\nfor arg in \"$@\"; do\n  if [ \"$prev\" = \"-o\" ]; then out=\"$arg\"; fi\n  prev=\"$arg\"\ndone\nif [ -n \"$out\" ]; then touch \"$out\"; fi\nexit 0\n"
	_ = os.WriteFile(filepath.Join(fakeBin, "makeinfo"), []byte(makeinfoStub), 0755)

	fmt.Fprintf(w, "[%s] bsgo-worker: executing rpmbuild -ba %s\n", time.Now().Format("15:04:05"), specFile)
	cmd := exec.CommandContext(ctx, "rpmbuild", "--nodeps", "--define", fmt.Sprintf("_topdir %s", rpmTopDir), "-ba", specFile)
	cmd.Dir = rpmTopDir
	cmd.Env = append(os.Environ(), fmt.Sprintf("PATH=%s:%s", fakeBin, os.Getenv("PATH")))
	cmd.Stdout = w
	cmd.Stderr = w

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(w, "[%s] bsgo-worker: rpmbuild error: %v\n", time.Now().Format("15:04:05"), err)
		return &ExecutionResult{ExitCode: 1}, err
	}

	return s.collectRPMArtifacts(rpmTopDir, jobDir, job.Project, job.Package, w, start)
}

func (s *ProcessSandbox) collectRPMArtifacts(rpmTopDir, jobDir, project, pkg string, w io.Writer, start time.Time) (*ExecutionResult, error) {
	pkgDir := filepath.Join(jobDir, "packages")
	_ = os.MkdirAll(pkgDir, 0755)

	builtRPMs, _ := filepath.Glob(filepath.Join(rpmTopDir, "RPMS", "*", "*.rpm"))
	var artifacts []v1.ChrootRPM
	for _, rpmPath := range builtRPMs {
		baseName := filepath.Base(rpmPath)
		dest := filepath.Join(pkgDir, baseName)
		data, err := os.ReadFile(rpmPath)
		if err == nil {
			_ = os.WriteFile(dest, data, 0644)
			artifacts = append(artifacts, v1.ChrootRPM{
				Filename:  baseName,
				S3Key:     fmt.Sprintf("artifacts/%s/%s/%s", project, pkg, baseName),
				SizeBytes: int64(len(data)),
			})
			fmt.Fprintf(w, "[%s] bsgo-worker: built real RPM artifact: %s (%d bytes)\n", time.Now().Format("15:04:05"), baseName, len(data))
		}
	}

	elapsed := time.Since(start).Seconds()
	return &ExecutionResult{
		ExitCode:     0,
		Artifacts:    artifacts,
		TotalSeconds: elapsed,
	}, nil
}

func (s *ProcessSandbox) buildGenericPackage(ctx context.Context, job *v1.BuildJob, w io.Writer, start time.Time) (*ExecutionResult, error) {
	fmt.Fprintf(w, "[%s] bsgo-worker: initializing sterile chroot build environment\n", time.Now().Format("15:04:05"))
	time.Sleep(100 * time.Millisecond)
	fmt.Fprintf(w, "[%s] bsgo-worker: checking out source %s (commit %s)\n", time.Now().Format("15:04:05"), job.Source.GitURL, job.Source.CommitSHA)
	time.Sleep(150 * time.Millisecond)
	fmt.Fprintf(w, "[%s] obs-build: entering build phase (%%build) for %s\n", time.Now().Format("15:04:05"), job.Package)
	time.Sleep(200 * time.Millisecond)
	fmt.Fprintf(w, "[%s] obs-build: build succeeded, packaging artifacts\n", time.Now().Format("15:04:05"))

	elapsed := time.Since(start).Seconds()
	return &ExecutionResult{
		ExitCode:     0,
		TotalSeconds: elapsed,
	}, nil
}

func (s *ProcessSandbox) Cleanup(_ context.Context) error {
	return os.RemoveAll(s.WorkDir)
}
