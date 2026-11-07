package scheduler_test

import (
	"context"
	"testing"

	"github.com/openSUSE/bsgo/pkg/manifest"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/scheduler"
	"github.com/openSUSE/bsgo/pkg/solv"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func TestCascadeEngineReverseDependencyResolution(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore()

	// Upstream Project: base:libs
	prjBase := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: "base:libs"},
		Targets: map[string]manifest.BuildTarget{
			"standard": {
				Architectures: []string{"x86_64"},
				Flags:         manifest.TargetFlags{Build: true, Rebuild: "transitive"},
			},
		},
	}
	_ = store.CreateProject(ctx, prjBase)

	pkgLib := &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package:       manifest.PackageInfo{Name: "libfoo"},
		Source:        manifest.PackageSource{Git: "https://git/libfoo.git", Commit: "111"},
		Build:         manifest.PackageBuild{Enabled: true},
	}
	_ = store.UpsertPackage(ctx, "base:libs", pkgLib)

	// Downstream Project 1: apps:transitive (Rebuild: transitive)
	prjTrans := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: "apps:transitive"},
		Targets: map[string]manifest.BuildTarget{
			"standard": {
				Architectures: []string{"x86_64"},
				Dependencies: []manifest.DependencySource{
					{Project: "base:libs", Target: "standard"},
				},
				Flags: manifest.TargetFlags{Build: true, Rebuild: "transitive", Block: "all"},
			},
		},
	}
	_ = store.CreateProject(ctx, prjTrans)

	pkgApp1 := &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package:       manifest.PackageInfo{Name: "app-transitive"},
		Source:        manifest.PackageSource{Git: "https://git/app1.git", Commit: "222"},
		Build:         manifest.PackageBuild{Enabled: true},
	}
	_ = store.UpsertPackage(ctx, "apps:transitive", pkgApp1)

	// Downstream Project 2: apps:local (Rebuild: local -> should NOT trigger on upstream publication)
	prjLocal := &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: "apps:local"},
		Targets: map[string]manifest.BuildTarget{
			"standard": {
				Architectures: []string{"x86_64"},
				Dependencies: []manifest.DependencySource{
					{Project: "base:libs", Target: "standard"},
				},
				Flags: manifest.TargetFlags{Build: true, Rebuild: "local"},
			},
		},
	}
	_ = store.CreateProject(ctx, prjLocal)

	pkgApp2 := &manifest.PackageManifest{
		SchemaVersion: "v1",
		Package:       manifest.PackageInfo{Name: "app-local"},
		Source:        manifest.PackageSource{Git: "https://git/app2.git", Commit: "333"},
		Build:         manifest.PackageBuild{Enabled: true},
	}
	_ = store.UpsertPackage(ctx, "apps:local", pkgApp2)

	// Mock solver: both app-transitive and app-local resolve to require libfoo
	solver := &cascadeMockSolver{
		deps: map[string][]string{
			"app-transitive": {"libfoo", "glibc"},
			"app-local":      {"libfoo", "glibc"},
			"libfoo":         {"glibc"},
		},
	}

	var publishedSubjects []string
	pub := scheduler.NewNatsPublisher(func(_ context.Context, subject string, _ []byte) error {
		publishedSubjects = append(publishedSubjects, subject)
		return nil
	})

	gatekeeper := scheduler.NewMemoryGatekeeper()
	engine := scheduler.NewCascadeEngine(store, solver, pub, gatekeeper)

	event := v1.NewPublicationEvent("base:libs", "standard", "x86_64", "libfoo", []string{"libfoo-1.0.rpm"}, "repomd.xml")

	// 1. Unblocked case: libfoo publication triggers app-transitive, but skips app-local
	triggers, err := engine.ProcessPublicationEvent(ctx, event)
	if err != nil {
		t.Fatalf("unexpected error processing publication: %v", err)
	}

	if len(triggers) != 1 {
		t.Fatalf("expected 1 cascade trigger, got %d", len(triggers))
	}
	trig := triggers[0]
	if trig.Package != "app-transitive" {
		t.Errorf("expected cascade trigger for app-transitive, got %s", trig.Package)
	}
	if trig.Priority != natsqueue.PriorityMed {
		t.Errorf("expected priority med, got %s", trig.Priority)
	}
	if trig.Reason != "CASCADE" {
		t.Errorf("expected reason CASCADE, got %s", trig.Reason)
	}

	// 2. Gatekeeper case: mark dependency as currently building
	gatekeeper.SetBuilding("apps:transitive", "libfoo", true)
	triggersBlocked, err := engine.ProcessPublicationEvent(ctx, event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(triggersBlocked) != 0 {
		t.Errorf("expected 0 triggers when dependency is actively building, got %d", len(triggersBlocked))
	}
}

type cascadeMockSolver struct {
	deps map[string][]string
}

func (m *cascadeMockSolver) Resolve(_ context.Context, req solv.ExpansionRequest) (*solv.ExpansionResult, error) {
	var pkgs []solv.ResolvedPackage
	for _, reqName := range req.BuildRequires {
		for _, dep := range m.deps[reqName] {
			pkgs = append(pkgs, solv.ResolvedPackage{Name: dep, EVR: "1.0", Arch: "x86_64"})
		}
	}
	return &solv.ExpansionResult{Packages: pkgs}, nil
}
