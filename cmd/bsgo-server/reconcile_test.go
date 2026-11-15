package main

import (
	"context"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/dispatcher"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func TestOrphanedBuilds(t *testing.T) {
	tests := []struct {
		name      string
		running   []string // job IDs reported by a worker heartbeat
		age       time.Duration
		wantOrphn bool
	}{
		{"recent build is not touched", nil, time.Minute, false},
		{"old build reported by a worker is kept", []string{"j1"}, time.Hour, false},
		{"old build nobody runs is orphaned", nil, time.Hour, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store := storage.NewMemoryStore()
			prj := &manifest.ProjectManifest{SchemaVersion: "v1", Project: manifest.ProjectInfo{Name: "p"},
				Targets: map[string]manifest.BuildTarget{"std": {Architectures: []string{"x86_64"}}}}
			if err := store.CreateProject(ctx, prj); err != nil {
				t.Fatal(err)
			}
			row := &storage.PackageBuildState{Project: "p", Repo: "std", Arch: "x86_64", Package: "a",
				State: storage.StateBuilding, JobID: "j1"}
			if err := store.UpsertBuildState(ctx, row); err != nil {
				t.Fatal(err)
			}
			disp := dispatcher.NewDispatcher(dispatcher.NewFuncPublisher(func(string, []byte) {}), time.Minute)
			disp.ObserveActiveJobs("w1", tt.running)
			got := orphanedBuilds(ctx, store, disp, time.Now().Add(tt.age))
			if (len(got) == 1) != tt.wantOrphn {
				t.Errorf("orphaned %d rows, want orphan=%v", len(got), tt.wantOrphn)
			}
		})
	}
}
