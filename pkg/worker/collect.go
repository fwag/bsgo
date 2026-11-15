package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openSUSE/bsgo/pkg/bininfo"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

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
