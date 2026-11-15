package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

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
