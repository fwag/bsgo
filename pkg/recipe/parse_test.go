package recipe_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/buildconfig"
	"github.com/openSUSE/bsgo/pkg/recipe"
)

func testConfig(t *testing.T, text string) *buildconfig.Config {
	t.Helper()
	cfg, err := buildconfig.Parse(text, "x86_64")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestParseSpec(t *testing.T) {
	tests := []struct {
		name string
		cfg  string
		spec string
		opt  recipe.Options
		want recipe.Info
	}{
		{
			name: "identity and deps",
			spec: "Name: foo\nVersion: 1.2\nRelease: 3\nEpoch: 4\nBuildRequires: a b >= 1, c =< 2\nBuildPrereq: p\n",
			want: recipe.Info{Name: "foo", Version: "1.2", Release: "3", Epoch: "4",
				Deps: []string{"a", "b >= 1", "c <= 2", "p"}, Subpacks: []string{"foo"}},
		},
		{
			name: "conflicts and ignore",
			spec: "Name: foo\nBuildConflicts: x\n#!BuildIgnore: y\n#!BuildRequires: z\n",
			want: recipe.Info{Name: "foo", Deps: []string{"-x", "-y", "z"}, Subpacks: []string{"foo"}},
		},
		{
			name: "conflictdeps option",
			spec: "Name: foo\nBuildConflicts: x\n#!BuildIgnore: y\n",
			opt:  recipe.Options{ConflictDeps: true},
			want: recipe.Info{Name: "foo", Deps: []string{"!x", "-y"}, Subpacks: []string{"foo"}},
		},
		{
			name: "rich dependency",
			spec: "Name: foo\nBuildRequires: (a if b) c\n",
			want: recipe.Info{Name: "foo", Deps: []string{"(a if b)", "c"}, Subpacks: []string{"foo"}},
		},
		{
			// Quirk kept from obs-build: the qualifier test is inverted, so
			// a dependency whose qualifier lists the target arch is dropped.
			name: "arch qualifier quirk",
			spec: "Name: foo\nBuildRequires: a [x86_64]\nBuildRequires: b [!x86_64]\nBuildRequires: c [aarch64]\nBuildRequires: d [!aarch64]\nBuildRequires: e >= 1 [aarch64,x86_64]\n",
			want: recipe.Info{Name: "foo", Deps: []string{"b", "c"}, Subpacks: []string{"foo"}},
		},
		{
			name: "subpackages and arch lists",
			spec: "Name: foo\nExclusiveArch: x86_64 aarch64\nExcludeArch: s390x\n%package devel\nSummary: x\n%package -n bar\nSummary: y\n",
			want: recipe.Info{Name: "foo", Deps: []string{}, Subpacks: []string{"foo", "foo-devel", "bar"},
				ExclArch: []string{"x86_64", "aarch64"}, BadArch: []string{"s390x"}},
		},
		{
			name: "defines and conditionals",
			spec: "%define pn bar\n%bcond_without x\nName: %{pn}\n%if %{with x}\nBuildRequires: with-x\n%else\nBuildRequires: no-x\n%endif\n%ifarch aarch64\nBuildRequires: arm\n%endif\n",
			want: recipe.Info{Name: "bar", Deps: []string{"with-x"}, Subpacks: []string{"bar"}},
		},
		{
			name: "config macros",
			cfg:  "Macros:\n%vendor_dep vdep\n:Macros\n",
			spec: "Name: foo\nBuildRequires: %{vendor_dep}\n",
			want: recipe.Info{Name: "foo", Deps: []string{"vdep"}, Subpacks: []string{"foo"}},
		},
		{
			name: "build flavor",
			spec: "Name: foo@BUILD_FLAVOR@\n%if \"@BUILD_FLAVOR@\" == \"mini\"\nBuildRequires: small\n%else\nBuildRequires: big\n%endif\nBuildRequires: @OBS_PACKAGE@-tools\n",
			opt:  recipe.Options{Package: "pk", Flavor: "mini"},
			want: recipe.Info{Name: "foomini", Deps: []string{"small", "pk-tools"}, Subpacks: []string{"foomini"}},
		},
		{
			name: "build target and constraint",
			spec: "Name: foo\n#!BuildTarget: x86_64:tgt\n#!BuildConstraint: hardware:memory 8\n#!BuildConstraint: a b\n",
			want: recipe.Info{Name: "foo", Deps: []string{}, Subpacks: []string{"foo"}, BuildTarget: "tgt",
				Constraints: []string{"hardware:memory 8", "a b"}},
		},
		{
			name: "build target for other arch",
			spec: "Name: foo\n#!BuildTarget: i586:tgt\n",
			want: recipe.Info{Name: "foo", Deps: []string{}, Subpacks: []string{"foo"}},
		},
		{
			name: "missing name",
			spec: "Version: 1\n",
			want: recipe.Info{Version: "1", Deps: []string{}, Subpacks: []string{}, Error: "can not parse name from foo.spec"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := recipe.Parse(testConfig(t, tt.cfg), "foo.spec", []byte(tt.spec), tt.opt)
			if err != nil {
				t.Fatal(err)
			}
			got.Warnings = nil
			if !reflect.DeepEqual(*got, tt.want) {
				t.Errorf("got  %+v\nwant %+v", *got, tt.want)
			}
		})
	}
}

func TestParseSpecShellExpansionWarns(t *testing.T) {
	got, err := recipe.Parse(testConfig(t, ""), "foo.spec", []byte("Name: foo\nBuildRequires: %(echo x)\n"), recipe.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "cannot expand %(...)") {
		t.Errorf("warnings = %v", got.Warnings)
	}
	if !reflect.DeepEqual(got.Deps, []string{"MACRO"}) {
		t.Errorf("deps = %v, want the Perl placeholder MACRO", got.Deps)
	}
}

func TestParseUnsupportedAndUnknown(t *testing.T) {
	cfg := testConfig(t, "")
	for _, f := range []string{"x.kiwi", "Dockerfile", "PKGBUILD"} {
		_, err := recipe.Parse(cfg, f, nil, recipe.Options{})
		var ue *recipe.UnsupportedError
		if !errors.Is(err, recipe.ErrUnsupported) || !errors.As(err, &ue) {
			t.Errorf("%s: err = %v, want unsupported", f, err)
		}
	}
	if _, err := recipe.Parse(cfg, "README", nil, recipe.Options{}); err == nil || errors.Is(err, recipe.ErrUnsupported) {
		t.Errorf("unknown type: err = %v", err)
	}
}
