package buildconfig_test

import (
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/buildconfig"
	"github.com/openSUSE/bsgo/pkg/manifest"
)

func scopedProject() *manifest.ProjectManifest {
	return &manifest.ProjectManifest{
		SchemaVersion: "v1",
		Project:       manifest.ProjectInfo{Name: "SUSE:SLFO:1.2"},
		PublicConfig: &manifest.ProjectConfig{
			Defines: manifest.Defines{{Name: "lua_pref", Value: "lua54"}, {Name: "a", Value: "1"}},
			Release: "<CI_CNT>.<B_CNT>",
			Prefer:  []string{"%{lua_pref}", "-yast2-schema"},
			Macros:  "%vendor SUSE\n",
			RawConfig: "%if 0%{?is_stage_project}\n" +
				"Support: staged\n%endif\n",
		},
		ProtectedConfig: &manifest.ProjectConfig{BuildFlags: []string{"excludebuild:lttng-modules"}},
		PrivateConfig:   &manifest.ProjectConfig{Support: []string{"private-pkg"}},
		ArchConfig: map[string]*manifest.ProjectConfig{
			"x86_64":  {Prefer: []string{"x86-only"}},
			"aarch64": {Prefer: []string{"arm-only"}},
		},
		Targets: map[string]manifest.BuildTarget{
			"images": {
				Config:     &manifest.ProjectConfig{BuildEngine: "podman", RepoTypes: []string{"none"}},
				ArchConfig: map[string]*manifest.ProjectConfig{"x86_64": {Ignore: []string{"x86-image-only"}}},
			},
			"standard": {},
		},
	}
}

func TestRenderScopeSelection(t *testing.T) {
	prj := scopedProject()
	tests := []struct {
		name     string
		repo     string
		arch     string
		consumer string
		want     []string
		absent   []string
	}{
		{
			name: "self sees public protected and private", repo: "standard", consumer: "SUSE:SLFO:1.2",
			want:   []string{"Prefer: %{lua_pref}", "BuildFlags: excludebuild:lttng-modules", "Support: private-pkg"},
			absent: []string{"BuildEngine"},
		},
		{
			name: "subproject sees public and protected", repo: "standard", consumer: "SUSE:SLFO:1.2:PullRequest",
			want:   []string{"Prefer: %{lua_pref}", "BuildFlags: excludebuild:lttng-modules"},
			absent: []string{"private-pkg"},
		},
		{
			name: "sibling prefix is not a subproject", repo: "standard", consumer: "SUSE:SLFO:1.20",
			want:   []string{"Prefer: %{lua_pref}"},
			absent: []string{"lttng-modules", "private-pkg"},
		},
		{
			name: "foreign sees public only", repo: "standard", consumer: "home:x",
			want:   []string{"Prefer: %{lua_pref}"},
			absent: []string{"lttng-modules", "private-pkg"},
		},
		{
			name: "target overlay applies to its repository only", repo: "images", consumer: "home:x",
			want: []string{"BuildEngine: podman", "Repotype: none"},
		},
		{
			name: "standard has no target overlay", repo: "standard", consumer: "home:x",
			absent: []string{"BuildEngine", "x86-image-only"},
		},
		{
			name: "specific arch emits unguarded overlay", repo: "images", arch: "x86_64", consumer: "home:x",
			want:   []string{"Prefer: x86-only", "Ignore: x86-image-only"},
			absent: []string{"arm-only", "%ifarch"},
		},
		{
			name: "no arch emits guarded overlays in sorted order", repo: "standard", consumer: "home:x",
			want: []string{"%ifarch aarch64\nPrefer: arm-only\n%endif\n%ifarch x86_64\nPrefer: x86-only\n%endif\n"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildconfig.Render(prj, tc.repo, tc.arch, tc.consumer)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in\n%s", w, got)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(got, a) {
					t.Errorf("unexpected %q in\n%s", a, got)
				}
			}
		})
	}
}

func TestRenderScopeOrderAndDeterminism(t *testing.T) {
	prj := scopedProject()
	first, err := buildconfig.Render(prj, "standard", "", "SUSE:SLFO:1.2")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		again, err := buildconfig.Render(prj, "standard", "", "SUSE:SLFO:1.2")
		if err != nil {
			t.Fatal(err)
		}
		if again != first {
			t.Fatalf("render is not deterministic")
		}
	}
	order := []string{"%define lua_pref lua54", "%define a 1", "Release:", "Prefer: %{lua_pref}", "%if 0%{?is_stage_project}", "Macros:\n%vendor SUSE\n:Macros", "BuildFlags:", "Support: private-pkg"}
	pos := -1
	for _, o := range order {
		i := strings.Index(first, o)
		if i < 0 || i < pos {
			t.Fatalf("%q out of order (at %d, previous %d) in\n%s", o, i, pos, first)
		}
		pos = i
	}
}

func TestRenderRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		cfg  manifest.ProjectConfig
	}{
		{"newline in list entry", manifest.ProjectConfig{Prefer: []string{"a\nIgnore: b"}}},
		{"empty list entry", manifest.ProjectConfig{Prefer: []string{" "}}},
		{"bad define name", manifest.ProjectConfig{Defines: manifest.Defines{{Name: "a b", Value: "1"}}}},
		{"define value with newline", manifest.ProjectConfig{Defines: manifest.Defines{{Name: "a", Value: "1\n2"}}}},
		{"deprecated macros_file", manifest.ProjectConfig{MacrosFile: "x"}},
		{"macros delimiter inside body", manifest.ProjectConfig{Macros: "a\n:Macros\nb"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prj := &manifest.ProjectManifest{Project: manifest.ProjectInfo{Name: "p"}, PublicConfig: &tc.cfg}
			if _, err := buildconfig.Render(prj, "r", "", "p"); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	if _, err := buildconfig.Render(nil, "r", "", "p"); err == nil {
		t.Fatal("expected an error for nil manifest")
	}
}

func TestRenderFromScratchComesFirst(t *testing.T) {
	prj := &manifest.ProjectManifest{
		Project:      manifest.ProjectInfo{Name: "p"},
		PublicConfig: &manifest.ProjectConfig{FromScratch: true, Defines: manifest.Defines{{Name: "a", Value: "1"}}, Prefer: []string{"x"}},
	}
	got, err := buildconfig.Render(prj, "r", "", "p")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "FromScratch:\n%define a 1\n") {
		t.Fatalf("FromScratch must precede everything:\n%s", got)
	}
	if _, err := buildconfig.Parse(got, "x86_64"); err != nil {
		t.Fatalf("rendered FromScratch config does not parse: %v", err)
	}
}

func TestRenderRoundTripsDirectivesThroughParse(t *testing.T) {
	prj := &manifest.ProjectManifest{
		Project: manifest.ProjectInfo{Name: "p"},
		PublicConfig: &manifest.ProjectConfig{
			Preinstall:   []string{"bash", "rpm"},
			VMinstall:    []string{"kernel"},
			Support:      []string{"s1", "!s1", "s2"},
			Order:        []string{"o1"},
			BuildEngine:  "podman",
			BuildTypes:   []string{"docker"},
			RepoTypes:    []string{"none"},
			Substitute:   map[string][]string{"jdk": {"java-17"}},
			Conflict:     [][]string{{"a", "b"}},
			FileProvides: map[string][]string{"/bin/sh": {"bash"}},
			Optflags:     map[string]string{"x86_64": "-O2 -g"},
			BuildFlags:   []string{"allowrootforbuild"},
			PublishFlags: []string{"archsync"},
			ExpandFlags:  []string{"keepfilerequires"},
			ExportFilter: []string{`\.rpm$ x86_64`},
			Constraints:  []string{"hardware:cpu:flag sse"},
		},
	}
	text, err := buildconfig.Render(prj, "r", "x86_64", "p")
	if err != nil {
		t.Fatal(err)
	}
	c, err := buildconfig.Parse(text, "x86_64")
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	checks := []struct {
		name string
		ok   bool
	}{
		{"preinstall", strings.Join(c.Preinstall, " ") == "bash rpm"},
		{"support removal", strings.Join(c.Support, " ") == "s2"},
		{"engine", c.BuildEngine == "podman"},
		{"type", c.Type == "docker"},
		{"repotype", strings.Join(c.Repotype, " ") == "none"},
		{"substitute", strings.Join(c.Substitute["jdk"], " ") == "java-17"},
		{"fileprovides", strings.Join(c.FileProvides["/bin/sh"], " ") == "bash"},
		{"optflags", c.Optflags["x86_64"] == "-O2 -g"},
		{"buildflags", c.BuildFlagsH["allowrootforbuild"] == "1"},
		{"order", c.Order["o1"]},
		{"exportfilter", strings.Join(c.ExportFilter[`\.rpm$`], " ") == "x86_64"},
		{"constraint", len(c.Constraint) == 1},
	}
	for _, ch := range checks {
		if !ch.ok {
			t.Errorf("%s not parsed back: %+v", ch.name, c)
		}
	}
}
