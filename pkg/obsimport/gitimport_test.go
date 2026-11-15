package obsimport

import (
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/manifest"
)

func TestURLResolverResolve(t *testing.T) {
	tests := []struct {
		name    string
		res     URLResolver
		raw     string
		want    string
		wantErr bool
	}{
		{"relative against gitea base", URLResolver{GiteaBase: "https://forge.example/"}, "../../pool/foo", "https://forge.example/pool/foo.git", false},
		{"relative against superproject", URLResolver{Superproject: "https://forge.example/products/SLFO.git"}, "../../pool/foo", "https://forge.example/pool/foo.git", false},
		{"one level up", URLResolver{Superproject: "https://forge.example/products/SLFO.git"}, "../other", "https://forge.example/products/other.git", false},
		{"too many ups", URLResolver{Superproject: "https://forge.example/SLFO.git"}, "../../../x", "", true},
		{"relative without any base", URLResolver{}, "../../pool/foo", "", true},
		{"relative with ssh remote", URLResolver{Superproject: "git@forge.example:products/SLFO.git"}, "../../pool/foo", "", true},
		{"absolute http gets suffix", URLResolver{}, "https://h.example/pool/foo", "https://h.example/pool/foo.git", false},
		{"absolute with suffix unchanged", URLResolver{}, "https://h.example/pool/foo.git", "https://h.example/pool/foo.git", false},
		{"ssh unchanged", URLResolver{}, "git@h.example:pool/foo", "git@h.example:pool/foo", false},
		{"empty", URLResolver{GiteaBase: "https://x"}, "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.res.Resolve(tc.raw)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("Resolve(%q) = %q, %v; want %q (err=%t)", tc.raw, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestParseLsTree(t *testing.T) {
	c40 := strings.Repeat("a", 40)
	c64 := strings.Repeat("b", 64)
	tests := []struct {
		name    string
		in      string
		want    []Gitlink
		wantErr bool
	}{
		{"gitlinks only", "100644 blob " + c40 + "\t_config\x00160000 commit " + c40 + "\tfoo\x00160000 commit " + c64 + "\tbar\x00040000 tree " + c40 + "\tdir\x00",
			[]Gitlink{{"foo", c40}, {"bar", c64}}, false},
		{"empty", "", nil, false},
		{"short commit", "160000 commit abc123\tfoo\x00", nil, true},
		{"garbage", "nonsense\x00", nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseLsTree([]byte(tc.in))
			if (err != nil) != tc.wantErr || len(got) != len(tc.want) {
				t.Fatalf("got %v, %v", got, err)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("entry %d = %v, want %v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

const testModules = `[submodule "a"]
	path = a
	url = ../../pool/a
	branch = br
[submodule "nobranch"]
	path = nobranch
	url = ../../pool/nb
[submodule "onlymod"]
	path = onlymod
	url = ../../pool/onlymod
[submodule "nourl"]
	path = nourl
`

func TestBuildPackages(t *testing.T) {
	c := strings.Repeat("c", 40)
	mods := ParseGitmodules(testModules)
	links := []Gitlink{{"a", c}, {"nobranch", c}, {"nourl", c}, {"onlytree", c}}
	res := URLResolver{GiteaBase: "https://f.example"}
	tests := []struct {
		name         string
		defBranch    string
		wantBranchNB string
	}{
		{"no default branch", "", ""},
		{"default branch", "dflt", "dflt"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pkgs, rep := BuildPackages(mods, links, res, tc.defBranch)
			if len(pkgs) != 2 || rep.Packages != 2 {
				t.Fatalf("pkgs = %d", len(pkgs))
			}
			a, nb := pkgs[0], pkgs[1]
			if a.Package.Name != "a" || a.Source.Git != "https://f.example/pool/a.git" || a.Source.Branch != "br" || a.Source.Commit != c {
				t.Errorf("a = %+v", a.Source)
			}
			if nb.Source.Branch != tc.wantBranchNB || a.Source.Recipe != "" || !a.Build.Enabled || a.Resources != (manifest.ResourceLimits{}) {
				t.Errorf("nb = %+v, a = %+v", nb, a)
			}
			if strings.Join(rep.MissingInTree, ",") != "onlymod" || strings.Join(rep.MissingInModules, ",") != "onlytree" ||
				len(rep.Skipped) != 1 || rep.Skipped[0].Package != "nourl" {
				t.Errorf("report = %+v", rep)
			}
		})
	}
}

func TestBuildPackagesRejectsTraversalName(t *testing.T) {
	c := strings.Repeat("d", 40)
	mods := []Submodule{{Path: "../evil", URL: "https://h/x"}}
	pkgs, rep := BuildPackages(mods, []Gitlink{{"../evil", c}}, URLResolver{}, "")
	if len(pkgs) != 0 || len(rep.Skipped) != 1 {
		t.Fatalf("pkgs=%v rep=%+v", pkgs, rep)
	}
}

func TestNeedsBase(t *testing.T) {
	rel := []Submodule{{URL: "../../pool/x"}}
	abs := []Submodule{{URL: "https://h/x"}}
	tests := []struct {
		name string
		res  URLResolver
		mods []Submodule
		want bool
	}{
		{"relative, nothing known", URLResolver{}, rel, true},
		{"relative, gitea base", URLResolver{GiteaBase: "https://h"}, rel, false},
		{"relative, https remote", URLResolver{Superproject: "https://h/a/b.git"}, rel, false},
		{"absolute only", URLResolver{}, abs, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.res.NeedsBase(tc.mods); got != tc.want {
				t.Errorf("got %v", got)
			}
		})
	}
}

func testProject() *manifest.ProjectManifest {
	return &manifest.ProjectManifest{
		Project: manifest.ProjectInfo{Name: "p"},
		Targets: map[string]manifest.BuildTarget{
			"standard": {Dependencies: []manifest.DependencySource{{Project: "own", Target: "standard"}}},
			"images":   {},
		},
	}
}

func TestAddBootstrapDependency(t *testing.T) {
	tests := []struct {
		name    string
		dep     string
		targets []string
		wantErr bool
	}{
		{"appended after own paths", "X/standard", []string{"standard"}, false},
		{"several targets", "X/standard", []string{"standard", "images"}, false},
		{"no targets", "X/standard", nil, true},
		{"no name", "", []string{"standard"}, true},
		{"unknown target", "X/standard", []string{"nope"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prj := testProject()
			err := AddBootstrapDependency(prj, "http://b/", tc.dep, tc.targets)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if tc.wantErr {
				return
			}
			for _, tg := range tc.targets {
				deps := prj.Targets[tg].Dependencies
				last := deps[len(deps)-1]
				if last.URL != "http://b/" || last.Name != tc.dep || last.Type != "rpm-md" {
					t.Errorf("%s last dep = %+v", tg, last)
				}
			}
			if d := prj.Targets["standard"].Dependencies; d[0].Project != "own" {
				t.Errorf("own path entry moved: %+v", d)
			}
		})
	}
}

func TestAddBootstrapDependencyTwiceFails(t *testing.T) {
	prj := testProject()
	if err := AddBootstrapDependency(prj, "http://b/", "n", []string{"standard"}); err != nil {
		t.Fatal(err)
	}
	if err := AddBootstrapDependency(prj, "http://b/", "n", []string{"standard"}); err == nil {
		t.Fatal("duplicate name accepted")
	}
}

func TestRemoveDependency(t *testing.T) {
	prj := testProject()
	if err := AddBootstrapDependency(prj, "http://b/", "n", []string{"standard"}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, target, dep string
		want, wantErr     bool
	}{
		{"unknown target", "nope", "n", false, true},
		{"unknown name", "standard", "other", false, false},
		{"removes", "standard", "n", true, false},
		{"already gone", "standard", "n", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RemoveDependency(prj, tc.target, tc.dep)
			if got != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
	if d := prj.Targets["standard"].Dependencies; len(d) != 1 || d[0].Project != "own" {
		t.Errorf("deps = %+v", d)
	}
}
