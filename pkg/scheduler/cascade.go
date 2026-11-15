package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/openSUSE/bsgo/pkg/manifest"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/solv"
	"github.com/openSUSE/bsgo/pkg/storage"
)

type DependencyGatekeeper interface {
	IsBuilding(project, pkg string) bool
	SetBuilding(project, pkg string, building bool)
	IsUnresolvable(project, pkg string) bool
	SetUnresolvable(project, pkg string, unresolvable bool)
	GetUnresolvablePackages(project string) []string
}

type MemoryGatekeeper struct {
	mu           sync.RWMutex
	activeBuilds map[string]bool
	unresolvable map[string]bool
}

func NewMemoryGatekeeper() *MemoryGatekeeper {
	return &MemoryGatekeeper{
		activeBuilds: make(map[string]bool),
		unresolvable: make(map[string]bool),
	}
}

func (g *MemoryGatekeeper) IsBuilding(project, pkg string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.activeBuilds[fmt.Sprintf("%s:%s", project, pkg)]
}

func (g *MemoryGatekeeper) SetBuilding(project, pkg string, building bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := fmt.Sprintf("%s:%s", project, pkg)
	if building {
		g.activeBuilds[key] = true
	} else {
		delete(g.activeBuilds, key)
	}
}

func (g *MemoryGatekeeper) IsUnresolvable(project, pkg string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.unresolvable[fmt.Sprintf("%s:%s", project, pkg)]
}

func (g *MemoryGatekeeper) SetUnresolvable(project, pkg string, unresolvable bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := fmt.Sprintf("%s:%s", project, pkg)
	if unresolvable {
		g.unresolvable[key] = true
	} else {
		delete(g.unresolvable, key)
	}
}

func (g *MemoryGatekeeper) GetUnresolvablePackages(project string) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	prefix := project + ":"
	var pkgs []string
	for k := range g.unresolvable {
		if strings.HasPrefix(k, prefix) {
			pkgs = append(pkgs, strings.TrimPrefix(k, prefix))
		}
	}
	return pkgs
}

type CascadeEngine struct {
	store      storage.Store
	solver     Solver
	publisher  JobPublisher
	gatekeeper DependencyGatekeeper
}

func NewCascadeEngine(store storage.Store, solver Solver, pub JobPublisher, gk DependencyGatekeeper) *CascadeEngine {
	if gk == nil {
		gk = NewMemoryGatekeeper()
	}
	return &CascadeEngine{
		store:      store,
		solver:     solver,
		publisher:  pub,
		gatekeeper: gk,
	}
}

func (c *CascadeEngine) Gatekeeper() DependencyGatekeeper {
	return c.gatekeeper
}

// ProcessPublicationEvent resolves reverse dependencies for a published package and emits cascade triggers to bsgo.triggers.med.
func (c *CascadeEngine) ProcessPublicationEvent(ctx context.Context, event *v1.PublicationEvent) ([]*v1.BuildTrigger, error) {
	projects, err := c.store.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed listing projects: %w", err)
	}

	var emittedTriggers []*v1.BuildTrigger
	for _, prj := range projects {
		triggers, err := c.evaluateProjectForPublication(ctx, prj, event)
		if err != nil {
			return nil, err
		}
		emittedTriggers = append(emittedTriggers, triggers...)
	}

	return emittedTriggers, nil
}

func (c *CascadeEngine) evaluateProjectForPublication(ctx context.Context, prj *manifest.ProjectManifest, event *v1.PublicationEvent) ([]*v1.BuildTrigger, error) {
	var triggers []*v1.BuildTrigger
	for targetName, targetCfg := range prj.Targets {
		if !targetDependsOnPublication(prj.Project.Name, targetName, targetCfg, event) {
			continue
		}

		rebuildMode := targetCfg.Flags.EffectiveRebuild()
		if rebuildMode == "local" && prj.Project.Name != event.Project {
			continue // local rebuild policy ignores changes from external/parent repositories
		}

		targetTriggers, err := c.evaluateTargetPackages(ctx, prj.Project.Name, targetName, targetCfg, event)
		if err != nil {
			return nil, err
		}
		triggers = append(triggers, targetTriggers...)

		unresTriggers := c.retriggerUnresolvablePackages(ctx, prj.Project.Name, targetName, event)
		triggers = append(triggers, unresTriggers...)
	}
	return triggers, nil
}

func (c *CascadeEngine) retriggerUnresolvablePackages(ctx context.Context, prjName, targetName string, event *v1.PublicationEvent) []*v1.BuildTrigger {
	unresPkgs := c.gatekeeper.GetUnresolvablePackages(prjName)
	if len(unresPkgs) == 0 {
		return nil
	}

	var triggers []*v1.BuildTrigger
	for _, unresPkg := range unresPkgs {
		if unresPkg == event.Package || c.gatekeeper.IsBuilding(prjName, unresPkg) {
			continue
		}

		pkgManifest, err := c.store.GetPackage(ctx, prjName, unresPkg)
		if err != nil || pkgManifest == nil || !pkgManifest.Build.Enabled {
			continue
		}

		commit := pkgManifest.Source.Commit
		if commit == "" {
			commit = "HEAD"
		}

		trig := v1.NewTrigger(prjName, unresPkg, pkgManifest.Source.Git, commit, pkgManifest.Source.Branch, "cascade-engine")
		trig.Priority = natsqueue.PriorityMed
		trig.Reason = "CASCADE"
		trig.Target = targetName
		trig.Architecture = event.Architecture

		if c.publisher != nil {
			subject := natsqueue.TriggerSubject(natsqueue.PriorityMed, prjName, unresPkg)
			data, _ := json.Marshal(trig)
			_ = c.publisher.Publish(ctx, subject, data)
		}
		triggers = append(triggers, trig)
	}
	return triggers
}

func (c *CascadeEngine) evaluateTargetPackages(ctx context.Context, prjName, targetName string, targetCfg manifest.BuildTarget, event *v1.PublicationEvent) ([]*v1.BuildTrigger, error) {
	pkgs, err := c.store.ListPackages(ctx, prjName)
	if err != nil {
		return nil, fmt.Errorf("failed listing packages for project %s: %w", prjName, err)
	}

	var triggers []*v1.BuildTrigger
	for _, pkg := range pkgs {
		if !pkg.Build.Enabled || (prjName == event.Project && pkg.Package.Name == event.Package) {
			continue
		}
		if c.gatekeeper.IsBuilding(prjName, pkg.Package.Name) {
			continue
		}

		trig, err := c.checkPackageCascade(ctx, prjName, targetName, targetCfg, pkg, event)
		if err != nil {
			return nil, err
		}
		if trig != nil {
			triggers = append(triggers, trig)
		}
	}
	return triggers, nil
}

func (c *CascadeEngine) resolveFromTargetSolv(ctx context.Context, event *v1.PublicationEvent, req solv.ExpansionRequest) *solv.ExpansionResult {
	if c.store == nil {
		return nil
	}
	solvRec, err := c.store.GetTargetSolv(ctx, event.Project, event.Target, event.Architecture)
	if err != nil || solvRec == nil || len(solvRec.SolvBlob) == 0 {
		return nil
	}

	pool, err := solv.NewPool(event.Architecture)
	if err != nil {
		return nil
	}
	defer pool.Close()

	repoName := fmt.Sprintf("%s:%s", event.Project, event.Target)
	if _, err := pool.LoadRepoFromMemory(repoName, solvRec.SolvBlob); err != nil {
		return nil
	}
	res, _ := pool.Resolve(req)
	return res
}

func (c *CascadeEngine) checkPackageCascade(ctx context.Context, prjName, targetName string, targetCfg manifest.BuildTarget, pkg *manifest.PackageManifest, event *v1.PublicationEvent) (*v1.BuildTrigger, error) {
	reqs := pkg.Build.Requires
	if len(reqs) == 0 {
		reqs = []string{pkg.Package.Name}
	}
	req := solv.ExpansionRequest{
		BuildRequires: reqs,
		Architecture:  event.Architecture,
	}

	res := c.resolveFromTargetSolv(ctx, event, req)
	if res == nil && c.solver != nil {
		res, _ = c.solver.Resolve(ctx, req)
	}

	if !checkDependencyMatch(res, event.Package) {
		return nil, nil
	}

	if c.isBlockedByDependencies(targetCfg.Flags.EffectiveBlock(), prjName, res, event) {
		return nil, nil
	}

	commit := pkg.Source.Commit
	if commit == "" {
		commit = "HEAD"
	}

	trig := v1.NewTrigger(prjName, pkg.Package.Name, pkg.Source.Git, commit, pkg.Source.Branch, "cascade-engine")
	trig.Priority = natsqueue.PriorityMed
	trig.Reason = "CASCADE"
	trig.Target = targetName
	trig.Architecture = event.Architecture

	if c.publisher != nil {
		subject := natsqueue.TriggerSubject(natsqueue.PriorityMed, prjName, pkg.Package.Name)
		data, _ := json.Marshal(trig)
		_ = c.publisher.Publish(ctx, subject, data)
	}

	return trig, nil
}

// checkDependencyMatch reports whether the resolved build environment contains
// the published package by exact name. Without a resolution result nothing is
// triggered: guessing from requirement names caused unrelated rebuilds.
func checkDependencyMatch(res *solv.ExpansionResult, eventPkg string) bool {
	if res == nil {
		return false
	}
	for _, p := range res.Packages {
		if p.Name == eventPkg {
			return true
		}
	}
	return false
}

func (c *CascadeEngine) isBlockedByDependencies(blockMode, prjName string, res *solv.ExpansionResult, event *v1.PublicationEvent) bool {
	if blockMode == "never" {
		return false
	}
	if res != nil {
		for _, dep := range res.Packages {
			if blockMode == "local" && prjName != event.Project {
				continue
			}
			if c.gatekeeper.IsBuilding(prjName, dep.Name) {
				return true
			}
		}
		return false
	}
	return c.gatekeeper.IsBuilding(event.Project, event.Package)
}

func targetDependsOnPublication(prjName, targetName string, targetCfg manifest.BuildTarget, event *v1.PublicationEvent) bool {
	if prjName == event.Project && targetName == event.Target {
		return true // Same project and target
	}

	for _, dep := range targetCfg.Dependencies {
		if dep.Project == event.Project && (dep.Target == "" || dep.Target == event.Target) {
			return true
		}
	}
	return false
}
