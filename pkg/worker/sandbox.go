package worker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/storage/s3"
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
	WorkDir     string
	StateDir    string
	DiskDir     string
	CacheDir    string
	SandboxMode string
	ObsBuildBin string
	KVMUser     string
	KVMVMArgs   string
	KVMAio      string
	KVMCache    string
	// S3Storage is the artifact store. nil disables all S3 interaction
	// (legacy local development only; hermetic jobs require it).
	S3Storage ObjectStore
	// HTTPClient fetches DoD bdeps; nil selects a client with a long timeout.
	HTTPClient *http.Client
	// AllowUnisolatedChroot permits BuildInfo jobs in plain obs-build chroot
	// mode. obs-build chroot mode has no network isolation, so the default
	// is to refuse such jobs.
	AllowUnisolatedChroot bool
	// KeepFailedJobs keeps the job directory of failed builds for debugging.
	KeepFailedJobs bool
	repoMu         sync.Mutex
}

// ObjectStore is the subset of the S3 client the sandbox uses. *s3.S3Storage
// implements it; tests use an in-memory fake.
type ObjectStore interface {
	GetObject(ctx context.Context, s3Key string) (io.ReadCloser, int64, string, error)
	UploadFile(ctx context.Context, localPath, s3Key string) error
	DownloadPrefix(ctx context.Context, s3Prefix, localDir string) (int, error)
	Bucket() string
}

var _ ObjectStore = (*s3.S3Storage)(nil)

// prepareJobInputs handles the per-contract pre-flight: BuildInfo jobs are
// checked against the isolation policy, legacy jobs download and verify their
// prerequisite RPMs as before.
func (s *ProcessSandbox) prepareJobInputs(ctx context.Context, job *v1.BuildJob, w io.Writer) error {
	if job.BuildInfo != nil {
		if err := s.checkHermeticAllowed(job); err != nil {
			fmt.Fprintf(w, "[%s] bsgo-worker: refusing job: %v\n", time.Now().Format("15:04:05"), err)
			return err
		}
		return nil
	}
	fmt.Fprintf(os.Stderr, "bsgo-worker: warning: legacy job without buildinfo: %s\n", job.JobID)
	s.downloadPrerequisiteRPMs(ctx, job.ChrootRPMs)
	if err := VerifyChrootRPMs(s.CacheDir, job.ChrootRPMs); err != nil {
		fmt.Fprintf(w, "[%s] bsgo-worker: integrity error: %v\n", time.Now().Format("15:04:05"), err)
		return err
	}
	return nil
}

func (s *ProcessSandbox) Execute(ctx context.Context, job *v1.BuildJob, logWriter io.Writer) (*ExecutionResult, error) {
	if job == nil || job.JobID == "" {
		return nil, fmt.Errorf("invalid job: nil or empty job ID")
	}
	cleanJobID := filepath.Clean(job.JobID)
	if cleanJobID == "." || cleanJobID == ".." || strings.Contains(job.JobID, "/") || strings.Contains(job.JobID, "\\") || strings.Contains(job.JobID, "..") {
		return nil, fmt.Errorf("invalid job ID (path traversal detected): %q", job.JobID)
	}

	jobsDir := filepath.Join(s.WorkDir, "jobs")
	jobDir := filepath.Join(jobsDir, cleanJobID)
	rel, err := filepath.Rel(jobsDir, jobDir)
	if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
		return nil, fmt.Errorf("invalid job directory resolution: %s", jobDir)
	}

	if err := os.MkdirAll(jobDir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create job dir: %w", err)
	}

	logFilePath := filepath.Clean(filepath.Join(jobDir, "build.log"))
	logFile, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to create log file: %w", err)
	}
	defer logFile.Close()

	multiWriter := io.MultiWriter(logWriter, logFile)
	start := time.Now()
	fmt.Fprintf(multiWriter, "[%s] bsgo-worker: starting build job %s (%s/%s, mode: %s, engine: %s, type: %s)\n",
		time.Now().Format("15:04:05"), job.JobID, job.Project, job.Package, s.SandboxMode, job.BuildEngine, job.BuildType)

	if err := s.prepareJobInputs(ctx, job, multiWriter); err != nil {
		return nil, err
	}

	switch strings.ToLower(strings.TrimSpace(job.BuildEngine)) {
	case "podman":
		return s.buildWithPodman(ctx, job, jobDir, multiWriter, start)
	case "helm":
		return s.buildWithHelm(ctx, job, jobDir, multiWriter, start)
	}

	if s.SandboxMode == "kvm" || s.SandboxMode == "obs-build" || s.SandboxMode == "nspawn" {
		return s.buildWithKVM(ctx, job, jobDir, multiWriter, start)
	}

	srcDir, specName := findPackageSources(job.Package, job.Source.RecipePath)
	specPath := filepath.Join(srcDir, specName)
	if _, err := os.Stat(specPath); err == nil {
		return s.buildSourcePackage(ctx, job, jobDir, srcDir, specName, multiWriter, start)
	}

	return s.buildGenericPackage(ctx, job, multiWriter, start)
}

func (s *ProcessSandbox) setupKVMDisks(job *v1.BuildJob) (string, string, error) {
	diskDir := s.DiskDir
	if diskDir == "" {
		diskDir = filepath.Join(s.WorkDir, "disks")
	}
	if err := os.MkdirAll(diskDir, 0750); err != nil {
		return "", "", err
	}

	vmDisk := filepath.Join(diskDir, fmt.Sprintf("%s.img", job.JobID))
	vmSwap := filepath.Join(diskDir, fmt.Sprintf("%s.swap", job.JobID))
	return vmDisk, vmSwap, nil
}

func cleanupKVMRun(jobDir, buildRoot, vmDisk, vmSwap string) {
	wipeAndRemoveFile(vmDisk)
	wipeAndRemoveFile(vmSwap)
	_ = os.Remove(vmDisk + ".size")
	_ = os.Remove(vmSwap + ".size")
	wipeAndRemoveFile(filepath.Join(jobDir, "build-root.img"))
	wipeAndRemoveFile(filepath.Join(jobDir, "build-root.swap"))
	_ = os.Remove(filepath.Join(jobDir, "build-root.img.size"))
	_ = os.Remove(filepath.Join(jobDir, "build-root.swap.size"))
	_ = os.RemoveAll(buildRoot)
}

func wipeAndRemoveFile(path string) {
	cleanPath := filepath.Clean(path)
	if !checkFileExists(cleanPath) {
		return
	}
	// Overwrite file header with zeroes to eliminate residual compilation state
	if f, err := os.OpenFile(cleanPath, os.O_WRONLY, 0600); err == nil {
		zeroBuf := make([]byte, 4096)
		_, _ = f.Write(zeroBuf)
		_ = f.Truncate(0)
		_ = f.Close()
	}
	_ = os.Remove(cleanPath)
}

// CleanupOrphans removes stale VM disk images, swap files, and orphaned chroot trees from previous runs.
func (s *ProcessSandbox) CleanupOrphans(_ context.Context) error {
	diskDir := s.DiskDir
	if diskDir == "" {
		diskDir = filepath.Join(s.WorkDir, "disks")
	}
	cleanupOrphanDisks(diskDir)
	cleanupOrphanJobs(filepath.Join(s.WorkDir, "jobs"))
	return nil
}

func cleanupOrphanDisks(diskDir string) {
	entries, err := filepath.Glob(filepath.Join(diskDir, "*"))
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasSuffix(e, ".img") || strings.HasSuffix(e, ".swap") {
			wipeAndRemoveFile(e)
		} else if strings.HasSuffix(e, ".size") {
			_ = os.Remove(e)
		}
	}
}

func cleanupOrphanJobs(jobsDir string) {
	for _, pattern := range []string{"*/build-root.img*", "*/build-root.swap*", "*/build-root"} {
		entries, err := filepath.Glob(filepath.Join(jobsDir, pattern))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasSuffix(e, ".img") || strings.HasSuffix(e, ".swap") {
				wipeAndRemoveFile(e)
			} else {
				_ = os.RemoveAll(e)
			}
		}
	}
}

func (s *ProcessSandbox) Cleanup(_ context.Context) error {
	return os.RemoveAll(s.WorkDir)
}

func checkFileExists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}

// jobTargetArch returns the job's target and architecture. Both are mandatory:
// there are no implicit defaults.
func jobTargetArch(job *v1.BuildJob) (target, arch string, err error) {
	if job == nil {
		return "", "", fmt.Errorf("job is nil")
	}
	if job.Target == "" {
		return "", "", fmt.Errorf("job %s has no target", job.JobID)
	}
	if job.Architecture == "" {
		return "", "", fmt.Errorf("job %s has no architecture", job.JobID)
	}
	return job.Target, job.Architecture, nil
}
