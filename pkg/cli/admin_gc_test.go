package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/cli"
	"github.com/openSUSE/bsgo/pkg/gc"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/scheduler/coord"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func gcFixture(t *testing.T) (*storage.MemoryStore, *gc.MemoryObjects) {
	t.Helper()
	store, objs := storage.NewMemoryStore(), gc.NewMemoryObjects()
	p := buildrepo.Prpa{Project: "p", Repo: "standard", Arch: "x86_64"}
	old := time.Now().Add(-72 * time.Hour)
	for _, h := range []string{"cur", "old"} {
		objs.Put(buildrepo.SolvKey(p, h), []byte("s"), old)
		objs.Put(buildrepo.EntriesKey(p, h), []byte("[]"), old)
	}
	err := store.SaveBuildRepoIndex(context.Background(), &storage.BuildRepoIndex{
		Project: p.Project, Repo: p.Repo, Arch: p.Arch, ContentHash: "cur",
		SolvKey: buildrepo.SolvKey(p, "cur"), EntriesKey: buildrepo.EntriesKey(p, "cur"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, objs
}

func runGC(t *testing.T, store *storage.MemoryStore, objs *gc.MemoryObjects, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	open := func(_ context.Context, s storage.Store) (*cli.GCAdmin, error) {
		return &cli.GCAdmin{Collector: &gc.Collector{
			Objects: objs, Store: s.(gc.Store), Leases: &coord.Memory{}, Owner: "test",
		}}, nil
	}
	root := cli.NewRootCommand(store, cli.WithGCOpener(open))
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(append([]string{"admin", "gc"}, args...))
	err = root.Execute()
	return out.String(), errOut.String(), err
}

func TestAdminGC(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantErr     string
		wantObjects int
	}{
		{"non-interactive without --yes fails and deletes nothing", []string{"--prefix", "buildrepo"}, "pass --yes", 4},
		{"--json is never interactive", []string{"--prefix", "buildrepo", "--json"}, "pass --yes", 4},
		{"dry run deletes nothing", []string{"--prefix", "buildrepo", "--dry-run"}, "", 4},
		{"--yes deletes", []string{"--prefix", "buildrepo", "--keep-index-versions", "0", "--yes"}, "", 2},
		{"all prefixes", []string{"--prefix", "all", "--keep-index-versions", "0", "--yes"}, "", 2},
		{"project filter without match", []string{"--project", "other", "--yes"}, "", 4},
		{"invalid prefix", []string{"--prefix", "repos", "--yes"}, "invalid --prefix", 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, objs := gcFixture(t)
			_, _, err := runGC(t, store, objs, tt.args...)
			if tt.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("got %v, want %q", err, tt.wantErr)
			}
			if got := len(objs.Keys()); got != tt.wantObjects {
				t.Errorf("%d objects left, want %d: %v", got, tt.wantObjects, objs.Keys())
			}
		})
	}
}

func TestAdminGCJSONIsDeterministicAndClean(t *testing.T) {
	store, objs := gcFixture(t)
	first, stderr, err := runGC(t, store, objs, "--prefix", "buildrepo", "--keep-index-versions", "0", "--dry-run", "--json")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := runGC(t, store, objs, "--prefix", "buildrepo", "--keep-index-versions", "0", "--dry-run", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("output differs between runs:\n%s\n%s", first, second)
	}
	if strings.Contains(first, "\x1b") || stderr != "[GC] buildrepo: kept 2, would delete 2 objects, would free 3 B in 0s\n" {
		t.Errorf("stdout %q stderr %q", first, stderr)
	}
	var rep struct {
		DryRun  bool `json:"dry_run"`
		Results []struct {
			Prefix string `json:"prefix"`
			Plans  []struct {
				Prpa   string   `json:"prpa"`
				Kept   int      `json:"kept"`
				Delete []string `json:"delete"`
			} `json:"plans"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(first), &rep); err != nil {
		t.Fatal(err)
	}
	pl := rep.Results[0].Plans[0]
	if !rep.DryRun || pl.Prpa != "p/standard/x86_64" || pl.Kept != 2 || len(pl.Delete) != 2 {
		t.Errorf("report %+v", rep)
	}
}

func TestAdminGCHelpHasExamples(t *testing.T) {
	store, objs := gcFixture(t)
	out, _, err := runGC(t, store, objs, "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Examples:", "--dry-run", "--yes", "--json", "--prefix", "admin:gc", "--grace", "--project"} {
		if !strings.Contains(out, want) {
			t.Errorf("help lacks %q", want)
		}
	}
}
