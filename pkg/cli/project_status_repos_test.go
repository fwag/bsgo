package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func sampleRepoStatus() *ProjectStatusResult {
	return &ProjectStatusResult{
		Project:       "p",
		TotalPackages: 4,
		Counts:        map[string]int{"succeeded": 2, "dispatching": 1, "blocked": 1},
		Succeeded:     2,
		Dispatching:   1,
		Repositories: []RepoStateEntry{
			{Repo: "standard", Arch: "aarch64", State: "published"},
			{Repo: "standard", Arch: "x86_64", State: "building", Details: "1 jobs in flight"},
		},
		Packages: []PackageStateEntry{},
	}
}

func TestProjectStatusRepositoriesGolden(t *testing.T) {
	tests := []struct {
		name   string
		json   bool
		golden string
	}{
		{"human", false, "project_status_repos.golden"},
		{"json", true, "project_status_repos_json.golden"},
	}
	res := sampleRepoStatus()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := Render(&out, formatProjectStatusHuman(res), res, "2/4", OutputOptions{JSON: tt.json}); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.String(), "\x1b") {
				t.Error("output contains ANSI escapes")
			}
			checkGolden(t, tt.golden, out.String())
		})
	}
}

func TestLoadRepoStatesAndWatchSummaries(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore()
	prj := &manifest.ProjectManifest{SchemaVersion: "v1", Project: manifest.ProjectInfo{Name: "p"},
		Targets: map[string]manifest.BuildTarget{"std": {Architectures: []string{"x86_64", "aarch64"}}}}
	if err := store.CreateProject(ctx, prj); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPrpaState(ctx, &storage.PrpaState{Project: "p", Repo: "std", Arch: "x86_64", State: storage.RepoBlocked, Details: "2 units blocked"}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		filter     storage.BuildStateFilter
		wantRepos  int
		wantStates map[string]string // arch -> repo state of the watch summary
	}{
		{"all", storage.BuildStateFilter{}, 1, map[string]string{"x86_64": "blocked", "aarch64": ""}},
		{"arch without state", storage.BuildStateFilter{Arch: "aarch64"}, 0, map[string]string{"aarch64": ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := loadProjectStatus(ctx, store, "p", tt.filter)
			if err != nil || len(res.Repositories) != tt.wantRepos {
				t.Fatalf("repositories %+v, %v; want %d", res.Repositories, err, tt.wantRepos)
			}
			snap, err := storeStateSource{store: store, now: time.Now}.Snapshot(ctx, "p", tt.filter)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range snap.Summaries {
				if s.RepoState != tt.wantStates[s.Arch] {
					t.Errorf("%s/%s repo state %q, want %q", s.Repo, s.Arch, s.RepoState, tt.wantStates[s.Arch])
				}
			}
			if len(snap.Summaries) != len(tt.wantStates) {
				t.Errorf("%d summaries, want %d", len(snap.Summaries), len(tt.wantStates))
			}
		})
	}
}
