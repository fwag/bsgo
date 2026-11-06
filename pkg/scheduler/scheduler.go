package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
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

type Scheduler struct {
	store      storage.Store
	solverPool *solv.WorkerPool
	publisher  JobPublisher
	cfg        Config
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

func NewScheduler(store storage.Store, solverPool *solv.WorkerPool, publisher JobPublisher, cfg Config) *Scheduler {
	return &Scheduler{
		store:      store,
		solverPool: solverPool,
		publisher:  publisher,
		cfg:        cfg,
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
		if enabled, exists := pkg.Build.Targets[targetName]; exists && !enabled {
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

func (s *Scheduler) processTarget(ctx context.Context, trigger *v1.BuildTrigger, targetName string, archs []string, pkg *manifest.PackageManifest) ([]*v1.BuildJob, error) {
	var jobs []*v1.BuildJob
	for _, arch := range archs {
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

	res, err := s.solverPool.Resolve(ctx, req)
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

	return &v1.BuildJob{
		JobID:        fmt.Sprintf("job-%d", time.Now().UnixNano()),
		Project:      trigger.Project,
		Package:      trigger.Package,
		Target:       targetName,
		Architecture: arch,
		Priority:     int32(50 + len(chrootRPMs)),
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

func isArchExcluded(arch string, excluded []string) bool {
	for _, ex := range excluded {
		if ex == arch {
			return true
		}
	}
	return false
}
