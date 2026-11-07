package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/openSUSE/bsgo/pkg/manifest"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/solv"
	"github.com/openSUSE/bsgo/pkg/storage"
)

type Config struct {
	S3Bucket string
}

type Solver interface {
	Resolve(ctx context.Context, req solv.ExpansionRequest) (*solv.ExpansionResult, error)
}

type Scheduler struct {
	store     storage.Store
	solver    Solver
	publisher JobPublisher
	cfg       Config
}

type JobPublisher interface {
	Publish(ctx context.Context, subject string, data []byte) error
}

type NatsPublisher struct {
	publishFunc func(ctx context.Context, subject string, data []byte) error
}

func NewNatsPublisher(fn func(ctx context.Context, subject string, data []byte) error) *NatsPublisher {
	return &NatsPublisher{publishFunc: fn}
}

func (p *NatsPublisher) Publish(ctx context.Context, subject string, data []byte) error {
	return p.publishFunc(ctx, subject, data)
}

func NewScheduler(store storage.Store, solver Solver, publisher JobPublisher, cfg Config) *Scheduler {
	return &Scheduler{
		store:     store,
		solver:    solver,
		publisher: publisher,
		cfg:       cfg,
	}
}

func (s *Scheduler) ProcessTrigger(ctx context.Context, trigger *v1.BuildTrigger) ([]*v1.BuildJob, error) {
	prj, err := s.store.GetProject(ctx, trigger.Project)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch project %s: %w", trigger.Project, err)
	}

	pkg, err := s.store.GetPackage(ctx, trigger.Project, trigger.Package)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch package %s: %w", trigger.Package, err)
	}

	if !pkg.Build.Enabled {
		return nil, nil // Package builds explicitly disabled
	}

	var generatedJobs []*v1.BuildJob
	for targetName, targetCfg := range prj.Targets {
		if !s.isTargetEligible(trigger, pkg, targetName, targetCfg) {
			continue
		}

		targetJobs, err := s.processTarget(ctx, trigger, targetName, targetCfg.Architectures, pkg)
		if err != nil {
			return nil, err
		}
		generatedJobs = append(generatedJobs, targetJobs...)
	}

	return generatedJobs, nil
}

// ProcessCommitBatch processes multiple package triggers from a single atomic Git commit.
func (s *Scheduler) ProcessCommitBatch(ctx context.Context, triggers []*v1.BuildTrigger) ([]*v1.BuildJob, error) {
	var allJobs []*v1.BuildJob
	for _, tr := range triggers {
		jobs, err := s.ProcessTrigger(ctx, tr)
		if err != nil {
			return nil, fmt.Errorf("failed processing trigger %s/%s: %w", tr.Project, tr.Package, err)
		}
		allJobs = append(allJobs, jobs...)
	}
	return allJobs, nil
}

func (s *Scheduler) isTargetEligible(trigger *v1.BuildTrigger, pkg *manifest.PackageManifest, targetName string, targetCfg manifest.BuildTarget) bool {
	if trigger.Target != "" && trigger.Target != targetName {
		return false
	}

	if enabled, exists := pkg.Build.Targets[targetName]; exists && !enabled {
		return false
	}

	// Evaluate Rebuild policy
	rebuildMode := targetCfg.Flags.EffectiveRebuild()
	if rebuildMode == "local" && trigger.Reason == "CASCADE" && !trigger.ForceRebuild {
		return false // local rebuild mode skips downstream cascade rebuilds
	}
	if rebuildMode == "direct" && trigger.Reason == "CASCADE_INDIRECT" && !trigger.ForceRebuild {
		return false // direct rebuild mode skips indirect cascades
	}

	return true
}

func (s *Scheduler) processTarget(ctx context.Context, trigger *v1.BuildTrigger, targetName string, archs []string, pkg *manifest.PackageManifest) ([]*v1.BuildJob, error) {
	var jobs []*v1.BuildJob
	for _, arch := range archs {
		if trigger.Architecture != "" && trigger.Architecture != arch {
			continue
		}
		if isArchExcluded(arch, pkg.Build.Architectures.Exclude) {
			continue
		}

		job, err := s.buildJobForArch(ctx, trigger, targetName, arch, pkg)
		if err != nil {
			return nil, err
		}

		data, err := json.Marshal(job)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal job: %w", err)
		}

		if err := s.publisher.Publish(ctx, natsqueue.SubjectJobsPending, data); err != nil {
			return nil, fmt.Errorf("failed to publish job to nats: %w", err)
		}

		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (s *Scheduler) buildJobForArch(ctx context.Context, trigger *v1.BuildTrigger, targetName, arch string, pkg *manifest.PackageManifest) (*v1.BuildJob, error) {
	req := solv.ExpansionRequest{
		BuildRequires: []string{pkg.Package.Name},
	}

	res, err := s.solver.Resolve(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("solver failed for %s (%s): %w", pkg.Package.Name, arch, err)
	}

	var chrootRPMs []v1.ChrootRPM
	for _, p := range res.Packages {
		chrootRPMs = append(chrootRPMs, v1.ChrootRPM{
			Filename: fmt.Sprintf("%s-%s.%s.rpm", p.Name, p.EVR, p.Arch),
			S3Key:    fmt.Sprintf("repos/%s/%s/%s/%s-%s.%s.rpm", trigger.Project, targetName, p.Arch, p.Name, p.EVR, p.Arch),
			SHA256:   "mock-sha256-digest",
		})
	}

	prio := calculatePriority(trigger.NormalizedPriority(), len(chrootRPMs))

	return &v1.BuildJob{
		JobID:        fmt.Sprintf("job-%d", time.Now().UnixNano()),
		Project:      trigger.Project,
		Package:      trigger.Package,
		Target:       targetName,
		Architecture: arch,
		Priority:     prio,
		Reason:       trigger.Reason,
		Source: v1.SourceSpec{
			GitURL:     trigger.SourceGitURL,
			CommitSHA:  trigger.CommitSHA,
			RecipePath: pkg.Source.Recipe,
		},
		ChrootRPMs: chrootRPMs,
		Resources: v1.ResourceRequirements{
			MemoryMB: 4096,
			VCPUs:    2,
			DiskGB:   20,
		},
		Destination: v1.ArtifactDestination{
			S3Bucket: s.cfg.S3Bucket,
			S3Prefix: fmt.Sprintf("artifacts/%s/%s/%s/%s/", trigger.Project, targetName, arch, trigger.Package),
		},
		CreatedAtUnix: time.Now().Unix(),
	}, nil
}

func calculatePriority(priorityTier string, chrootCount int) int32 {
	var base int32
	switch priorityTier {
	case "low":
		base = 0
	case "med":
		base = 500
	default:
		base = 1000
	}

	if chrootCount < 0 {
		chrootCount = 0
	}
	if chrootCount > math.MaxInt32-1050 {
		return math.MaxInt32
	}
	// #nosec G115 -- chrootCount bounded above to prevent integer overflow
	return base + 50 + int32(chrootCount)
}

func isArchExcluded(arch string, excluded []string) bool {
	for _, ex := range excluded {
		if ex == arch {
			return true
		}
	}
	return false
}
