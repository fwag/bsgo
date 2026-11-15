package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// fixedSource serves a fixed snapshot honouring the state filter, like the store.
type fixedSource struct {
	rows []PackageStateEntry
}

func (f fixedSource) Snapshot(_ context.Context, _ string, flt storage.BuildStateFilter) (watchSnapshot, error) {
	counts := map[string]int{}
	var rows []PackageStateEntry
	for _, e := range f.rows {
		if (flt.Repo != "" && flt.Repo != e.Repo) || (flt.Arch != "" && flt.Arch != e.Arch) {
			continue
		}
		counts[e.State]++
		if flt.State == "" || flt.State == e.State {
			rows = append(rows, e)
		}
	}
	return watchSnapshot{Counts: counts, Rows: rows}, nil
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Errorf("%s mismatch\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestRunWatchOnceGolden(t *testing.T) {
	src := fixedSource{rows: sampleEntries()}
	tests := []struct {
		name   string
		params watchParams
		state  string
		json   bool
		golden string
	}{
		{"building table", watchParams{Project: "p"}, "building", false, "watch_once_building.golden"},
		{"building json", watchParams{Project: "p"}, "building", true, "watch_once_building_json.golden"},
		{"all json filtered by arch", watchParams{Project: "p", Arch: "aarch64"}, "", true, "watch_once_all_aarch64_json.golden"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := runWatchOnce(context.Background(), &out, src, tt.params, tt.state, OutputOptions{JSON: tt.json}); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.String(), "\x1b") {
				t.Error("output contains ANSI escapes")
			}
			checkGolden(t, tt.golden, out.String())
		})
	}
}

func TestWatchCommandEndToEnd(t *testing.T) {
	store := storage.NewMemoryStore()
	ctx := context.Background()
	if err := store.CreateProject(ctx, &manifest.ProjectManifest{SchemaVersion: "v1", Project: manifest.ProjectInfo{Name: "home:bot"}}); err != nil {
		t.Fatal(err)
	}
	err := store.UpsertBuildStates(ctx, []*storage.PackageBuildState{
		{Project: "home:bot", Repo: "standard", Arch: "x86_64", Package: "a", State: storage.StateBuilding, JobID: "job-a", UpdatedAt: time.Now().Add(-time.Minute)},
		{Project: "home:bot", Repo: "standard", Arch: "x86_64", Package: "b", State: storage.StateFailed, JobID: "job-b", UpdatedAt: time.Now()},
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		args    []string
		wantErr string
		check   func(t *testing.T, out string)
	}{
		{"positional default is building", []string{"watch", "home:bot", "--once", "--json"}, "", func(t *testing.T, out string) {
			var res WatchResult
			if err := json.Unmarshal([]byte(out), &res); err != nil {
				t.Fatal(err)
			}
			if res.State != "building" || len(res.Packages) != 1 || res.Packages[0].Package != "a" || res.Counts["failed"] != 1 {
				t.Errorf("unexpected result %+v", res)
			}
		}},
		{"project flag alias and state", []string{"watch", "-p", "home:bot", "--once", "--state", "failed", "--json"}, "", func(t *testing.T, out string) {
			if !strings.Contains(out, `"package": "b"`) || strings.Contains(out, `"package": "a"`) {
				t.Errorf("unexpected output %s", out)
			}
		}},
		{"missing project", []string{"watch", "--once"}, "known projects: home:bot", nil},
		{"unknown project", []string{"watch", "nope", "--once"}, `unknown project "nope" (known projects: home:bot)`, nil},
		{"bad state", []string{"watch", "home:bot", "--once", "--state", "bogus"}, "invalid --state", nil},
		{"conflicting project", []string{"watch", "home:bot", "-p", "x"}, "conflicting projects", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewRootCommand(store)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(tt.args)
			err := cmd.Execute()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, out.String())
		})
	}
}

func TestWriteHistoryJSON(t *testing.T) {
	log := &fakeStatusLog{last: 2, msgs: map[uint64]string{
		1: statusJSON("j1", "glibc", "BUILDING"),
		2: statusJSON("j1", "glibc", "SUCCEEDED"),
	}}
	var out bytes.Buffer
	p := watchParams{Project: "p", History: "glibc", Limit: 5}
	if err := writeHistory(context.Background(), &out, log, p, OutputOptions{JSON: true}); err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "watch_history_json.golden", out.String())

	out.Reset()
	if err := writeHistory(context.Background(), &out, &fakeStatusLog{}, p, OutputOptions{JSON: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"jobs": []`) {
		t.Errorf("empty history must print an empty array, got %s", out.String())
	}
}
