package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
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
}

type MemoryGatekeeper struct {
	mu           sync.RWMutex
	activeBuilds map[string]bool
}

func NewMemoryGatekeeper() *MemoryGatekeeper {
	return &MemoryGatekeeper{
		activeBuilds: make(map[string]bool),
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
	}
	return triggers, nil
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

func (c *CascadeEngine) checkPackageCascade(ctx context.Context, prjName, targetName string, targetCfg manifest.BuildTarget, pkg *manifest.PackageManifest, event *v1.PublicationEvent) (*v1.BuildTrigger, error) {
	req := solv.ExpansionRequest{
		BuildRequires: []string{pkg.Package.Name},
	}
	res, _ := c.solver.Resolve(ctx, req)

	if !checkDependencyMatch(res, prjName, event.Project, event.Package) {
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

func checkDependencyMatch(res *solv.ExpansionResult, prjName, eventPrj, eventPkg string) bool {
	if res != nil {
		for _, p := range res.Packages {
			if p.Name == eventPkg {
				return true
			}
		}
		return false
	}
	return prjName != eventPrj
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
