package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/cli"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/storage"
)

const relProject = "p:q"

// releaseStore has p resolved twice (1.0 vrev 7, then 2.0 vrev 1), q pending and
// build state rows with versrels.
func releaseStore(t *testing.T) *storage.MemoryStore {
	t.Helper()
	ctx := context.Background()
	store := storage.NewMemoryStore()
	pin := func(pkg, commit string) {
		pm := &manifest.PackageManifest{
			SchemaVersion: "v1", Package: manifest.PackageInfo{Name: pkg},
			Source: manifest.PackageSource{Git: "https://git.example/" + pkg, Commit: commit},
		}
		if err := store.UpsertPackage(ctx, relProject, pm); err != nil {
			t.Fatal(err)
		}
	}
	resolve := func(pkg, version string, git int) {
		revs, _ := store.ListRevisions(ctx, relProject, []string{pkg})
		for _, r := range revs {
			if r.State == storage.RevisionPending {
				if _, err := store.FinishRevision(ctx, r, storage.RevisionResult{Version: version, GitVrev: git}); err != nil {
					t.Fatal(err)
				}
				return
			}
		}
	}
	pin("p", "aaaaaaaaaaaaaaaaaaaa")
	resolve("p", "1.0", 7)
	pin("p", "bbbbbbbbbbbbbbbbbbbb")
	resolve("p", "2.0", 1)
	pin("q", "cccccccccccccccccccc")
	err := store.UpsertBuildStates(ctx, []*storage.PackageBuildState{
		{Project: relProject, Repo: "std", Arch: "x86_64", Package: "p", Flavor: "mini", State: storage.StateSucceeded, Versrel: "2.0-1", Bcnt: 3, SourceCommit: "bbbb"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

type releaseRun struct {
	out, err string
	failure  error
}

func runRelease(store storage.Store, args ...string) releaseRun {
	root := cli.NewRootCommand(store)
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"admin", "release"}, args...))
	err := root.Execute()
	return releaseRun{out.String(), errOut.String(), err}
}

func TestAdminReleaseShow(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantErr   string
		wantRevs  int
		wantPkgs  int
		wantUnits int
	}{
		{"latest revision of one package", []string{"show", relProject, "p", "--json"}, "", 1, 1, 1},
		{"whole log", []string{"show", relProject, "p", "--all-revisions", "--json"}, "", 2, 1, 1},
		{"all packages", []string{"show", relProject, "--json"}, "", 2, 2, 1},
		{"unknown package", []string{"show", relProject, "nope", "--json"}, "no revision log", 0, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := runRelease(releaseStore(t), tc.args...)
			if tc.wantErr != "" {
				if r.failure == nil || !strings.Contains(r.failure.Error(), tc.wantErr) {
					t.Fatalf("error %v, want %q", r.failure, tc.wantErr)
				}
				return
			}
			if r.failure != nil {
				t.Fatal(r.failure)
			}
			var rep struct {
				Project  string `json:"project"`
				Packages []struct {
					Package   string `json:"package"`
					Revisions []struct {
						Seq     int    `json:"seq"`
						State   string `json:"state"`
						Version string `json:"version"`
						Vrev    int    `json:"vrev"`
					} `json:"revisions"`
					Units []struct {
						Unit    string `json:"unit"`
						Versrel string `json:"versrel"`
						Bcnt    int    `json:"bcnt"`
					} `json:"units"`
				} `json:"packages"`
			}
			if err := json.Unmarshal([]byte(r.out), &rep); err != nil {
				t.Fatalf("stdout is not one JSON document: %v\n%s", err, r.out)
			}
			revs, units := 0, 0
			for _, p := range rep.Packages {
				revs += len(p.Revisions)
				units += len(p.Units)
			}
			if rep.Project != relProject || len(rep.Packages) != tc.wantPkgs || revs != tc.wantRevs || units != tc.wantUnits {
				t.Errorf("report %+v: want %d packages, %d revisions, %d units", rep, tc.wantPkgs, tc.wantRevs, tc.wantUnits)
			}
			if tc.name == "latest revision of one package" {
				p := rep.Packages[0]
				if p.Revisions[0].Seq != 2 || p.Revisions[0].Vrev != 1 || p.Revisions[0].Version != "2.0" ||
					p.Units[0].Unit != "p:mini" || p.Units[0].Versrel != "2.0-1" || p.Units[0].Bcnt != 3 {
					t.Errorf("package %+v", p)
				}
			}
		})
	}
}

func TestAdminReleaseShowHumanOutputIsPlain(t *testing.T) {
	r := runRelease(releaseStore(t), "show", relProject)
	if r.failure != nil {
		t.Fatal(r.failure)
	}
	for _, want := range []string{"p seq 2 commit bbbbbbbbbbbb state resolved version 2.0 git_vrev 1 vrev 1", "q seq 1", "state pending", "versrel 2.0-1 bcnt 3"} {
		if !strings.Contains(r.out, want) {
			t.Errorf("output lacks %q:\n%s", want, r.out)
		}
	}
	if strings.Contains(r.out, "\x1b") {
		t.Errorf("ANSI escape in output: %q", r.out)
	}
}

func TestAdminReleaseRecompute(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantErr     string
		wantApplied bool
		wantPkgs    []string
		wantState   string // state of p afterwards
		wantMin     int
	}{
		{"needs packages or --all", []string{"recompute", relProject, "--yes"}, "name the packages", false, nil, storage.RevisionResolved, 0},
		{"not both", []string{"recompute", relProject, "p", "--all", "--yes"}, "not both", false, nil, storage.RevisionResolved, 0},
		{"refuses without --yes", []string{"recompute", relProject, "p"}, "without confirmation", false, nil, storage.RevisionResolved, 0},
		{"unknown package", []string{"recompute", relProject, "nope", "--yes"}, "no revision log", false, nil, storage.RevisionResolved, 0},
		{"dry run changes nothing", []string{"recompute", relProject, "p", "--dry-run", "--json"}, "", false, []string{"p"}, storage.RevisionResolved, 0},
		{"pending packages are not affected", []string{"recompute", relProject, "--all", "--dry-run", "--json"}, "", false, []string{"p"}, storage.RevisionResolved, 0},
		{"applied keeps the old vrev as minimum", []string{"recompute", relProject, "p", "--yes", "--json"}, "", true, []string{"p"}, storage.RevisionPending, 1},
		{"allow decrease", []string{"recompute", relProject, "--all", "--allow-decrease", "--yes", "--json"}, "", true, []string{"p"}, storage.RevisionPending, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := releaseStore(t)
			r := runRelease(store, tc.args...)
			if tc.wantErr != "" {
				if r.failure == nil || !strings.Contains(r.failure.Error(), tc.wantErr) {
					t.Fatalf("error %v, want %q", r.failure, tc.wantErr)
				}
			} else {
				if r.failure != nil {
					t.Fatal(r.failure)
				}
				var ch struct {
					DryRun   bool     `json:"dry_run"`
					Applied  bool     `json:"applied"`
					Packages []string `json:"packages"`
				}
				if err := json.Unmarshal([]byte(r.out), &ch); err != nil {
					t.Fatalf("stdout is not one JSON document: %v\n%s", err, r.out)
				}
				if ch.Applied != tc.wantApplied || strings.Join(ch.Packages, ",") != strings.Join(tc.wantPkgs, ",") {
					t.Errorf("change %+v, want applied %v packages %v", ch, tc.wantApplied, tc.wantPkgs)
				}
			}
			revs, _ := store.ListRevisions(context.Background(), relProject, []string{"p"})
			last := revs[len(revs)-1]
			if last.State != tc.wantState || last.MinVrev != tc.wantMin {
				t.Errorf("p latest revision %+v, want state %s min vrev %d", last, tc.wantState, tc.wantMin)
			}
		})
	}
}

func TestAdminReleaseRecomputeWarningGoesToStderr(t *testing.T) {
	r := runRelease(releaseStore(t), "recompute", relProject, "p", "--allow-decrease", "--dry-run", "--json")
	if r.failure != nil || !strings.Contains(r.err, "warning: --allow-decrease") || strings.Contains(r.out, "warning") {
		t.Errorf("stderr %q stdout %q err %v", r.err, r.out, r.failure)
	}
}

func TestAdminReleaseSet(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantErr  string
		wantVrev int
		wantSrc  string
	}{
		{"vrev is required", []string{"set", relProject, "p", "--yes"}, "--vrev is required", 1, storage.RevisionSourceGit},
		{"unknown package", []string{"set", relProject, "nope", "--vrev", "5", "--yes"}, "no revision log", 0, ""},
		{"commit guard", []string{"set", relProject, "p", "--vrev", "5", "--commit", "aaaa", "--yes"}, "pinned to bbbbbbbbbbbb", 1, storage.RevisionSourceGit},
		{"refuses without --yes", []string{"set", relProject, "p", "--vrev", "5"}, "without confirmation", 1, storage.RevisionSourceGit},
		{"dry run", []string{"set", relProject, "p", "--vrev", "5", "--dry-run", "--json"}, "", 1, storage.RevisionSourceGit},
		{"applied", []string{"set", relProject, "p", "--vrev", "5", "--commit", "bbbb", "--yes", "--json"}, "", 5, storage.RevisionSourceAdmin},
		{"pending revision needs a version", []string{"set", relProject, "q", "--vrev", "5", "--yes"}, "no version yet", 0, ""},
		{"pending revision with version", []string{"set", relProject, "q", "--vrev", "5", "--version", "3.1", "--yes", "--json"}, "", 5, storage.RevisionSourceAdmin},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := releaseStore(t)
			r := runRelease(store, tc.args...)
			if tc.wantErr != "" {
				if r.failure == nil || !strings.Contains(r.failure.Error(), tc.wantErr) {
					t.Fatalf("error %v, want %q", r.failure, tc.wantErr)
				}
			} else if r.failure != nil {
				t.Fatal(r.failure)
			} else {
				var ch struct {
					Applied  bool                     `json:"applied"`
					Revision *storage.PackageRevision `json:"revision"`
				}
				if err := json.Unmarshal([]byte(r.out), &ch); err != nil || ch.Revision == nil {
					t.Fatalf("stdout is not one JSON document with a revision: %v\n%s", err, r.out)
				}
			}
			pkg := tc.args[2]
			if pkg == "nope" {
				return
			}
			revs, _ := store.ListRevisions(context.Background(), relProject, []string{pkg})
			last := revs[len(revs)-1]
			if last.Vrev != tc.wantVrev || last.Source != tc.wantSrc {
				t.Errorf("latest revision %+v, want vrev %d source %q", last, tc.wantVrev, tc.wantSrc)
			}
		})
	}
}
