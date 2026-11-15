package scheduler_test

import (
	"context"
	"testing"

	"github.com/openSUSE/bsgo/pkg/manifest"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/scheduler"
	"github.com/openSUSE/bsgo/pkg/solv"
	"github.com/openSUSE/bsgo/pkg/storage"
)

type stubSolver struct {
	names []string
}

func (s stubSolver) Resolve(_ context.Context, _ solv.ExpansionRequest) (*solv.ExpansionResult, error) {
	res := &solv.ExpansionResult{}
	for _, n := range s.names {
		res.Packages = append(res.Packages, solv.ResolvedPackage{Name: n})
	}
	return res, nil
}

// TestCascadeExactNameMatchOnly guards against the former HasPrefix matching,
// which retriggered unrelated packages (for example "7zip" for event "7z").
func TestCascadeExactNameMatchOnly(t *testing.T) {
	tests := []struct {
		name      string
		solver    scheduler.Solver
		wantTrigs int
	}{
		{"exact name in resolution triggers", stubSolver{names: []string{"libfoo", "glibc"}}, 1},
		{"name sharing a prefix does not trigger", stubSolver{names: []string{"libfoo-devel", "libfoo2"}}, 0},
		{"no resolution result does not trigger", nil, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := storage.NewMemoryStore()
			_ = store.CreateProject(ctx, &manifest.ProjectManifest{
				SchemaVersion: "v1",
				Project:       manifest.ProjectInfo{Name: "my:project"},
				Targets: map[string]manifest.BuildTarget{
					"standard": {
						Architectures: []string{"x86_64"},
						Flags:         manifest.TargetFlags{Build: true, Rebuild: "transitive"},
					},
				},
			})
			_ = store.UpsertPackage(ctx, "my:project", &manifest.PackageManifest{
				SchemaVersion: "v1",
				Package:       manifest.PackageInfo{Name: "app"},
				Source:        manifest.PackageSource{Git: "https://git/app.git", Commit: "1"},
				Build:         manifest.PackageBuild{Enabled: true},
			})

			engine := scheduler.NewCascadeEngine(store, tc.solver, nil, scheduler.NewMemoryGatekeeper())
			ev := v1.NewPublicationEvent("my:project", "standard", "x86_64", "libfoo", nil, "")
			trigs, err := engine.ProcessPublicationEvent(ctx, ev)
			if err != nil {
				t.Fatalf("ProcessPublicationEvent failed: %v", err)
			}
			if len(trigs) != tc.wantTrigs {
				t.Errorf("expected %d triggers, got %d", tc.wantTrigs, len(trigs))
			}
		})
	}
}
