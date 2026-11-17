package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
		if rpm.SHA256 == "" {
			continue // legacy jobs without BuildInfo may omit the digest; hermetic jobs never do
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

const (
	bdepCacheSubdir  = "bdeps"
	defaultHTTPLimit = 30 * time.Minute
)

// bdepCacheRoot is the worker-local content-addressed bdep cache. It is keyed
// by sha256, never shared between workers, and re-verified on every use.
func (s *ProcessSandbox) bdepCacheRoot() string {
	base := s.CacheDir
	if base == "" {
		base = filepath.Join(s.WorkDir, "cache")
	}
	return filepath.Join(base, bdepCacheSubdir)
}

func (s *ProcessSandbox) bdepCachePath(sha string) string {
	sha = strings.ToLower(sha)
	return filepath.Join(s.bdepCacheRoot(), sha[:2], sha)
}

func (s *ProcessSandbox) httpClient() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	return &http.Client{Timeout: defaultHTTPLimit}
}

// validateBdep rejects bdeps the worker cannot fetch and verify safely.
func validateBdep(b v1.Bdep) error {
	fields := []struct{ name, value string }{
		{"name", b.Name}, {"version", b.Version}, {"release", b.Release}, {"arch", b.Arch},
	}
	for _, f := range fields {
		if f.value == "" {
			return fmt.Errorf("bdep %q has no %s", b.Name, f.name)
		}
		if strings.ContainsAny(f.value, "/\\") || strings.Contains(f.value, "..") {
			return fmt.Errorf("bdep %q has an unsafe %s %q", b.Name, f.name, f.value)
		}
	}
	if len(b.SHA256) != sha256.Size*2 {
		return fmt.Errorf("bdep %s has no valid sha256 (got %q)", b.Name, b.SHA256)
	}
	if _, err := hex.DecodeString(b.SHA256); err != nil {
		return fmt.Errorf("bdep %s has a non-hex sha256: %w", b.Name, err)
	}
	if (b.Source.S3Key == "") == (b.Source.URL == "") {
		return fmt.Errorf("bdep %s must have exactly one of source.s3_key or source.url", b.Name)
	}
	if b.Source.URL != "" {
		u, err := url.Parse(b.Source.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("bdep %s has an unsupported source url %q", b.Name, b.Source.URL)
		}
	}
	return nil
}

// fetchBdeps materialises every bdep in bdepsDir through the sha256 cache.
// Any failure, including a digest mismatch, fails the job.
func (s *ProcessSandbox) fetchBdeps(ctx context.Context, bdeps []v1.Bdep, bdepsDir string, w io.Writer) error {
	if err := os.MkdirAll(bdepsDir, 0o750); err != nil {
		return fmt.Errorf("creating bdeps dir: %w", err)
	}
	for _, b := range bdeps {
		if err := validateBdep(b); err != nil {
			return err
		}
		if err := s.fetchBdep(ctx, b, bdepsDir); err != nil {
			return fmt.Errorf("fetching bdep %s: %w", b.Name, err)
		}
	}
	fmt.Fprintf(w, "[%s] bsgo-worker: %d bdeps verified (sha256) in %s\n", time.Now().Format("15:04:05"), len(bdeps), bdepsDir)
	return nil
}

func (s *ProcessSandbox) fetchBdep(ctx context.Context, b v1.Bdep, bdepsDir string) error {
	cached := s.bdepCachePath(b.SHA256)
	if err := verifyFileSHA256(cached, b.SHA256); err != nil {
		// Missing or corrupt cache entry: drop it and download again.
		if rmErr := os.Remove(cached); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return fmt.Errorf("removing stale cache entry: %w", rmErr)
		}
		if err := s.downloadBdep(ctx, b, cached); err != nil {
			return err
		}
	}
	if err := placeBdep(cached, filepath.Join(bdepsDir, b.Filename()), b.SHA256); err != nil {
		return fmt.Errorf("placing bdep: %w", err)
	}
	return nil
}

// placeBdep puts the cached file at dest. It is idempotent: a dest that
// already holds the expected content is left alone, anything else (another
// build of the same file name, a read-only hard link) is replaced.
func placeBdep(cached, dest, sha256sum string) error {
	if verifyFileSHA256(dest, sha256sum) == nil {
		return nil
	}
	if err := os.Remove(dest); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Link(cached, dest); err == nil {
		return nil
	}
	return copyFileContents(cached, dest)
}

func verifyFileSHA256(path, want string) error {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
		return fmt.Errorf("sha256 mismatch for %s: expected %s, got %s", path, want, got)
	}
	return nil
}

func (s *ProcessSandbox) openBdep(ctx context.Context, b v1.Bdep) (io.ReadCloser, error) {
	if b.Source.S3Key != "" {
		if s.S3Storage == nil {
			return nil, errors.New("bdep is stored in S3 but the worker has no object store")
		}
		rc, _, _, err := s.S3Storage.GetObject(ctx, b.Source.S3Key)
		return rc, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.Source.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", b.Source.URL, resp.Status)
	}
	return resp.Body, nil
}

// downloadBdep streams the bdep into a temporary file, verifies size and
// sha256, and only then moves it into the content-addressed cache.
func (s *ProcessSandbox) downloadBdep(ctx context.Context, b v1.Bdep, cached string) error {
	rc, err := s.openBdep(ctx, b)
	if err != nil {
		return err
	}
	defer rc.Close()
	dir := filepath.Dir(cached)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(tmp, h), rc)
	if closeErr := tmp.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return fmt.Errorf("downloading: %w", copyErr)
	}
	if b.Size > 0 && n != b.Size {
		return fmt.Errorf("size mismatch: expected %d bytes, got %d", b.Size, n)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, b.SHA256) {
		return fmt.Errorf("sha256 mismatch: expected %s, got %s", b.SHA256, got)
	}
	if err := os.Chmod(tmp.Name(), 0o400); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), cached)
}
