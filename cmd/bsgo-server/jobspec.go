package main

import (
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/openSUSE/bsgo/pkg/constraints"
	"github.com/openSUSE/bsgo/pkg/manifest"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/scheduler/checker"
)

// Resource defaults for packages that declare none.
const (
	defaultVCPUs    = 2
	defaultMemoryMB = 4096
	defaultDiskGB   = 20
)

// jobFromSpec maps the checker's JobSpec onto the wire job. The worker builds
// hermetically from BuildInfo and ignores the legacy fields.
func jobFromSpec(spec *checker.JobSpec, bucket string, now time.Time) (*v1.BuildJob, error) {
	res, err := resourcesOf(spec.Resources)
	if err != nil {
		return nil, fmt.Errorf("resources of %s: %w", spec.Package, err)
	}
	jc := jobConstraints(spec.Constraints)
	foldConstraints(&res, jc)
	bdeps := make([]v1.Bdep, 0, len(spec.Bdeps))
	for _, b := range spec.Bdeps {
		bd, err := bdepOf(b)
		if err != nil {
			return nil, fmt.Errorf("bdep %s of %s: %w", b.Name, spec.Package, err)
		}
		bdeps = append(bdeps, bd)
	}
	prpa := buildrepo.Prpa{Project: spec.Project, Repo: spec.Repo, Arch: spec.Arch}
	return &v1.BuildJob{
		JobID:        spec.JobID,
		Project:      spec.Project,
		Package:      spec.Package,
		Target:       spec.Repo,
		Architecture: spec.Arch,
		Priority:     spec.Priority,
		Reason:       spec.Explain,
		BuildType:    spec.BuildType,
		BuildEngine:  "build",
		Source: v1.SourceSpec{
			GitURL: spec.SourceURL, Branch: spec.Branch, CommitSHA: spec.SourceCommit, RecipePath: spec.RecipeFile,
		},
		BuildInfo: &v1.BuildInfo{
			Config: spec.Config, Bdeps: bdeps, ArchPath: spec.Archpath,
			Release: spec.Release, DistURL: spec.DistURL,
			Flavor: spec.Flavor, ObsPackage: spec.BasePackage,
			SourceCommit: spec.SourceCommit, Meta: spec.Meta, LogIdleLimit: spec.LogIdleLimit,
		},
		Resources:     res,
		Constraints:   jc,
		Destination:   v1.ArtifactDestination{S3Bucket: bucket, S3Prefix: buildrepo.ArtifactPrefix(prpa, spec.Package)},
		CreatedAtUnix: now.Unix(),
	}, nil
}

// jobConstraints maps the merged constraints onto the wire message; nil when
// no constraint is dispatch relevant.
func jobConstraints(c constraints.Constraints) *v1.JobConstraints {
	jc := &v1.JobConstraints{
		MemoryMB:   int64(math.Ceil(c.Hardware.MemoryMB)),
		DiskMB:     int64(math.Ceil(c.Hardware.DiskMB)),
		Processors: int32(min(c.Hardware.Processors, 1<<20)),
	}
	for _, s := range c.Sandbox {
		jc.Sandbox = append(jc.Sandbox, v1.SandboxConstraint{Value: s.Value, Exclude: s.Exclude})
	}
	if len(jc.Sandbox) == 0 && jc.MemoryMB == 0 && jc.DiskMB == 0 && jc.Processors == 0 {
		return nil
	}
	return jc
}

// foldConstraints raises the resource request to what the constraints demand,
// so the dispatcher matches workers by capacity and the VM is sized for it.
func foldConstraints(res *v1.ResourceRequirements, jc *v1.JobConstraints) {
	if jc == nil {
		return
	}
	res.MemoryMB = max(res.MemoryMB, jc.MemoryMB)
	res.DiskGB = max(res.DiskGB, (jc.DiskMB+1023)/1024)
	res.VCPUs = max(res.VCPUs, jc.Processors)
}

func bdepOf(b checker.Bdep) (v1.Bdep, error) {
	src := v1.BdepSource{S3Key: b.Source.S3Key, URL: b.Source.URL}
	if (src.S3Key == "") == (src.URL == "") {
		return v1.Bdep{}, fmt.Errorf("need exactly one of s3 key and url")
	}
	if src.URL != "" {
		u, err := url.Parse(src.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return v1.Bdep{}, fmt.Errorf("url %q is not an absolute http(s) url", src.URL)
		}
	}
	return v1.Bdep{
		Name: b.Name, Epoch: b.Epoch, Version: b.Version, Release: b.Release, Arch: b.Arch,
		SHA256: b.SHA256, Size: b.Size, Source: src,
		Preinstall: b.Preinstall, VMInstall: b.VMInstall, RunScripts: b.RunScripts, NotMeta: b.NotMeta,
	}, nil
}

// resourcesOf converts the manifest's resource limits ("32Gi", "100Gi", "8h").
func resourcesOf(l manifest.ResourceLimits) (v1.ResourceRequirements, error) {
	r := v1.ResourceRequirements{VCPUs: defaultVCPUs, MemoryMB: defaultMemoryMB, DiskGB: defaultDiskGB}
	if l.VCPUs > 0 {
		r.VCPUs = int32(min(l.VCPUs, 1<<20))
	}
	if l.Memory != "" {
		mb, err := quantity(l.Memory, 1)
		if err != nil {
			return r, fmt.Errorf("memory: %w", err)
		}
		r.MemoryMB = mb
	}
	if l.Disk != "" {
		gb, err := quantity(l.Disk, 1<<10)
		if err != nil {
			return r, fmt.Errorf("disk: %w", err)
		}
		r.DiskGB = gb
	}
	if l.Timeout != "" {
		secs, err := timeoutSeconds(l.Timeout)
		if err != nil {
			return r, fmt.Errorf("timeout: %w", err)
		}
		r.TimeoutSeconds = secs
	}
	return r, nil
}

var quantityUnits = []struct {
	suffix string
	mb     int64 // size of the unit in MiB
}{
	{"Ti", 1 << 20}, {"Gi", 1 << 10}, {"Mi", 1}, {"T", 1 << 20}, {"G", 1 << 10}, {"M", 1},
}

// quantity parses "<n>[Ti|Gi|Mi|T|G|M]" and returns the amount in units of
// per MiB: with per=1 the result is MiB, with per=1024 it is GiB. A plain
// number is in the result unit.
func quantity(s string, per int64) (int64, error) {
	s = strings.TrimSpace(s)
	for _, u := range quantityUnits {
		if num, ok := strings.CutSuffix(s, u.suffix); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(num), 10, 64)
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("invalid quantity %q", s)
			}
			return max(n*u.mb/per, 1), nil
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid quantity %q", s)
	}
	return n, nil
}

func timeoutSeconds(s string) (int32, error) {
	s = strings.TrimSpace(s)
	if n, err := strconv.ParseInt(s, 10, 32); err == nil {
		if n < 0 {
			return 0, fmt.Errorf("negative timeout %q", s)
		}
		return int32(n), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 || d > 1<<30*time.Second {
		return 0, fmt.Errorf("invalid timeout %q", s)
	}
	return int32(d / time.Second), nil
}
