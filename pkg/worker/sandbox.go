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
	jobDir := filepath.Join(s.WorkDir, job.JobID)
	if err := os.MkdirAll(jobDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create job dir: %w", err)
	}

	cmd := exec.CommandContext(ctx, "echo", fmt.Sprintf("Building %s in %s", job.Package, jobDir))
	cmd.Stdout = logWriter
	cmd.Stderr = logWriter

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start).Seconds()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, err
		}
	}

	return &ExecutionResult{
		ExitCode:     exitCode,
		TotalSeconds: elapsed,
	}, nil
}

func (s *ProcessSandbox) Cleanup(_ context.Context) error {
	return os.RemoveAll(s.WorkDir)
}
