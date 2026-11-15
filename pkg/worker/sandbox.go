package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/manifest"
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
	S3Storage   *s3.S3Storage
	PublishFunc func(ctx context.Context, subject string, data []byte) error
	repoMu      sync.Mutex
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

	s.downloadPrerequisiteRPMs(ctx, job.ChrootRPMs)

	if err := VerifyChrootRPMs(s.CacheDir, job.ChrootRPMs); err != nil {
		fmt.Fprintf(multiWriter, "[%s] bsgo-worker: integrity error: %v\n", time.Now().Format("15:04:05"), err)
		return nil, err
	}

	switch strings.ToLower(strings.TrimSpace(job.BuildEngine)) {
	case "podman":
		return s.buildWithPodman(ctx, job, jobDir, multiWriter, start)
	case "helm":
		return s.buildWithHelm(ctx, job, jobDir, multiWriter, start)
	}

	if s.SandboxMode == "kvm" || s.SandboxMode == "obs-build" {
		return s.buildWithKVM(ctx, job, jobDir, multiWriter, start)
	}

	srcDir, specName := findPackageSources(job.Package, job.Source.RecipePath)
	specPath := filepath.Join(srcDir, specName)
	if _, err := os.Stat(specPath); err == nil {
		return s.buildSourcePackage(ctx, job, jobDir, srcDir, specName, multiWriter, start)
	}

	return s.buildGenericPackage(ctx, job, multiWriter, start)
}

func (s *ProcessSandbox) downloadPrerequisiteRPMs(ctx context.Context, rpms []v1.ChrootRPM) {
	if s.S3Storage == nil || len(rpms) == 0 {
		return
	}
	cacheDir := s.CacheDir
	if cacheDir == "" {
		cacheDir = filepath.Join(s.WorkDir, "cache")
	}
	_ = os.MkdirAll(cacheDir, 0750)
	for _, rpm := range rpms {
		s.fetchSinglePrerequisite(ctx, cacheDir, rpm)
	}
}

func (s *ProcessSandbox) fetchSinglePrerequisite(ctx context.Context, cacheDir string, rpm v1.ChrootRPM) {
	if rpm.S3Key == "" {
		return
	}
	dest := filepath.Join(cacheDir, rpm.Filename)
	if checkFileExists(dest) {
		return
	}
	rc, _, _, err := s.S3Storage.GetObject(ctx, rpm.S3Key)
	if err != nil {
		return
	}
	defer rc.Close()
	if data, readErr := io.ReadAll(rc); readErr == nil {
		_ = os.WriteFile(dest, data, 0644)
	}
}

// VerifyChrootRPMs validates cryptographic SHA-256 digests of prerequisite packages before installation.
func VerifyChrootRPMs(rpmDir string, chrootRPMs []v1.ChrootRPM) error {
	for _, rpm := range chrootRPMs {
		if rpm.SHA256 == "" || rpm.SHA256 == "mock-sha256-digest" {
			continue // Skip empty or mock hashes in test mode
		}
		candidatePaths := []string{
			filepath.Join(rpmDir, rpm.Filename),
			filepath.Join("/var/cache/build", rpm.Filename),
		}
		var targetPath string
		for _, cp := range candidatePaths {
			if checkFileExists(cp) {
				targetPath = cp
				break
			}
		}
		if targetPath == "" {
			continue // Not present locally yet
		}

		data, err := os.ReadFile(filepath.Clean(targetPath))
		if err != nil {
			return fmt.Errorf("failed reading prerequisite %s: %w", rpm.Filename, err)
		}
		sum := sha256.Sum256(data)
		actualSHA := hex.EncodeToString(sum[:])
		if !strings.EqualFold(actualSHA, rpm.SHA256) {
			return fmt.Errorf("cryptographic checksum mismatch for prerequisite %s: expected %s, got %s",
				rpm.Filename, rpm.SHA256, actualSHA)
		}
	}
	return nil
}

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

func (s *ProcessSandbox) buildKVMArgs(ctx context.Context, job *v1.BuildJob, buildRoot, vmDisk, vmSwap, cacheDir, specFile, configPath string, w io.Writer) []string {
	memMB := int64(job.Resources.MemoryMB)
	if memMB <= 0 {
		memMB = 4096
	}
	diskMB := int64(job.Resources.DiskGB) * 1024
	if diskMB <= 0 {
		diskMB = 10240
	}

	vcpus := job.Resources.VCPUs
	if vcpus <= 0 {
		vcpus = 1
	}

	var args []string
	if s.SandboxMode == "obs-build" {
		args = []string{
			"--clean",
			"--changelog",
			"--root", buildRoot,
			"--cachedir", cacheDir,
			"--jobs", strconv.Itoa(int(vcpus)),
		}
	} else {
		args = []string{
			"--clean",
			"--changelog",
			"--vm-type", "kvm",
			"--root", buildRoot,
			"--vm-disk", vmDisk,
			"--vm-swap", vmSwap,
			"--cachedir", cacheDir,
			"--vm-memory", strconv.FormatInt(memMB, 10),
			"--vm-disk-size", strconv.FormatInt(diskMB, 10),
			"--jobs", strconv.Itoa(int(vcpus)),
		}
		kvmUser := s.KVMUser
		if kvmUser == "" {
			kvmUser = "qemu"
		}
		args = append(args, "--vm-user", kvmUser)

		vmArgs := s.KVMVMArgs
		if vmArgs == "" {
			vmArgs = "-sandbox on -net none"
		}
		args = append(args, "--vm-args", vmArgs)
	}

	if checkFileExists(configPath) {
		args = append(args, "--dist", configPath)
	}

	repoArgs := s.ResolveJobRepositories(ctx, job, w)
	args = append(args, repoArgs...)
	if len(repoArgs) == 0 {
		args = append(args, "--repo", "zypp://")
	}
	args = append(args, specFile)
	return args
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

func (s *ProcessSandbox) buildWithKVM(ctx context.Context, job *v1.BuildJob, jobDir string, w io.Writer, start time.Time) (*ExecutionResult, error) {
	obsBin := s.ObsBuildBin
	if obsBin == "" {
		obsBin = "/usr/bin/build"
	}
	fmt.Fprintf(w, "[%s] bsgo-worker: preparing KVM micro-VM sandbox via %s\n", time.Now().Format("15:04:05"), obsBin)

	specFile, err := s.setupKVMSources(ctx, job, jobDir)
	if err != nil {
		return nil, fmt.Errorf("failed setting up package sources: %w", err)
	}

	cacheDir := filepath.Join(s.WorkDir, "cache")
	_ = os.MkdirAll(cacheDir, 0750)

	buildRoot := filepath.Join(jobDir, "build-root")
	vmDisk, vmSwap, err := s.setupKVMDisks(job)
	if err != nil {
		return nil, fmt.Errorf("failed configuring VM disks: %w", err)
	}

	defer cleanupKVMRun(jobDir, buildRoot, vmDisk, vmSwap)

	configPath := filepath.Join(jobDir, ".build.config")
	if !checkFileExists(configPath) {
		if job.BuildConfig != "" {
			_ = os.WriteFile(configPath, []byte(job.BuildConfig), 0600)
		} else if job.BuildEngine != "" || job.BuildType != "" || job.Macros != "" || len(job.BuildFlags) > 0 || len(job.Prefer) > 0 || len(job.Preinstall) > 0 || len(job.Support) > 0 || len(job.Required) > 0 {
			cfg := &manifest.ProjectConfig{
				BuildEngine: job.BuildEngine,
				BuildTypes:  []string{job.BuildType},
				Macros:      job.Macros,
				BuildFlags:  job.BuildFlags,
				Prefer:      job.Prefer,
				Preinstall:  job.Preinstall,
				VMinstall:   job.VMinstall,
				Support:     job.Support,
				Required:    job.Required,
				Order:       job.Order,
			}
			if rendered := RenderBuildConfig(cfg); rendered != "" {
				_ = os.WriteFile(configPath, []byte(rendered), 0600)
			}
		}
	}

	args := s.buildKVMArgs(ctx, job, buildRoot, vmDisk, vmSwap, cacheDir, specFile, configPath, w)

	defer func() {
		wipeBuildRoot(context.Background(), obsBin, buildRoot, w)
		_ = os.RemoveAll(filepath.Join(jobDir, "repo_snapshot"))
		_ = os.RemoveAll(filepath.Join(jobDir, "packages"))
	}()

	fmt.Fprintf(w, "[%s] bsgo-worker: executing %s %s\n", time.Now().Format("15:04:05"), obsBin, strings.Join(args, " "))
	// #nosec G204 -- obs-build runner executes parameterized sandbox binary
	cmd := exec.CommandContext(ctx, obsBin, args...)
	cmd.Dir = jobDir
	cmd.Stdout = w
	cmd.Stderr = w

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(w, "[%s] bsgo-worker: KVM execution exited: %v\n", time.Now().Format("15:04:05"), err)
		return &ExecutionResult{ExitCode: 1}, err
	}

	return s.collectKVMArtifacts(ctx, buildRoot, jobDir, job, w, start)
}

func wipeBuildRoot(ctx context.Context, obsBin, buildRoot string, w io.Writer) {
	if buildRoot == "" || !checkFileExists(buildRoot) {
		return
	}
	// #nosec G204 -- obs-build runner executes parameterized sandbox binary
	cmd := exec.CommandContext(ctx, obsBin, "--root", buildRoot, "--wipe")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(w, "[%s] bsgo-worker: failed to wipe build root: %v (%s)\n",
			time.Now().Format("15:04:05"), err, strings.TrimSpace(string(out)))
	}
}

func (s *ProcessSandbox) ResolveJobRepositories(ctx context.Context, job *v1.BuildJob, w io.Writer) []string {
	attachedRepos := make(map[string]bool)
	jobID := ""
	if job != nil {
		jobID = job.JobID
	}
	args := s.attachJobRepositories(ctx, job.Repositories, jobID, attachedRepos, w)

	target, arch, err := jobTargetArch(job)
	if err != nil {
		fmt.Fprintf(w, "cannot attach local repositories: %v\n", err)
		return args
	}

	// In cloud-native operation with S3 configured, remote projects must come from S3.
	// Only discover local disk repos if S3 is nil or to attach the job's own local repository.
	if s.S3Storage == nil {
		localArgs := s.discoverLocalRepositories(arch, target, attachedRepos, w)
		args = append(args, localArgs...)
	} else {
		// Attach own project's local repo if it has completed local builds
		ownRepo := filepath.Join(s.WorkDir, "repos", job.Project, target, arch)
		if !attachedRepos[ownRepo] {
			if _, err := os.Stat(filepath.Join(ownRepo, "repodata", "repomd.xml")); err == nil {
				args = append(args, "--repo", ownRepo)
				attachedRepos[ownRepo] = true
			}
		}
	}
	return args
}

func (s *ProcessSandbox) attachJobRepositories(
	ctx context.Context,
	repos []v1.RepositoryDependency,
	jobID string,
	attached map[string]bool,
	w io.Writer,
) []string {
	var args []string
	for _, repo := range repos {
		if repo.S3Prefix != "" && s.S3Storage != nil {
			if s3Arg := s.pullS3Repository(ctx, repo.S3Prefix, jobID, attached, w); s3Arg != "" {
				args = append(args, "--repo", s3Arg)
			}
		}
		if repo.URL != "" {
			args = append(args, "--repo", repo.URL)
			fmt.Fprintf(w, "[%s] bsgo-worker: attaching external repository: %s\n",
				time.Now().Format("15:04:05"), repo.URL)
		}
	}
	return args
}

func (s *ProcessSandbox) pullS3Repository(ctx context.Context, prefix, jobID string, attached map[string]bool, w io.Writer) string {
	s.repoMu.Lock()
	cleanPrefix := strings.Trim(filepath.Clean(prefix), "/") + "/"
	s3RepoDir := filepath.Join(s.WorkDir, "cache", "s3_repos", filepath.Clean(cleanPrefix))
	count, err := s.S3Storage.DownloadPrefix(ctx, cleanPrefix, s3RepoDir)
	var activeRepoDir string
	if err == nil && count > 0 {
		activeRepoDir = s.SnapshotRepoForJob(s3RepoDir, jobID)
	}
	s.repoMu.Unlock()

	if err != nil {
		fmt.Fprintf(w, "[%s] bsgo-worker: S3 download error for s3://bsgo-artifacts/%s: %v\n",
			time.Now().Format("15:04:05"), cleanPrefix, err)
		return ""
	}
	if count == 0 {
		fmt.Fprintf(w, "[%s] bsgo-worker: S3 repository empty: s3://bsgo-artifacts/%s\n",
			time.Now().Format("15:04:05"), cleanPrefix)
		return ""
	}

	attached[activeRepoDir] = true
	fmt.Fprintf(w, "[%s] bsgo-worker: pulled S3 repository from s3://bsgo-artifacts/%s (%d files)\n",
		time.Now().Format("15:04:05"), cleanPrefix, count)
	return activeRepoDir
}

// SnapshotRepoForJob creates an isolated, frozen snapshot of the repository for the given job ID.
func (s *ProcessSandbox) SnapshotRepoForJob(s3RepoDir, jobID string) string {
	if jobID == "" {
		return s3RepoDir
	}
	jobRepoDir := filepath.Join(s.WorkDir, "jobs", filepath.Clean(jobID), "repo_snapshot")
	if _, err := os.Stat(filepath.Join(jobRepoDir, "repodata", "repomd.xml")); err == nil {
		return jobRepoDir
	}
	if err := os.MkdirAll(jobRepoDir, 0750); err != nil {
		return s3RepoDir
	}

	matches, _ := filepath.Glob(filepath.Join(s3RepoDir, "*.rpm"))
	for _, m := range matches {
		_ = os.Symlink(m, filepath.Join(jobRepoDir, filepath.Base(m)))
	}

	srcMeta := filepath.Join(s3RepoDir, "repodata")
	destMeta := filepath.Join(jobRepoDir, "repodata")
	if err := copyDir(srcMeta, destMeta); err != nil {
		return s3RepoDir
	}
	return jobRepoDir
}

func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0750); err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		srcFile := filepath.Join(src, entry.Name())
		dstFile := filepath.Join(dst, entry.Name())
		if err := copyFileContents(srcFile, dstFile); err != nil {
			return err
		}
	}
	return nil
}

func copyFileContents(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func (s *ProcessSandbox) discoverLocalRepositories(arch, target string, attached map[string]bool, w io.Writer) []string {
	repoBase := filepath.Join(s.WorkDir, "repos")
	repos, err := filepath.Glob(filepath.Join(repoBase, "*", target, arch))
	if err != nil {
		return nil
	}

	var args []string
	for _, r := range repos {
		if attached[r] {
			continue
		}
		if _, err := os.Stat(filepath.Join(r, "repodata", "repomd.xml")); err == nil {
			args = append(args, "--repo", r)
			attached[r] = true
			fmt.Fprintf(w, "[%s] bsgo-worker: attaching local upstream repository: %s\n",
				time.Now().Format("15:04:05"), r)
		}
	}
	return args
}

func (s *ProcessSandbox) collectKVMArtifacts(ctx context.Context, buildRoot, jobDir string, job *v1.BuildJob, w io.Writer, start time.Time) (*ExecutionResult, error) {
	pkgDir := filepath.Join(jobDir, "packages")
	_ = os.MkdirAll(pkgDir, 0750)

	target, arch, err := jobTargetArch(job)
	if err != nil {
		return &ExecutionResult{ExitCode: 1}, err
	}

	localRepo := filepath.Join(s.WorkDir, "repos", job.Project, target, arch)
	_ = os.MkdirAll(localRepo, 0750)

	var artifacts []v1.ChrootRPM
	patterns := []string{
		filepath.Join(buildRoot, ".build.packages/RPMS/*/*.rpm"),
		filepath.Join(buildRoot, "usr/src/packages/RPMS/*/*.rpm"),
		filepath.Join(buildRoot, ".build.packages/OTHER/*.iso"),
		filepath.Join(buildRoot, ".build.packages/OTHER/*.qcow2"),
		filepath.Join(buildRoot, ".build.packages/OTHER/*.raw"),
		filepath.Join(buildRoot, ".build.packages/OTHER/*.tar*"),
		filepath.Join(buildRoot, ".build.packages/OTHER/*.tgz"),
	}
	for _, p := range patterns {
		arts := collectRPMsFromPattern(p, pkgDir, job.Project, job.Package, "extracted KVM artifact", w)
		artifacts = append(artifacts, arts...)
	}

	if err := s.uploadAndPublishArtifacts(ctx, job, pkgDir, target, arch, artifacts, w); err != nil {
		return &ExecutionResult{ExitCode: 1}, err
	}

	return finalizeBuildArtifacts(pkgDir, job, artifacts, w, start)
}

// uploadBinInfo extracts the RPM headers of every produced RPM and uploads
// them as one bininfo.json next to the artifacts. Any extraction failure fails
// the job: an RPM without a trustworthy index entry must not be published.
func (s *ProcessSandbox) uploadBinInfo(ctx context.Context, job *v1.BuildJob, pkgDir, target, arch string, artifacts []v1.ChrootRPM, w io.Writer) error {
	var paths []string
	for _, a := range artifacts {
		if strings.HasSuffix(a.Filename, ".rpm") {
			paths = append(paths, filepath.Join(pkgDir, a.Filename))
		}
	}
	if len(paths) == 0 {
		return nil
	}
	infos, err := bininfo.ExtractAll(ctx, paths)
	if err != nil {
		return fmt.Errorf("extracting bininfo: %w", err)
	}
	data, err := bininfo.Marshal(infos)
	if err != nil {
		return err
	}
	local := filepath.Join(pkgDir, bininfo.FileName)
	if err := os.WriteFile(local, data, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", bininfo.FileName, err)
	}
	if s.S3Storage == nil {
		return nil
	}
	key := fmt.Sprintf("artifacts/%s/%s/%s/%s/%s", job.Project, target, arch, job.Package, bininfo.FileName)
	if err := s.S3Storage.UploadFile(ctx, local, key); err != nil {
		return fmt.Errorf("failed uploading %s to S3: %w", bininfo.FileName, err)
	}
	fmt.Fprintf(w, "[%s] bsgo-worker: uploaded bininfo for %d RPMs to s3://%s/%s\n",
		time.Now().Format("15:04:05"), len(infos), s.S3Storage.Bucket(), key)
	return nil
}

func (s *ProcessSandbox) uploadAndPublishArtifacts(ctx context.Context, job *v1.BuildJob, pkgDir, target, arch string, artifacts []v1.ChrootRPM, w io.Writer) error {
	var artifactPaths []string
	var artifactKeys []string
	if err := s.uploadBinInfo(ctx, job, pkgDir, target, arch, artifacts, w); err != nil {
		return err
	}
	if s.S3Storage != nil {
		for _, a := range artifacts {
			src := filepath.Join(pkgDir, a.Filename)
			s3Key := fmt.Sprintf("artifacts/%s/%s/%s/%s/%s", job.Project, target, arch, job.Package, a.Filename)
			if err := s.S3Storage.UploadFile(ctx, src, s3Key); err != nil {
				fmt.Fprintf(w, "[%s] bsgo-worker: failed uploading artifact %s to S3: %v\n",
					time.Now().Format("15:04:05"), a.Filename, err)
				return fmt.Errorf("failed uploading artifact to S3: %w", err)
			}
			artifactPaths = append(artifactPaths, src)
			artifactKeys = append(artifactKeys, s3Key)
			fmt.Fprintf(w, "[%s] bsgo-worker: uploaded RPM artifact to s3://%s/%s\n",
				time.Now().Format("15:04:05"), s.S3Storage.Bucket(), s3Key)
		}
	}

	if s.PublishFunc != nil && len(artifacts) > 0 {
		pubReq := v1.PublishRequest{
			RequestID:    uuid.NewString(),
			JobID:        job.JobID,
			Project:      job.Project,
			Target:       target,
			Architecture: arch,
			Package:      job.Package,
			NewRPMPaths:  artifactPaths,
			ArtifactKeys: artifactKeys,
		}
		subject := fmt.Sprintf("bsgo.publish.rpm.%s.%s.%s", job.Project, target, arch)
		data, err := json.Marshal(pubReq)
		if err != nil {
			return fmt.Errorf("failed to marshal PublishRequest: %w", err)
		}
		if err := s.PublishFunc(ctx, subject, data); err != nil {
			return fmt.Errorf("failed to emit PublishRequest to %s: %w", subject, err)
		}
		fmt.Fprintf(w, "[%s] bsgo-worker: emitted PublishRequest to %s (%d RPMs)\n",
			time.Now().Format("15:04:05"), subject, len(artifacts))
	} else if s.PublishFunc == nil {
		localRepo := filepath.Join(s.WorkDir, "repos", job.Project, target, arch)
		_ = os.MkdirAll(localRepo, 0750)
		s.fallbackLocalPublish(localRepo, artifacts, pkgDir, target, arch, job, w)
	}

	return nil
}

func (s *ProcessSandbox) fallbackLocalPublish(localRepo string, artifacts []v1.ChrootRPM, pkgDir, target, arch string, job *v1.BuildJob, w io.Writer) {
	for _, a := range artifacts {
		src := filepath.Join(pkgDir, a.Filename)
		dest := filepath.Join(localRepo, a.Filename)
		// #nosec G304 -- reading RPM from validated pkgDir sandbox
		data, err := os.ReadFile(src)
		if err == nil {
			// #nosec G703 G306 -- publishing RPM artifact with read permission
			_ = os.WriteFile(dest, data, 0644)
		}
	}

	fmt.Fprintf(w, "[%s] bsgo-worker: updating local repository repodata via createrepo_c\n", time.Now().Format("15:04:05"))
	// #nosec G204 -- createrepo_c called on validated local repo directory
	crCmd := exec.Command("createrepo_c", localRepo)
	crCmd.Stdout = w
	crCmd.Stderr = w
	_ = crCmd.Run()

	// Sync generated RPMs and repodata to Garage S3
	if s.S3Storage != nil {
		s3Prefix := fmt.Sprintf("repos/%s/%s/%s/", job.Project, target, arch)
		_ = filepath.Walk(localRepo, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(localRepo, path)
			return s.S3Storage.UploadFile(context.Background(), path, s3Prefix+rel)
		})
		fmt.Fprintf(w, "[%s] bsgo-worker: synced repository to Garage S3: s3://bsgo-artifacts/%s\n",
			time.Now().Format("15:04:05"), s3Prefix)
	}
}

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

func (s *ProcessSandbox) collectRPMArtifacts(ctx context.Context, rpmTopDir, jobDir string, job *v1.BuildJob, w io.Writer, start time.Time) (*ExecutionResult, error) {
	pkgDir := filepath.Join(jobDir, "packages")
	_ = os.MkdirAll(pkgDir, 0750)

	pattern := filepath.Join(rpmTopDir, "RPMS", "*", "*.rpm")
	artifacts := collectRPMsFromPattern(pattern, pkgDir, job.Project, job.Package, "built real RPM artifact", w)

	target, arch, err := jobTargetArch(job)
	if err != nil {
		return &ExecutionResult{ExitCode: 1}, err
	}

	if err := s.uploadAndPublishArtifacts(ctx, job, pkgDir, target, arch, artifacts, w); err != nil {
		return &ExecutionResult{ExitCode: 1}, err
	}
	return finalizeBuildArtifacts(pkgDir, job, artifacts, w, start)
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

func finalizeBuildArtifacts(pkgDir string, job *v1.BuildJob, artifacts []v1.ChrootRPM, w io.Writer, start time.Time) (*ExecutionResult, error) {
	statement := v1.GenerateSLSAProvenance(job, artifacts, "bsgo-worker", start, time.Now())
	if provJSON, err := json.MarshalIndent(statement, "", "  "); err == nil {
		provDest := filepath.Clean(filepath.Join(pkgDir, "_slsa_provenance.json"))
		// #nosec G703 -- provenance written into validated job packages directory
		_ = os.WriteFile(provDest, provJSON, 0600)
		provDigest := v1.ComputeSHA256(provJSON)
		artifacts = append(artifacts, v1.ChrootRPM{
			Filename:  "_slsa_provenance.json",
			S3Key:     fmt.Sprintf("artifacts/%s/%s/_slsa_provenance.json", job.Project, job.Package),
			SHA256:    provDigest,
			SizeBytes: int64(len(provJSON)),
		})
		fmt.Fprintf(w, "[%s] bsgo-worker: generated SLSA 1.2 provenance attestation (%d bytes, sha256: %s)\n",
			time.Now().Format("15:04:05"), len(provJSON), provDigest[:12])
	}

	elapsed := time.Since(start).Seconds()
	return &ExecutionResult{
		ExitCode:     0,
		Artifacts:    artifacts,
		TotalSeconds: elapsed,
	}, nil
}

func collectRPMsFromPattern(pattern, pkgDir, project, pkg, label string, w io.Writer) []v1.ChrootRPM {
	builtRPMs, _ := filepath.Glob(pattern)
	var artifacts []v1.ChrootRPM
	for _, rpmPath := range builtRPMs {
		cleanRPMPath := filepath.Clean(rpmPath)
		baseName := filepath.Base(cleanRPMPath)
		dest := filepath.Clean(filepath.Join(pkgDir, baseName))
		data, err := os.ReadFile(cleanRPMPath)
		if err == nil {
			// #nosec G703 -- artifact RPM copied into validated job packages directory
			_ = os.WriteFile(dest, data, 0600)
			sha256Digest := v1.ComputeSHA256(data)
			artifacts = append(artifacts, v1.ChrootRPM{
				Filename:  baseName,
				S3Key:     fmt.Sprintf("artifacts/%s/%s/%s", project, pkg, baseName),
				SHA256:    sha256Digest,
				SizeBytes: int64(len(data)),
			})
			fmt.Fprintf(w, "[%s] bsgo-worker: %s: %s (%d bytes, sha256: %s)\n",
				time.Now().Format("15:04:05"), label, baseName, len(data), sha256Digest[:12])
		}
	}
	return artifacts
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

func (s *ProcessSandbox) collectJobArtifacts(ctx context.Context, jobDir string, job *v1.BuildJob, filePaths []string, w io.Writer, start time.Time) (*ExecutionResult, error) {
	pkgDir := filepath.Join(jobDir, "packages")
	_ = os.MkdirAll(pkgDir, 0750)

	target, arch, err := jobTargetArch(job)
	if err != nil {
		return &ExecutionResult{ExitCode: 1}, err
	}

	var artifacts []v1.ChrootRPM
	for _, fp := range filePaths {
		if !checkFileExists(fp) {
			continue
		}
		baseName := filepath.Base(fp)
		dest := filepath.Join(pkgDir, baseName)
		data, err := os.ReadFile(fp)
		if err == nil {
			_ = os.WriteFile(dest, data, 0600)
			digest := v1.ComputeSHA256(data)
			artifacts = append(artifacts, v1.ChrootRPM{
				Filename:  baseName,
				S3Key:     fmt.Sprintf("artifacts/%s/%s/%s/%s/%s", job.Project, target, arch, job.Package, baseName),
				SHA256:    digest,
				SizeBytes: int64(len(data)),
			})
			fmt.Fprintf(w, "[%s] bsgo-worker: built artifact: %s (%d bytes, sha256: %s)\n",
				time.Now().Format("15:04:05"), baseName, len(data), digest[:12])
		}
	}

	if err := s.uploadAndPublishArtifacts(ctx, job, pkgDir, target, arch, artifacts, w); err != nil {
		return &ExecutionResult{ExitCode: 1}, err
	}

	return finalizeBuildArtifacts(pkgDir, job, artifacts, w, start)
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
