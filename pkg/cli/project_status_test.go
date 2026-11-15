package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/openSUSE/bsgo/pkg/cli"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func TestProjectStatusReadsPersistedStates(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore()
	prj := "home:status"
	_ = store.CreateProject(ctx, &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: prj},
	})
	for _, n := range []string{"a", "b", "c"} {
		_ = store.UpsertPackage(ctx, prj, &manifest.PackageManifest{Package: manifest.PackageInfo{Name: n}})
	}
	rows := []storage.PackageBuildState{
		{Project: prj, Repo: "r1", Arch: "x86_64", Package: "a", State: storage.StateSucceeded},
		{Project: prj, Repo: "r1", Arch: "x86_64", Package: "b", State: storage.StateUnresolvable, Details: "nothing provides foo"},
		{Project: prj, Repo: "r1", Arch: "aarch64", Package: "b", State: storage.StateFailed, Details: "exit code 1"},
	}
	for i := range rows {
		if err := store.UpsertBuildState(ctx, &rows[i]); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name         string
		args         []string
		wantErr      bool
		wantPackages int
		wantCounts   map[string]int
	}{
		{"all", nil, false, 3, map[string]int{"succeeded": 1, "unresolvable": 1, "failed": 1}},
		{"arch filter", []string{"--arch", "x86_64"}, false, 2, map[string]int{"succeeded": 1, "unresolvable": 1}},
		{"state filter", []string{"--state", "unresolvable", "--repo", "r1"}, false, 1, map[string]int{"unresolvable": 1}},
		{"invalid state", []string{"--state", "bogus"}, true, 0, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := cli.NewRootCommand(store)
			out := new(bytes.Buffer)
			cmd.SetOut(out)
			cmd.SetErr(new(bytes.Buffer))
			cmd.SetArgs(append([]string{"project", "status", prj, "--json"}, tc.args...))
			err := cmd.Execute()
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var res cli.ProjectStatusResult
			if err := json.Unmarshal(out.Bytes(), &res); err != nil {
				t.Fatalf("invalid JSON %q: %v", out.String(), err)
			}
			if res.TotalPackages != 3 || len(res.Packages) != tc.wantPackages {
				t.Errorf("unexpected result: %+v", res)
			}
			for k, v := range tc.wantCounts {
				if res.Counts[k] != v {
					t.Errorf("counts[%s] = %d, want %d", k, res.Counts[k], v)
				}
			}
			for _, p := range res.Packages {
				if p.State == "unresolvable" && p.Details != "nothing provides foo" {
					t.Errorf("details missing: %+v", p)
				}
			}
		})
	}
}
