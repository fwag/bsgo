package buildconfig_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/buildconfig"
	"github.com/openSUSE/bsgo/pkg/manifest"
)

func prj(name string, cfg *manifest.ProjectConfig) *manifest.ProjectManifest {
	return &manifest.ProjectManifest{Project: manifest.ProjectInfo{Name: name}, PublicConfig: cfg}
}

func loader(ps ...*manifest.ProjectManifest) buildconfig.Loader {
	return func(name string) (*manifest.ProjectManifest, error) {
		for _, p := range ps {
			if p.Project.Name == name {
				return p, nil
			}
		}
		return nil, nil
	}
}

func TestLayerOrderAndHeaders(t *testing.T) {
	base := prj("base", &manifest.ProjectConfig{Prefer: []string{"from-base"}})
	top := prj("base:top", &manifest.ProjectConfig{Prefer: []string{"from-top"}})
	path := []buildconfig.PathElem{{Project: "base:top", Repo: "standard"}, {Project: "base", Repo: "standard"}}
	sched, worker, err := buildconfig.Layer("base:top", "standard", path, loader(base, top))
	if err != nil {
		t.Fatal(err)
	}
	if sched != worker {
		t.Errorf("without Macros: both texts must be equal")
	}
	iBase, iTop := strings.Index(sched, "### from base\n"), strings.Index(sched, "### from base:top\n")
	if iBase < 0 || iTop < 0 || iBase > iTop {
		t.Fatalf("furthest dependency must come first:\n%s", sched)
	}
	c, err := buildconfig.Parse(sched, "x86_64")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.Prefer, " ") != "from-base from-top" {
		t.Errorf("prefer = %v, want base entries first without duplicates", c.Prefer)
	}
}

func TestLayerKeepsReadConfigListSemantics(t *testing.T) {
	// Layering is textual: list directives append as in Build::read_config,
	// so an entry repeated by two path elements appears twice. Consumers
	// treat Prefer as a set.
	both := &manifest.ProjectConfig{Prefer: []string{"same"}}
	path := []buildconfig.PathElem{{Project: "b", Repo: "r"}, {Project: "a", Repo: "r"}}
	sched, _, err := buildconfig.Layer("b", "r", path, loader(prj("a", both), prj("b", both)))
	if err != nil {
		t.Fatal(err)
	}
	c, err := buildconfig.Parse(sched, "x86_64")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.Prefer, " ") != "same same" {
		t.Errorf("prefer = %v, want read_config append semantics", c.Prefer)
	}
}

func TestLayerSkipsUnknownAndConfiglessElements(t *testing.T) {
	known := prj("known", &manifest.ProjectConfig{Prefer: []string{"k"}})
	noConfig := &manifest.ProjectManifest{Project: manifest.ProjectInfo{Name: "plain"}}
	path := []buildconfig.PathElem{
		{Project: "known", Repo: "r"},
		{Project: "dod", Repo: "r", URL: "https://example.invalid/"},
		{Project: "plain", Repo: "r"},
	}
	sched, _, err := buildconfig.Layer("known", "r", path, loader(known, noConfig))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(sched, "### from") != 1 || !strings.Contains(sched, "### from known\n") {
		t.Errorf("only the project with a config must contribute:\n%s", sched)
	}
}

func TestLayerFromScratchDiscardsEarlierElements(t *testing.T) {
	dep := prj("dep", &manifest.ProjectConfig{Prefer: []string{"dropped"}})
	own := prj("own", &manifest.ProjectConfig{FromScratch: true, Prefer: []string{"kept"}})
	path := []buildconfig.PathElem{{Project: "own", Repo: "r"}, {Project: "dep", Repo: "r"}}
	sched, _, err := buildconfig.Layer("own", "r", path, loader(dep, own))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sched, "dropped") || strings.Contains(sched, "### from dep") {
		t.Errorf("FromScratch must discard the dependency:\n%s", sched)
	}
	c, err := buildconfig.Parse(sched, "x86_64")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.Prefer, " ") != "kept" {
		t.Errorf("prefer = %v", c.Prefer)
	}
}

func TestLayerSchedulerStripsMacros(t *testing.T) {
	p := prj("p", &manifest.ProjectConfig{Prefer: []string{"x"}, Macros: "%vendor SUSE\n%other 1\n"})
	sched, worker, err := buildconfig.Layer("p", "r", nil, loader(p))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sched, "vendor") || strings.Contains(strings.ToLower(sched), "macros:") {
		t.Errorf("scheduler text keeps Macros:\n%s", sched)
	}
	if !strings.Contains(worker, "Macros:\n%vendor SUSE\n%other 1\n:Macros\n") {
		t.Errorf("worker text lost Macros:\n%s", worker)
	}
	if strings.Count(sched, "\n") != strings.Count(worker, "\n") {
		t.Errorf("stripping must keep the line count (scheduler %d, worker %d)",
			strings.Count(sched, "\n"), strings.Count(worker, "\n"))
	}
}

func TestLayerLoaderError(t *testing.T) {
	boom := errors.New("boom")
	_, _, err := buildconfig.Layer("p", "r", nil, func(string) (*manifest.ProjectManifest, error) { return nil, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestStripMacros(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"none", "a\nb\n", "a\nb\n"},
		{"block keeps line count", "a\nMacros:\nx\ny\n:Macros\nb\n", "a\n\n\n\n\nb\n"},
		{"case and blanks", "  mAcRoS:  \n%x 1\n  :MACROS \nz\n", "\n\n\nz\n"},
		{"unterminated block runs to the end", "a\nMacros:\nx\ny\n", "a\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildconfig.StripMacros(tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPathFromTarget(t *testing.T) {
	p := &manifest.ProjectManifest{
		Project: manifest.ProjectInfo{Name: "SUSE:SLFO:1.2"},
		Targets: map[string]manifest.BuildTarget{
			"standard": {Dependencies: []manifest.DependencySource{
				{Project: "SUSE:SLFO:Base", Target: "standard"},
				{URL: "https://example.invalid/", Name: "SUSE:SLFO:1.1/standard"},
			}},
			"bad-url":  {Dependencies: []manifest.DependencySource{{URL: "https://example.invalid/"}}},
			"bad-dep":  {Dependencies: []manifest.DependencySource{{Project: "x"}}},
			"no-deps":  {},
			"url-name": {Dependencies: []manifest.DependencySource{{URL: "u", Name: "noslash"}}},
		},
	}
	got, err := buildconfig.PathFromTarget(p, "standard")
	if err != nil {
		t.Fatal(err)
	}
	want := []buildconfig.PathElem{
		{Project: "SUSE:SLFO:1.2", Repo: "standard"},
		{Project: "SUSE:SLFO:Base", Repo: "standard"},
		{Project: "SUSE:SLFO:1.1", Repo: "standard", URL: "https://example.invalid/"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("elem %d = %v, want %v", i, got[i], want[i])
		}
	}
	if only, err := buildconfig.PathFromTarget(p, "no-deps"); err != nil || len(only) != 1 {
		t.Errorf("no-deps: %v %v", only, err)
	}
	for _, bad := range []string{"bad-url", "bad-dep", "url-name"} {
		if _, err := buildconfig.PathFromTarget(p, bad); err == nil {
			t.Errorf("%s: expected an error", bad)
		}
	}
}

func TestItems(t *testing.T) {
	text := "# c\n%define a 1\n%define b x \\\n  y\nMacros:\n%m 1\n:Macros\n%if 0\nPrefer: z\n%endif\n"
	items := buildconfig.Items(text)
	if len(items) != 7 {
		t.Fatalf("got %d items: %+v", len(items), items)
	}
	if items[2].Raw != "%define b x \\\n  y" || items[2].Line != 3 {
		t.Errorf("continuation not joined: %+v", items[2])
	}
	if !items[3].Macros || items[3].Body != "%m 1\n" || items[3].Line != 5 {
		t.Errorf("macros item: %+v", items[3])
	}
	if items[4].Line != 8 {
		t.Errorf("line after macros block = %d, want 8", items[4].Line)
	}
}
