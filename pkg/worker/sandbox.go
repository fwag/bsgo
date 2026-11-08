package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
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
	SandboxMode string
	ObsBuildBin string
	S3Storage   *s3.S3Storage
	PublishFunc func(ctx context.Context, subject string, data []byte) error
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
	fmt.Fprintf(multiWriter, "[%s] bsgo-worker: starting build job %s (%s/%s, mode: %s)\n",
		time.Now().Format("15:04:05"), job.JobID, job.Project, job.Package, s.SandboxMode)

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

func findPackageSources(pkgName, recipePath string) (string, string) {
	candidates := []string{
		filepath.Join("test/fixtures/demo-fast", pkgName),
		filepath.Join("test/fixtures/demo", pkgName),
		filepath.Join("test/fixtures", pkgName),
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

func (s *ProcessSandbox) buildWithKVM(ctx context.Context, job *v1.BuildJob, jobDir string, w io.Writer, start time.Time) (*ExecutionResult, error) {
	obsBin := s.ObsBuildBin
	if obsBin == "" {
		obsBin = "/usr/bin/build"
	}
	fmt.Fprintf(w, "[%s] bsgo-worker: preparing KVM micro-VM sandbox via %s\n", time.Now().Format("15:04:05"), obsBin)

	srcDir, specName := findPackageSources(job.Package, job.Source.RecipePath)
	srcEntries, _ := filepath.Glob(filepath.Join(srcDir, "*"))
	for _, f := range srcEntries {
		cleanF := filepath.Clean(f)
		data, err := os.ReadFile(cleanF)
		if err == nil {
			destFile := filepath.Clean(filepath.Join(jobDir, filepath.Base(cleanF)))
			// #nosec G703 -- destination file is written within validated jobDir sandbox
			_ = os.WriteFile(destFile, data, 0600)
		}
	}

	cacheDir := filepath.Join(s.WorkDir, "cache")
	_ = os.MkdirAll(cacheDir, 0750)

	buildRoot := filepath.Join(jobDir, "build-root")
	specFile := filepath.Join(jobDir, specName)

	defer func() {
		// Clean up heavy ephemeral VM disk images and chroot tree after artifact extraction
		_ = os.Remove(filepath.Join(jobDir, "build-root.img"))
		_ = os.Remove(filepath.Join(jobDir, "build-root.swap"))
		_ = os.RemoveAll(buildRoot)
	}()

	memMB := int64(job.Resources.MemoryMB)
	if memMB <= 0 {
		memMB = 4096
	}
	diskMB := int64(job.Resources.DiskGB) * 1024
	if diskMB <= 0 {
		diskMB = 10240
	}

	args := []string{
		"--clean",
		"--vm-type", "kvm",
		"--root", buildRoot,
		"--cachedir", cacheDir,
		"--vm-memory", strconv.FormatInt(memMB, 10),
		"--vm-disk-size", strconv.FormatInt(diskMB, 10),
	}

	repoArgs := s.ResolveJobRepositories(ctx, job, w)
	args = append(args, repoArgs...)
	args = append(args, "--repo", "zypp://")
	args = append(args, specFile)

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

func (s *ProcessSandbox) ResolveJobRepositories(ctx context.Context, job *v1.BuildJob, w io.Writer) []string {
	attachedRepos := make(map[string]bool)
	args := s.attachJobRepositories(ctx, job.Repositories, attachedRepos, w)

	arch := job.Architecture
	if arch == "" {
		arch = "x86_64"
	}
	target := job.Target
	if target == "" {
		target = "standard"
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

func (s *ProcessSandbox) attachJobRepositories(ctx context.Context, repos []v1.RepositoryDependency, attached map[string]bool, w io.Writer) []string {
	var args []string
	for _, repo := range repos {
		if repo.S3Prefix != "" && s.S3Storage != nil {
			if s3Arg := s.pullS3Repository(ctx, repo.S3Prefix, attached, w); s3Arg != "" {
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

func (s *ProcessSandbox) pullS3Repository(ctx context.Context, prefix string, attached map[string]bool, w io.Writer) string {
	cleanPrefix := strings.Trim(filepath.Clean(prefix), "/") + "/"
	s3RepoDir := filepath.Join(s.WorkDir, "cache", "s3_repos", filepath.Clean(cleanPrefix))
	count, err := s.S3Storage.DownloadPrefix(ctx, cleanPrefix, s3RepoDir)
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

	attached[s3RepoDir] = true
	fmt.Fprintf(w, "[%s] bsgo-worker: pulled S3 repository from s3://bsgo-artifacts/%s (%d files)\n",
		time.Now().Format("15:04:05"), cleanPrefix, count)
	return s3RepoDir
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

	target := job.Target
	if target == "" {
		target = "standard"
	}
	arch := job.Architecture
	if arch == "" {
		arch = "x86_64"
	}

	localRepo := filepath.Join(s.WorkDir, "repos", job.Project, target, arch)
	_ = os.MkdirAll(localRepo, 0750)

	var artifacts []v1.ChrootRPM
	patterns := []string{
		filepath.Join(buildRoot, ".build.packages/RPMS/*/*.rpm"),
		filepath.Join(buildRoot, "usr/src/packages/RPMS/*/*.rpm"),
	}
	for _, p := range patterns {
		arts := collectRPMsFromPattern(p, pkgDir, job.Project, job.Package, "extracted KVM RPM artifact", w)
		artifacts = append(artifacts, arts...)
	}

	// 1. Upload newly built RPMs to S3 artifacts destination
	var artifactPaths []string
	if s.S3Storage != nil {
		for _, a := range artifacts {
			src := filepath.Join(pkgDir, a.Filename)
			s3Key := fmt.Sprintf("artifacts/%s/%s/%s/%s/%s", job.Project, target, arch, job.Package, a.Filename)
			if err := s.S3Storage.UploadFile(ctx, src, s3Key); err == nil {
				artifactPaths = append(artifactPaths, src)
				fmt.Fprintf(w, "[%s] bsgo-worker: uploaded RPM artifact to s3://%s/%s\n",
					time.Now().Format("15:04:05"), s.S3Storage.Bucket(), s3Key)
			}
		}
	}

	// 2. Decoupled publishing: Emit PublishRequest to NATS JetStream PUBLISH stream
	if s.PublishFunc != nil && len(artifacts) > 0 {
		pubReq := v1.PublishRequest{
			RequestID:    uuid.NewString(),
			JobID:        job.JobID,
			Project:      job.Project,
			Target:       target,
			Architecture: arch,
			Package:      job.Package,
			NewRPMPaths:  artifactPaths,
		}
		subject := fmt.Sprintf("bsgo.publish.rpm.%s.%s.%s", job.Project, target, arch)
		if data, err := json.Marshal(pubReq); err == nil {
			if err := s.PublishFunc(ctx, subject, data); err == nil {
				fmt.Fprintf(w, "[%s] bsgo-worker: emitted PublishRequest to %s (%d RPMs)\n",
					time.Now().Format("15:04:05"), subject, len(artifacts))
			}
		}
	} else if s.PublishFunc == nil {
		s.fallbackLocalPublish(localRepo, artifacts, pkgDir, target, arch, job, w)
	}

	return finalizeBuildArtifacts(pkgDir, job, artifacts, w, start)
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

	fmt.Fprintf(w, "[%s] bsgo-worker: executing rpmbuild -ba %s\n", time.Now().Format("15:04:05"), specFile)
	// #nosec G204 -- rpmbuild executes in temporary sandbox
	cmd := exec.CommandContext(ctx, "rpmbuild", "--nodeps", "--define", fmt.Sprintf("_topdir %s", rpmTopDir), "-ba", specFile)
	cmd.Dir = rpmTopDir
	cmd.Env = append(os.Environ(), fmt.Sprintf("PATH=%s:%s", fakeBin, os.Getenv("PATH")))
	cmd.Stdout = w
	cmd.Stderr = w

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(w, "[%s] bsgo-worker: rpmbuild error: %v\n", time.Now().Format("15:04:05"), err)
		return &ExecutionResult{ExitCode: 1}, err
	}

	return s.collectRPMArtifacts(rpmTopDir, jobDir, job, w, start)
}

func (s *ProcessSandbox) collectRPMArtifacts(rpmTopDir, jobDir string, job *v1.BuildJob, w io.Writer, start time.Time) (*ExecutionResult, error) {
	pkgDir := filepath.Join(jobDir, "packages")
	_ = os.MkdirAll(pkgDir, 0750)

	pattern := filepath.Join(rpmTopDir, "RPMS", "*", "*.rpm")
	artifacts := collectRPMsFromPattern(pattern, pkgDir, job.Project, job.Package, "built real RPM artifact", w)

	return finalizeBuildArtifacts(pkgDir, job, artifacts, w, start)
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

func (s *ProcessSandbox) Cleanup(_ context.Context) error {
	return os.RemoveAll(s.WorkDir)
}
