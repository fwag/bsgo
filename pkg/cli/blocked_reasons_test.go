package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/cli"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/storage"
)

const whyProject = "home:why"

var cycleReason = &storage.BlockedReason{
	Producers: []storage.BlockedProducer{{Unit: "libxml2", State: "building", Binaries: []string{"libxml2-devel"}}},
	Cycles: []storage.BlockedCycle{{
		Held:     []storage.BlockedProducer{{Unit: "zlib", State: "succeeded", Binaries: []string{"zlib-devel", "zlib-tools"}}},
		InFlight: []storage.BlockedProducer{{Unit: "gcc15", State: "building"}},
	}},
}

func whyStore(t *testing.T, blocked int) storage.Store {
	t.Helper()
	ctx := context.Background()
	store := storage.NewMemoryStore()
	if err := store.CreateProject(ctx, &manifest.ProjectManifest{SchemaVersion: "v1", Project: manifest.ProjectInfo{Name: whyProject}}); err != nil {
		t.Fatal(err)
	}
	add := func(row storage.PackageBuildState) {
		row.Project, row.Repo, row.Arch = whyProject, "standard", "x86_64"
		if err := store.UpsertPackage(ctx, whyProject, &manifest.PackageManifest{Package: manifest.PackageInfo{Name: row.Package}}); err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertBuildState(ctx, &row); err != nil {
			t.Fatal(err)
		}
	}
	for i := range blocked {
		add(storage.PackageBuildState{Package: fmt.Sprintf("app%02d", i), State: storage.StateBlocked,
			Details: cycleReason.Summary(), Blocked: cycleReason})
	}
	add(storage.PackageBuildState{Package: "broken-dep", State: storage.StateUnresolvable,
		Details: "nothing provides crypto-policies needed by broken-dep, nothing provides foo >= 2"})
	add(storage.PackageBuildState{Package: "fresh", State: storage.StateScheduled, Details: "meta change"})
	return store
}

func runWhyCLI(t *testing.T, store storage.Store, args ...string) (string, error) {
	t.Helper()
	cmd := cli.NewRootCommand(store)
	out, errOut := new(bytes.Buffer), new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestProjectStatusBlockedListIsCropped(t *testing.T) {
	store := whyStore(t, 20)
	tests := []struct {
		name    string
		args    []string
		want    []string
		notWant []string
	}{
		{"default sample", nil,
			[]string{"Blocked packages (15 of 20 shown; use --all or --limit N)", "app14 ", "dependency cycle not finished"},
			[]string{"app15 "}},
		{"limit", []string{"--limit", "2"},
			[]string{"Blocked packages (2 of 20 shown; use --all or --limit N)"}, []string{"app02 "}},
		{"all", []string{"--all"},
			[]string{"Blocked packages (20)", "app19 "}, []string{"shown"}},
		{"verbose", []string{"--limit", "1", "--verbose"},
			[]string{"libxml2 (building) provides libxml2-devel", "in flight: gcc15 building",
				"held only by the cycle: zlib (succeeded) provides zlib-devel, zlib-tools"}, nil},
		{"short list needs no hint", []string{"--state", "unresolvable"},
			[]string{"Unresolvable packages (1)"}, []string{"shown"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := runWhyCLI(t, store, append([]string{"project", "status", whyProject}, tt.args...)...)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tt.want {
				if !strings.Contains(out, w) {
					t.Errorf("output lacks %q:\n%s", w, out)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(out, w) {
					t.Errorf("output has %q:\n%s", w, out)
				}
			}
		})
	}
}

func TestProjectStatusRejectsNegativeLimit(t *testing.T) {
	if _, err := runWhyCLI(t, whyStore(t, 1), "project", "status", whyProject, "--limit", "-1"); err == nil {
		t.Fatal("expected an error")
	}
}

// TestProjectStatusJSONIsComplete: --json ignores the sample size and carries
// the full structured reason.
func TestProjectStatusJSONIsComplete(t *testing.T) {
	store := whyStore(t, 20)
	var first string
	for _, extra := range [][]string{nil, {"--limit", "1"}, {"--all", "--verbose"}} {
		out, err := runWhyCLI(t, store, append([]string{"project", "status", whyProject, "--json"}, extra...)...)
		if err != nil {
			t.Fatal(err)
		}
		if first == "" {
			first = out
		}
		if out != first {
			t.Errorf("--json differs with %v", extra)
		}
		if strings.Contains(out, "\x1b") {
			t.Error("ANSI escape in JSON")
		}
	}
	var res cli.ProjectStatusResult
	if err := json.Unmarshal([]byte(first), &res); err != nil {
		t.Fatal(err)
	}
	blocked := 0
	for _, p := range res.Packages {
		if p.State == storage.StateBlocked {
			blocked++
			if !p.Blocked.Equal(cycleReason) {
				t.Errorf("%s: reason %+v", p.Package, p.Blocked)
			}
		}
	}
	if blocked != 20 {
		t.Errorf("%d blocked packages in JSON, want 20", blocked)
	}
}

func TestPackageWhyBlockedText(t *testing.T) {
	out, err := runWhyCLI(t, whyStore(t, 1), "package", "why", whyProject+"/app00")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{
		"standard/x86_64: blocked",
		"libxml2-devel <- libxml2 (building)",
		"zlib-devel <- zlib (succeeded): not ready only because its dependency cycle is not finished; in flight: gcc15 building",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
}

func TestPackageWhyJSON(t *testing.T) {
	tests := []struct {
		pkg   string
		check func(t *testing.T, e cli.PackageWhyEntry)
	}{
		{"app00", func(t *testing.T, e cli.PackageWhyEntry) {
			if len(e.BlockedBy) != 3 || e.BlockedBy[0].Binary != "libxml2-devel" || e.BlockedBy[0].CycleOnly ||
				!e.BlockedBy[1].CycleOnly || e.BlockedBy[1].CycleInFlight[0].Unit != "gcc15" || !e.Blocked.Equal(cycleReason) {
				t.Errorf("blocked_by %+v", e.BlockedBy)
			}
		}},
		{"broken-dep", func(t *testing.T, e cli.PackageWhyEntry) {
			if len(e.Missing) != 2 || e.Missing[0].Requirement != "crypto-policies" || e.Missing[0].NeededBy != "broken-dep" ||
				e.Missing[1].Requirement != "foo >= 2" {
				t.Errorf("missing %+v", e.Missing)
			}
		}},
		{"fresh", func(t *testing.T, e cli.PackageWhyEntry) {
			if e.ScheduleReason != "meta change" {
				t.Errorf("schedule reason %q", e.ScheduleReason)
			}
		}},
	}
	store := whyStore(t, 1)
	for _, tt := range tests {
		t.Run(tt.pkg, func(t *testing.T) {
			out, err := runWhyCLI(t, store, "package", "why", whyProject+"/"+tt.pkg, "--json", "--arch", "x86_64", "--repo", "standard")
			if err != nil {
				t.Fatal(err)
			}
			var res cli.PackageWhyResult
			if err := json.Unmarshal([]byte(out), &res); err != nil || len(res.Entries) != 1 {
				t.Fatalf("bad JSON %q: %v", out, err)
			}
			tt.check(t, res.Entries[0])
		})
	}
}

func TestPackageWhyErrors(t *testing.T) {
	store := whyStore(t, 1)
	for _, args := range [][]string{
		{"package", "why", "nope"},
		{"package", "why", whyProject + "/missing"},
		{"package", "why", whyProject + "/app00", "--arch", "aarch64"},
		{"package", "why", whyProject + "/app00:flavor"},
	} {
		if _, err := runWhyCLI(t, store, args...); err == nil {
			t.Errorf("%v: expected an error", args)
		}
	}
}
