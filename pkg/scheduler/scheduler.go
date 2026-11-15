package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openSUSE/bsgo/pkg/dod"
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
	store       storage.Store
	solver      Solver
	publisher   JobPublisher
	cfg         Config
	dodIngestor *dod.ExternalRepoIngestor
	dodFetcher  *dod.DoDFetcher
	dodRepos    *dod.RepoCache
}

// SetRepoCache makes external repository metadata come from the ingested solv
// blobs of rc instead of re-fetching primary.xml for every resolve.
func (s *Scheduler) SetRepoCache(rc *dod.RepoCache) {
	s.dodRepos = rc
}

func (s *Scheduler) SetDoD(ingestor *dod.ExternalRepoIngestor, fetcher *dod.DoDFetcher) {
	s.dodIngestor = ingestor
	s.dodFetcher = fetcher
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

// TargetArchError reports a failure scoped to one build target and architecture
// so that callers can record the state for the exact (repo, arch).
type TargetArchError struct {
	Target string
	Arch   string
	Err    error
}

func (e *TargetArchError) Error() string { return e.Err.Error() }

func (e *TargetArchError) Unwrap() error { return e.Err }

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
		effectiveCfg := ResolveTargetConfig(ctx, s.store, prj, targetName)
		if !s.isTargetEligible(trigger, pkg, targetName, targetCfg, effectiveCfg) {
			continue
		}

		targetJobs, err := s.processTarget(ctx, trigger, targetName, targetCfg, prj, pkg)
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

func (s *Scheduler) isTargetEligible(trigger *v1.BuildTrigger, pkg *manifest.PackageManifest, targetName string, targetCfg manifest.BuildTarget, effectiveCfg *manifest.ProjectConfig) bool {
	if trigger.Target != "" && trigger.Target != targetName {
		return false
	}

	if enabled, exists := pkg.Build.Targets[targetName]; exists && !enabled {
		return false
	}

	recipeType := DetectRecipeBuildType(pkg.Source.Recipe)
	if !effectiveCfg.HasBuildType(recipeType) {
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

func (s *Scheduler) processTarget(ctx context.Context, trigger *v1.BuildTrigger, targetName string, targetCfg manifest.BuildTarget, prj *manifest.ProjectManifest, pkg *manifest.PackageManifest) ([]*v1.BuildJob, error) {
	var eligibleArchs []string
	for _, arch := range targetCfg.Architectures {
		if trigger.Architecture != "" && trigger.Architecture != arch {
			continue
		}
		if isArchExcluded(arch, pkg.Build.Architectures.Exclude) {
			continue
		}
		eligibleArchs = append(eligibleArchs, arch)
	}

	var jobs []*v1.BuildJob
	for _, arch := range eligibleArchs {
		jobID := trigger.TriggerID
		if jobID == "" {
			jobID = uuid.NewString()
		} else if len(eligibleArchs) > 1 {
			jobID = fmt.Sprintf("%s-%s", trigger.TriggerID, arch)
		}

		archEffectiveCfg := ResolveTargetConfig(ctx, s.store, prj, targetName, arch)
		job, err := s.buildJobForArch(ctx, trigger, targetName, arch, pkg, targetCfg, archEffectiveCfg, jobID)
		if err != nil {
			return nil, &TargetArchError{Target: targetName, Arch: arch, Err: err}
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

func (s *Scheduler) ingestExternalRepos(ctx context.Context, deps []manifest.DependencySource) (map[string][]byte, []string) {
	var externalURLs []string
	rpmmdRepos := make(map[string][]byte)
	if s.dodIngestor == nil && s.dodRepos == nil {
		return rpmmdRepos, externalURLs
	}
	for _, dep := range deps {
		if dep.URL == "" {
			continue
		}
		externalURLs = append(externalURLs, dep.URL)
		if s.dodRepos != nil {
			if blob, err := s.dodRepos.Get(ctx, dep.URL); err == nil {
				rpmmdRepos[dep.URL] = blob.Data
			}
			continue
		}
		if primaryData, err := s.dodIngestor.FetchPrimary(ctx, dep.URL); err == nil {
			rpmmdRepos[dep.URL] = primaryData
		}
	}
	return rpmmdRepos, externalURLs
}

func (s *Scheduler) buildChrootRPMs(ctx context.Context, project, targetName string, externalURLs []string, pkgs []solv.ResolvedPackage) []v1.ChrootRPM {
	var chrootRPMs []v1.ChrootRPM
	for _, p := range pkgs {
		digest := p.SHA256
		filename := fmt.Sprintf("%s-%s.%s.rpm", p.Name, p.EVR, p.Arch)
		s3Key := fmt.Sprintf("repos/%s/%s/%s/%s", project, targetName, p.Arch, filename)

		if s.dodFetcher != nil {
			for _, u := range externalURLs {
				if cachedKey, err := s.dodFetcher.EnsurePackageCached(ctx, u, filename, p.SHA256); err == nil {
					s3Key = cachedKey
					break
				}
			}
		}

		chrootRPMs = append(chrootRPMs, v1.ChrootRPM{
			Filename: filename,
			S3Key:    s3Key,
			SHA256:   digest,
		})
	}
	return chrootRPMs
}

func (s *Scheduler) buildJobForArch(ctx context.Context, trigger *v1.BuildTrigger, targetName, arch string, pkg *manifest.PackageManifest, targetCfg manifest.BuildTarget, effectiveCfg *manifest.ProjectConfig, jobID string) (*v1.BuildJob, error) {
	rpmmdRepos, externalURLs := s.ingestExternalRepos(ctx, targetCfg.Dependencies)

	buildReqs := []string{pkg.Package.Name}
	if len(pkg.Build.Requires) > 0 {
		buildReqs = pkg.Build.Requires
	}

	var preferList []string
	if effectiveCfg != nil && len(effectiveCfg.Prefer) > 0 {
		preferList = make([]string, len(effectiveCfg.Prefer))
		copy(preferList, effectiveCfg.Prefer)
	}

	var ignores []string
	var fileProvides map[string][]string
	if effectiveCfg != nil {
		ignores = effectiveCfg.Ignore
		fileProvides = effectiveCfg.FileProvides
	}

	req := solv.ExpansionRequest{
		BuildRequires: buildReqs,
		Prefer:        preferList,
		Ignore:        ignores,
		FileProvides:  fileProvides,
		Architecture:  arch,
		RPMMDRepos:    rpmmdRepos,
	}

	res, err := s.solver.Resolve(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("solver failed for %s (%s): %w", pkg.Package.Name, arch, err)
	}

	chrootRPMs := s.buildChrootRPMs(ctx, trigger.Project, targetName, externalURLs, res.Packages)
	prio := calculatePriority(trigger.NormalizedPriority(), len(chrootRPMs))
	repos := resolveRepositoryDependencies(trigger, targetName, arch, targetCfg)
	recipeType := DetectRecipeBuildType(pkg.Source.Recipe)
	buildEngine := effectiveCfg.EffectiveBuildEngine()
	if recipeType == "docker" && effectiveCfg.BuildEngine == "" {
		buildEngine = "podman"
	}

	job := &v1.BuildJob{
		JobID:        jobID,
		Project:      trigger.Project,
		Package:      trigger.Package,
		Target:       targetName,
		Architecture: arch,
		Priority:     prio,
		Reason:       trigger.Reason,
		BuildType:    recipeType,
		BuildEngine:  buildEngine,
		Prefer:       preferList,
		Source: v1.SourceSpec{
			GitURL:     trigger.SourceGitURL,
			Branch:     pkg.Source.Branch,
			CommitSHA:  trigger.CommitSHA,
			RecipePath: pkg.Source.Recipe,
		},
		Repositories: repos,
		ChrootRPMs:   chrootRPMs,
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
	}
	populateJobBuildConfig(job, effectiveCfg)
	return job, nil
}

func populateJobBuildConfig(job *v1.BuildJob, effectiveCfg *manifest.ProjectConfig) {
	if effectiveCfg == nil {
		return
	}
	job.BuildConfig = manifest.RenderBuildConfig(effectiveCfg)
	if len(effectiveCfg.BuildFlags) > 0 {
		job.BuildFlags = make([]string, len(effectiveCfg.BuildFlags))
		copy(job.BuildFlags, effectiveCfg.BuildFlags)
	}
	job.Macros = effectiveCfg.Macros
	job.Preinstall = effectiveCfg.Preinstall
	job.VMinstall = effectiveCfg.VMinstall
	job.Support = effectiveCfg.Support
	job.Required = effectiveCfg.Required
	job.Order = effectiveCfg.Order
}

// DetectRecipeBuildType returns the build type based on the recipe filename or extension.
func DetectRecipeBuildType(recipePath string) string {
	base := strings.ToLower(filepath.Base(recipePath))
	switch {
	case strings.HasSuffix(base, ".spec"):
		return "spec"
	case strings.HasSuffix(base, ".kiwi"):
		return "kiwi"
	case strings.HasSuffix(base, ".productcompose"):
		return "productcompose"
	case strings.HasSuffix(base, ".dsc") || base == "debian.control":
		return "dsc"
	case base == "dockerfile" || strings.HasPrefix(base, "dockerfile."):
		return "docker"
	case base == "chart.yaml":
		return "helm"
	case base == "pkgbuild":
		return "arch"
	case base == "apkbuild":
		return "apk"
	default:
		return "spec"
	}
}

// ResolveTargetConfig resolves the effective configuration for a target and architecture across the dependency DAG.
// If the target sets from_scratch: true, upstream dependencies and project defaults are omitted.
func ResolveTargetConfig(ctx context.Context, store storage.Store, prj *manifest.ProjectManifest, targetName string, arch ...string) *manifest.ProjectConfig {
	selectedArch := ""
	if len(arch) > 0 {
		selectedArch = arch[0]
	}

	tgt, ok := prj.Targets[targetName]
	if !ok {
		return prj.EffectiveTargetArchConfig(targetName, selectedArch)
	}

	if tgt.Config != nil && tgt.Config.FromScratch {
		res := tgt.Config.Clone()
		if selectedArch != "" && tgt.ArchConfig != nil && tgt.ArchConfig[selectedArch] != nil {
			res = res.Merge(tgt.ArchConfig[selectedArch])
		}
		return res
	}

	baseConfig := resolveDependencyConfigs(ctx, store, prj, tgt.Dependencies, targetName, selectedArch)

	ownConfig := prj.EffectiveTargetArchConfig(targetName, selectedArch)
	if baseConfig == nil {
		baseConfig = ownConfig.Clone()
	} else {
		baseConfig = baseConfig.Merge(ownConfig)
	}

	if baseConfig == nil {
		return &manifest.ProjectConfig{}
	}
	return baseConfig
}

func resolveDependencyConfigs(ctx context.Context, store storage.Store, prj *manifest.ProjectManifest, deps []manifest.DependencySource, targetName, arch string) *manifest.ProjectConfig {
	if store == nil {
		return nil
	}
	var baseConfig *manifest.ProjectConfig
	for _, dep := range deps {
		if dep.Project == "" || dep.Project == prj.Project.Name {
			continue
		}
		parentPrj, err := store.GetProject(ctx, dep.Project)
		if err != nil || parentPrj == nil {
			continue
		}
		depTarget := dep.Target
		if depTarget == "" {
			depTarget = targetName
		}
		parentCfg := parentPrj.EffectiveConfigForArch(depTarget, arch, prj.Project.Name)
		if baseConfig == nil {
			baseConfig = parentCfg.Clone()
		} else {
			baseConfig = baseConfig.Merge(parentCfg)
		}
	}
	return baseConfig
}

func resolveRepositoryDependencies(trigger *v1.BuildTrigger, targetName, arch string, targetCfg manifest.BuildTarget) []v1.RepositoryDependency {
	var repos []v1.RepositoryDependency
	for _, dep := range targetCfg.Dependencies {
		if dep.Project != "" {
			depTarget := dep.Target
			if depTarget == "" {
				depTarget = targetName
			}
			repos = append(repos, v1.RepositoryDependency{
				Project:  dep.Project,
				Target:   depTarget,
				Arch:     arch,
				S3Prefix: fmt.Sprintf("repos/%s/%s/%s/", dep.Project, depTarget, arch),
			})
		}
		if dep.URL != "" {
			repos = append(repos, v1.RepositoryDependency{
				URL: dep.URL,
			})
		}
	}
	repos = append(repos, v1.RepositoryDependency{
		Project:  trigger.Project,
		Target:   targetName,
		Arch:     arch,
		S3Prefix: fmt.Sprintf("repos/%s/%s/%s/", trigger.Project, targetName, arch),
	})
	return repos
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
