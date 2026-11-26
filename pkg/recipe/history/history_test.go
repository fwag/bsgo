package history

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/recipe/internal/gittest"
)

func spec(version string, extra ...string) string {
	return "Name: foo\nVersion: " + version + "\n" + strings.Join(extra, "\n") + "\n"
}

func foo(content string) map[string]string { return map[string]string{"foo.spec": content} }

type mapMemo map[string]*Point

func (m mapMemo) Nearest(_ context.Context, _ Key, commits []string) (*Point, error) {
	for _, c := range commits {
		if p, ok := m[c]; ok {
			return p, nil
		}
	}
	return nil, nil
}

type scenario struct {
	name    string
	build   func(r *gittest.Repo) string // returns the tip
	req     func(r Request) Request      // optional request tweak
	version string
	vrev    int
}

func linear(r *gittest.Repo, versions ...string) string {
	var tip string
	for _, v := range versions {
		tip = r.Commit("c", foo(spec(v)))
	}
	return tip
}

func scenarios() []scenario {
	return []scenario{
		{"linear same version", func(r *gittest.Repo) string { return linear(r, "1.0", "1.0", "1.0") }, nil, "1.0", 3},
		{"version bump restarts", func(r *gittest.Repo) string { return linear(r, "1.0", "1.0", "2.0") }, nil, "2.0", 1},
		{"revert of a bump continues", func(r *gittest.Repo) string {
			return linear(r, "1.0", "1.0", "1.0", "2.0", "2.0", "1.0")
		}, nil, "1.0", 4},
		{"commits not touching the spec count", func(r *gittest.Repo) string {
			linear(r, "1.0")
			r.Commit("a", map[string]string{"README": "a"})
			return r.Commit("b", map[string]string{"README": "b"})
		}, nil, "1.0", 3},
		{"merge counts once", func(r *gittest.Repo) string {
			r.Commit("c1", foo(spec("1.0")))
			return r.Merge(3, map[string]string{"extra.txt": "x"})
		}, nil, "1.0", 2},
		{"spec changed on the side branch", func(r *gittest.Repo) string {
			r.Commit("c1", foo(spec("1.0")))
			return r.Merge(2, foo(spec("2.0")))
		}, nil, "2.0", 1},
		{"renamed spec, lone spec is the recipe", func(r *gittest.Repo) string {
			r.Commit("c1", foo(spec("1.0")))
			r.Git("mv", "foo.spec", "other.spec")
			return r.Commit("c2", nil)
		}, nil, "1.0", 2},
		{"ambiguous specs without pin are unknown", func(r *gittest.Repo) string {
			return r.Commit("c1", map[string]string{"a.spec": spec("1.0"), "b.spec": spec("2.0")})
		}, nil, Unknown, 1},
		{"pinned recipe", func(r *gittest.Repo) string {
			r.Commit("c1", map[string]string{"a.spec": spec("1.0"), "b.spec": spec("2.0")})
			return r.Commit("c2", map[string]string{"a.spec": spec("1.1")})
		}, func(q Request) Request { q.Pinned = "b.spec"; return q }, "2.0", 2},
		{"no recipe is unknown", func(r *gittest.Repo) string {
			r.Commit("c1", map[string]string{"README": "a"})
			return r.Commit("c2", map[string]string{"README": "b"})
		}, nil, Unknown, 2},
		{"known to unknown to known", func(r *gittest.Repo) string {
			r.Commit("c1", foo(spec("1.0")))
			r.Commit("c2", foo(""))
			return r.Commit("c3", foo(spec("1.0")))
		}, nil, "1.0", 3},
		{"release start value", func(r *gittest.Repo) string {
			return r.Commit("c1", foo(spec("1.0", "Release: 7")))
		}, nil, "1.0", 7},
		{"release start raises the first step only", func(r *gittest.Repo) string {
			r.Commit("c1", foo(spec("1.0", "Release: 2")))
			r.Commit("c2", foo(spec("1.0", "Release: 2", "# x")))
			return r.Commit("c3", foo(spec("1.0", "Release: 2", "# y")))
		}, nil, "1.0", 4},
		{"force multiversion is unknown", func(r *gittest.Repo) string {
			return r.Commit("c1", foo(spec("1.0", "#!ForceMultiVersion")))
		}, nil, Unknown, 1},
		{"spec without version is unknown", func(r *gittest.Repo) string {
			return r.Commit("c1", foo("Name: foo\n"))
		}, nil, Unknown, 1},
		{"multibuild uses the main spec", func(r *gittest.Repo) string {
			r.Commit("c1", map[string]string{
				"foo.spec": spec("6.12.0"), "foo-azure.spec": "Name: foo-azure\nVersion: 6.12.9\n",
				"_multibuild": "<multibuild><flavor>azure</flavor></multibuild>",
			})
			return r.Commit("c2", map[string]string{"foo-azure.spec": "Name: foo-azure\nVersion: 6.13.0\n"})
		}, nil, "6.12.0", 2},
		{"non-spec recipe is unknown", func(r *gittest.Repo) string {
			return r.Commit("c1", map[string]string{"foo.kiwi": "<image/>"})
		}, nil, Unknown, 1},
		{"kernel-source-like: old versions, long run", func(r *gittest.Repo) string {
			var vs []string
			for i := 0; i < 20; i++ {
				vs = append(vs, fmt.Sprintf("3.%d", i))
			}
			for i := 0; i < 25; i++ {
				vs = append(vs, "6.12.0")
			}
			return linear(r, vs...)
		}, nil, "6.12.0", 25},
	}
}

func TestResolve(t *testing.T) {
	for _, sc := range scenarios() {
		t.Run(sc.name, func(t *testing.T) {
			gittest.Each(t, func(t *testing.T, format string) {
				r := gittest.New(t, format)
				tip := sc.build(r)
				req := Request{URL: r.URL(), Commit: tip, Lookup: "foo"}
				if sc.req != nil {
					req = sc.req(req)
				}
				start := time.Now()
				res, err := Resolve(context.Background(), req, nil)
				if err != nil {
					t.Fatal(err)
				}
				if res.Version != sc.version || res.GitVrev != sc.vrev || res.Commit != tip {
					t.Errorf("got version %q vrev %d commit %s, want %q %d %s", res.Version, res.GitVrev, res.Commit, sc.version, sc.vrev, tip)
				}
				t.Logf("walked %d blobs %d in %s", res.Walked, res.BlobsFetched, time.Since(start).Round(time.Millisecond))
			})
		})
	}
}

func TestMainRecipeName(t *testing.T) {
	gittest.Each(t, func(t *testing.T, format string) {
		r := gittest.New(t, format)
		tip := r.Commit("c1", map[string]string{"foo.spec": spec("1.0"), "foo-x.spec": spec("2.0")})
		res, err := Resolve(context.Background(), Request{URL: r.URL(), Commit: tip, Lookup: "foo"}, nil)
		if err != nil || res.MainRecipe != "foo.spec" || res.Version != "1.0" {
			t.Errorf("got %+v err=%v", res, err)
		}
	})
}

func TestBlobsFetchedOncePerDistinctBlob(t *testing.T) {
	gittest.Each(t, func(t *testing.T, format string) {
		r := gittest.New(t, format)
		linear(r, "1.0", "2.0", "1.0", "2.0")
		tip := linear(r, "1.0")
		res, err := Resolve(context.Background(), Request{URL: r.URL(), Commit: tip, Lookup: "foo"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.BlobsFetched != 2 || res.Walked != 5 {
			t.Errorf("blobs %d walked %d, want 2 and 5", res.BlobsFetched, res.Walked)
		}
	})
}

func TestMemoBaseGivesSameResult(t *testing.T) {
	gittest.Each(t, func(t *testing.T, format string) {
		r := gittest.New(t, format)
		linear(r, "1.0", "1.0", "2.0")
		mid := linear(r, "2.0", "1.0")
		r.Commit("readme", map[string]string{"README": "x"})
		tip := linear(r, "1.0", "1.0", "3.0", "1.0")
		req := Request{URL: r.URL(), Commit: tip, Lookup: "foo"}

		full, err := Resolve(context.Background(), req, nil)
		if err != nil {
			t.Fatal(err)
		}
		midReq := req
		midReq.Commit = mid
		midRes, err := Resolve(context.Background(), midReq, nil)
		if err != nil {
			t.Fatal(err)
		}
		memo := mapMemo{mid: &midRes.Point}
		inc, err := Resolve(context.Background(), req, memo)
		if err != nil {
			t.Fatal(err)
		}
		if inc.Version != full.Version || inc.GitVrev != full.GitVrev || fmt.Sprint(inc.VT) != fmt.Sprint(full.VT) {
			t.Errorf("incremental %+v != full %+v", inc, full)
		}
		if inc.Walked != 5 || full.Walked != 10 || inc.BlobsFetched >= full.BlobsFetched {
			t.Errorf("walked %d/%d blobs %d/%d", inc.Walked, full.Walked, inc.BlobsFetched, full.BlobsFetched)
		}
		if len(midRes.VT) == 0 || midRes.VT["1.0"] == 0 {
			t.Errorf("memo point modified or empty: %v", midRes.VT)
		}
		same, err := Resolve(context.Background(), midReq, memo)
		if err != nil || same.Walked != 0 || same.GitVrev != midRes.GitVrev {
			t.Errorf("memo hit at the tip: %+v err=%v", same, err)
		}
	})
}

func TestWalkCap(t *testing.T) {
	gittest.Each(t, func(t *testing.T, format string) {
		r := gittest.New(t, format)
		tip := linear(r, "1.0", "1.0", "1.0", "1.0", "2.0", "2.0")
		res, err := Resolve(context.Background(), Request{URL: r.URL(), Commit: tip, Lookup: "foo", MaxWalk: 3}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Truncated || res.Walked != 3 || res.GitVrev != 2 || res.Version != "2.0" {
			t.Errorf("got %+v", res)
		}
		res, err = Resolve(context.Background(), Request{URL: r.URL(), Commit: tip, Lookup: "foo", MaxWalk: 6}, nil)
		if err != nil || res.Truncated || res.Walked != 6 {
			t.Errorf("exact fit: %+v err=%v", res, err)
		}
	})
}

func TestResolveErrors(t *testing.T) {
	r := gittest.New(t, "sha1")
	tip := linear(r, "1.0")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name string
		ctx  context.Context
		req  Request
	}{
		{"unknown commit", context.Background(), Request{URL: r.URL(), Commit: strings.Repeat("a", 40), Lookup: "foo"}},
		{"branch name", context.Background(), Request{URL: r.URL(), Commit: "main", Lookup: "foo"}},
		{"option as url", context.Background(), Request{URL: "--upload-pack=x", Commit: tip, Lookup: "foo"}},
		{"unreachable", context.Background(), Request{URL: "file://" + t.TempDir() + "/nope", Commit: tip, Lookup: "foo"}},
		{"empty package", context.Background(), Request{URL: r.URL(), Commit: tip}},
		{"cancelled", cancelled, Request{URL: r.URL(), Commit: tip, Lookup: "foo"}},
		{"timeout", context.Background(), Request{URL: r.URL(), Commit: tip, Lookup: "foo", Timeout: time.Nanosecond}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if res, err := Resolve(tt.ctx, tt.req, nil); err == nil {
				t.Errorf("expected error, got %+v", res)
			}
		})
	}
}

func TestMemoErrorPropagates(t *testing.T) {
	r := gittest.New(t, "sha1")
	tip := linear(r, "1.0")
	if _, err := Resolve(context.Background(), Request{URL: r.URL(), Commit: tip, Lookup: "foo"}, failMemo{}); err == nil {
		t.Error("memo error must be returned")
	}
}

type failMemo struct{}

func (failMemo) Nearest(context.Context, Key, []string) (*Point, error) {
	return nil, fmt.Errorf("db down")
}

func TestReleaseStart(t *testing.T) {
	tests := []struct {
		release string
		want    int
	}{
		{"", 0}, {"7", 7}, {"3.<B_CNT>", 3}, {"<RELEASE5>", 5}, {"<RELEASE>.g1", 0},
		{"12.1", 12}, {"lp.<B_CNT>", 0}, {"0.1", 0}, {"2.3.<B_CNT>", 3},
	}
	for _, tt := range tests {
		if got := releaseStart(tt.release); got != tt.want {
			t.Errorf("releaseStart(%q) = %d, want %d", tt.release, got, tt.want)
		}
	}
}

func TestFold(t *testing.T) {
	mk := func(vs ...string) []step {
		var s []step
		for i, v := range vs {
			s = append(s, step{Commit: fmt.Sprint(i), Version: v})
		}
		return s
	}
	tests := []struct {
		name  string
		base  *Point
		steps []step
		want  int
	}{
		{"same", nil, mk("a", "a", "a"), 3},
		{"restart", nil, mk("a", "a", "b"), 1},
		{"revert continues", nil, mk("a", "a", "b", "a"), 3},
		{"unknown then known", nil, mk("a", "a", Unknown, "a"), 4},
		{"unknown first", nil, mk(Unknown, "a"), 2},
		{"release start raises", nil, nil, 9},
		{"base", &Point{Version: "a", VT: map[string]int{"a": 5}}, mk("a"), 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps := tt.steps
			if tt.base == nil && len(steps) == 0 {
				steps = []step{{Version: "a", ReleaseStart: 9}}
			}
			p := fold(tt.base, steps)
			if got := p.VT[p.Version]; got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
	base := &Point{Version: "a", VT: map[string]int{"a": 5}}
	fold(base, mk("a"))
	if base.VT["a"] != 5 {
		t.Error("fold modified the base")
	}
}
