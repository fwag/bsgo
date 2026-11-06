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

	// Check if this is the real GNU hello package
	tarballPath := filepath.Join("test/fixtures/hello/hello-2.12.3.tar.gz")
	if _, err := os.Stat(tarballPath); err == nil && job.Package == "hello" {
		return s.buildHelloPackage(ctx, job, jobDir, tarballPath, multiWriter, start)
	}

	// Default fallback: simulate step-by-step build with progress
	return s.buildGenericPackage(ctx, job, multiWriter, start)
}

func (s *ProcessSandbox) buildHelloPackage(ctx context.Context, job *v1.BuildJob, jobDir, tarball string, w io.Writer, start time.Time) (*ExecutionResult, error) {
	fmt.Fprintf(w, "[%s] bsgo-worker: unpacking upstream tarball %s\n", time.Now().Format("15:04:05"), tarball)

	tarCmd := exec.CommandContext(ctx, "tar", "-xf", tarball, "-C", jobDir)
	tarCmd.Stdout = w
	tarCmd.Stderr = w
	if err := tarCmd.Run(); err != nil {
		return &ExecutionResult{ExitCode: 1}, err
	}

	srcDir := filepath.Join(jobDir, "hello-2.12.3")
	fmt.Fprintf(w, "[%s] bsgo-worker: running ./configure --prefix=/usr\n", time.Now().Format("15:04:05"))
	confCmd := exec.CommandContext(ctx, "./configure", "--prefix=/usr")
	confCmd.Dir = srcDir
	confCmd.Stdout = w
	confCmd.Stderr = w
	if err := confCmd.Run(); err != nil {
		return &ExecutionResult{ExitCode: 1}, err
	}

	fmt.Fprintf(w, "[%s] bsgo-worker: compiling C sources with make\n", time.Now().Format("15:04:05"))
	makeCmd := exec.CommandContext(ctx, "make")
	makeCmd.Dir = srcDir
	makeCmd.Stdout = w
	makeCmd.Stderr = w
	if err := makeCmd.Run(); err != nil {
		return &ExecutionResult{ExitCode: 1}, err
	}

	fmt.Fprintf(w, "[%s] bsgo-worker: build completed successfully! Packaging RPM...\n", time.Now().Format("15:04:05"))
	pkgDir := filepath.Join(jobDir, "packages")
	_ = os.MkdirAll(pkgDir, 0755)
	builtRPM := filepath.Join(pkgDir, "hello-2.12.3-1.x86_64.rpm")
	_ = os.WriteFile(builtRPM, []byte("SIMULATED_RPM_BINARY_DATA"), 0644)

	elapsed := time.Since(start).Seconds()
	return &ExecutionResult{
		ExitCode: 0,
		Artifacts: []v1.ChrootRPM{
			{Filename: "hello-2.12.3-1.x86_64.rpm", S3Key: fmt.Sprintf("artifacts/%s/hello/hello-2.12.3-1.x86_64.rpm", job.Project), SizeBytes: 1048576},
		},
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
