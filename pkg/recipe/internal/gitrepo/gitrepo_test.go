package gitrepo_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/recipe/internal/gitrepo"
	"github.com/openSUSE/bsgo/pkg/recipe/internal/gittest"
)

func open(t *testing.T, r *gittest.Repo, tip string) *gitrepo.Repo {
	t.Helper()
	g := gitrepo.New(t.TempDir(), r.URL(), tip)
	if err := g.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := g.FetchHistory(context.Background()); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestFirstParent(t *testing.T) {
	gittest.Each(t, func(t *testing.T, format string) {
		r := gittest.New(t, format)
		c1 := r.Commit("1", map[string]string{"a.spec": "v1"})
		c2 := r.Commit("2", map[string]string{"b.txt": "x"})
		m := r.Merge(3, nil)
		c4 := r.Commit("4", map[string]string{"a.spec": "v2"})
		g := open(t, r, c4)
		tests := []struct {
			name     string
			max      int
			want     []string
			wantMore bool
			wantErr  bool
		}{
			{"all, merge counts once", 10, []string{c4, m, c2, c1}, false, false},
			{"exact", 4, []string{c4, m, c2, c1}, false, false},
			{"capped", 2, []string{c4, m}, true, false},
			{"invalid limit", 0, nil, false, true},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got, more, err := g.FirstParent(context.Background(), tt.max)
				if (err != nil) != tt.wantErr {
					t.Fatalf("err = %v", err)
				}
				if !reflect.DeepEqual(got, tt.want) || more != tt.wantMore {
					t.Errorf("got %v more=%v, want %v more=%v", got, more, tt.want, tt.wantMore)
				}
			})
		}
	})
}

func TestRecipeChanges(t *testing.T) {
	gittest.Each(t, func(t *testing.T, format string) {
		r := gittest.New(t, format)
		c1 := r.Commit("1", map[string]string{"a.spec": "v1", "README": "r"})
		c2 := r.Commit("2", map[string]string{"README": "r2"})
		m := r.Merge(2, map[string]string{"b.spec": "side"})
		c4 := r.Commit("4", map[string]string{"a.spec": ""})
		g := open(t, r, c4)

		all, err := g.RecipeChanges(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}
		var commits []string
		for _, c := range all {
			commits = append(commits, c.Commit)
		}
		if want := []string{c1, m, c4}; !reflect.DeepEqual(commits, want) {
			t.Fatalf("commits %v, want %v (c2=%s: no recipe change must be omitted)", commits, want, c2)
		}
		if f := all[1].Files; len(f) != 1 || f[0].Name != "b.spec" || f[0].Deleted || f[0].OID == "" {
			t.Errorf("merge step files %+v: the side branch change appears at the merge", f)
		}
		if f := all[2].Files; len(f) != 1 || f[0].Name != "a.spec" || !f[0].Deleted {
			t.Errorf("delete step files %+v", f)
		}
		since, err := g.RecipeChanges(context.Background(), c1)
		if err != nil || len(since) != 2 || since[0].Commit != m {
			t.Errorf("since base: %+v err=%v", since, err)
		}
		if _, err := g.RecipeChanges(context.Background(), "main"); err == nil {
			t.Error("a non-id base must be rejected")
		}
	})
}

func TestFetchAndReadBlobs(t *testing.T) {
	gittest.Each(t, func(t *testing.T, format string) {
		r := gittest.New(t, format)
		r.Commit("1", map[string]string{"a.spec": "one", "b.spec": "two", "big.tar": strings.Repeat("x", 1<<16)})
		tip := r.Commit("2", map[string]string{"a.spec": "three"})
		g := open(t, r, tip)
		ents, err := g.Tree(context.Background(), tip)
		if err != nil {
			t.Fatal(err)
		}
		oid := map[string]string{}
		for _, e := range ents {
			oid[e.Name] = e.OID
		}
		ids := []string{oid["a.spec"], oid["b.spec"]}

		if _, err := g.ReadBlobs(context.Background(), ids); err == nil || !strings.Contains(err.Error(), "missing") {
			t.Fatalf("blob not fetched yet must be an error without lazy fetch, got %v", err)
		}
		n, err := g.FetchBlobs(context.Background(), ids)
		if err != nil || n != 2 {
			t.Fatalf("FetchBlobs = %d, %v", n, err)
		}
		if n, err := g.FetchBlobs(context.Background(), ids); err != nil || n != 0 {
			t.Errorf("second FetchBlobs = %d, %v; present blobs must not be fetched again", n, err)
		}
		got, err := g.ReadBlobs(context.Background(), ids)
		if err != nil || string(got[ids[0]]) != "three" || string(got[ids[1]]) != "two" {
			t.Errorf("ReadBlobs = %q, %v", got, err)
		}
		if _, err := g.ReadBlobs(context.Background(), []string{"HEAD"}); err == nil {
			t.Error("a non-id must be rejected")
		}
		unknown := strings.Repeat("a", len(ids[0]))
		if _, err := g.FetchBlobs(context.Background(), []string{unknown}); err == nil {
			t.Error("fetching an unknown blob must fail")
		}
	})
}

func TestFetchErrors(t *testing.T) {
	missing := strings.Repeat("a", 40)
	tests := []struct{ name, url, commit string }{
		{"unreachable", "file://" + t.TempDir() + "/nope.git", missing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := gitrepo.New(t.TempDir(), tt.url, tt.commit)
			if err := g.Init(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := g.FetchHistory(context.Background()); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestValidateSource(t *testing.T) {
	ok := strings.Repeat("a", 64)
	tests := []struct {
		url, commit string
		wantErr     bool
	}{
		{"https://x/y.git", ok, false},
		{"https://x/y.git", strings.Repeat("b", 40), false},
		{"", ok, true},
		{"--upload-pack=x", ok, true},
		{"https://x/\ny", ok, true},
		{"https://x/y.git", "main", true},
		{"https://x/y.git", strings.Repeat("A", 40), true},
	}
	for _, tt := range tests {
		if err := gitrepo.ValidateSource(tt.url, tt.commit); (err != nil) != tt.wantErr {
			t.Errorf("ValidateSource(%q,%q) = %v", tt.url, tt.commit, err)
		}
	}
}
