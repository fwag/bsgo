package obsimport_test

import (
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/obsimport"
)

const testMeta = `<project name="test:prj">
  <title>T</title>
  <description>D</description>
  <person userid="alice" role="maintainer"/>
  <person userid="bob" role="bugowner"/>
  <build><disable repository="images"/></build>
  <publish><disable/><enable repository="standard"/></publish>
  <repository name="standard">
    <path project="base" repository="standard"/>
    <arch>x86_64</arch><arch>aarch64</arch>
  </repository>
  <repository name="images">
    <arch>x86_64</arch>
  </repository>
</project>`

func convert(t *testing.T, prjconf string) *obsimport.Result {
	t.Helper()
	meta, err := obsimport.ParseMeta([]byte(testMeta))
	if err != nil {
		t.Fatal(err)
	}
	res, err := obsimport.Convert(meta, prjconf)
	if err != nil {
		t.Fatal(err)
	}
	mismatches, err := obsimport.Verify(res.Manifest, prjconf, []string{"x86_64", "aarch64", "i586"})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mismatches {
		t.Errorf("round trip: %+v", m)
	}
	return res
}

func rawReasons(r obsimport.Report) string {
	var parts []string
	for _, e := range r.Raw {
		parts = append(parts, e.Reason)
	}
	return strings.Join(parts, "|")
}

func TestConvertMapsScopes(t *testing.T) {
	res := convert(t, strings.Join([]string{
		"# comment",
		"%define lua_pref lua54",
		"Prefer: %{lua_pref} -yast2-schema",
		"Release: <CI_CNT>.<B_CNT>",
		"%if 0%{?_is_in_project}",
		"BuildFlags: excludebuild:foo",
		"%endif",
		"%if 0%{?_is_this_project}",
		"Support: only-self",
		"%endif",
		"%ifarch x86_64 aarch64",
		"Optflags: x86_64 -O3",
		"%endif",
		`%if "%_repository" == "images"`,
		"BuildEngine: podman",
		"  %ifarch x86_64",
		"  Ignore: img:x86",
		"  %endif",
		"%endif",
		"Macros:",
		"%vendor SUSE",
		":Macros",
		"",
	}, "\n"))
	m := res.Manifest
	if got := m.PublicConfig.Defines; len(got) != 1 || got[0].Name != "lua_pref" {
		t.Errorf("defines = %v", got)
	}
	if strings.Join(m.PublicConfig.Prefer, " ") != "%{lua_pref} -yast2-schema" || m.PublicConfig.Release != "<CI_CNT>.<B_CNT>" {
		t.Errorf("public = %+v", m.PublicConfig)
	}
	if m.ProtectedConfig == nil || m.ProtectedConfig.BuildFlags[0] != "excludebuild:foo" {
		t.Errorf("protected = %+v", m.ProtectedConfig)
	}
	if m.PrivateConfig == nil || m.PrivateConfig.Support[0] != "only-self" {
		t.Errorf("private = %+v", m.PrivateConfig)
	}
	for _, arch := range []string{"x86_64", "aarch64"} {
		if m.ArchConfig[arch] == nil || m.ArchConfig[arch].Optflags["x86_64"] != "-O3" {
			t.Errorf("arch_config.%s = %+v", arch, m.ArchConfig[arch])
		}
	}
	img := m.Targets["images"]
	if img.Config == nil || img.Config.BuildEngine != "podman" || img.ArchConfig["x86_64"].Ignore[0] != "img:x86" {
		t.Errorf("images target = %+v", img)
	}
	if m.PublicConfig.Macros != "%vendor SUSE\n" {
		t.Errorf("macros = %q", m.PublicConfig.Macros)
	}
	if res.Report.RawFragments != 0 || res.Report.DroppedComments != 1 {
		t.Errorf("report = %+v", res.Report)
	}
}

func TestConvertKeepsUnmappableConstructsVerbatim(t *testing.T) {
	tests := []struct {
		name       string
		prjconf    string
		wantReason string
		wantRaw    string
	}{
		{"other conditional", "%if 0%{?is_stage_project}\nRelease: x\n%endif\n", "conditional has no structured scope", "%if 0%{?is_stage_project}"},
		{"else branch", "%ifarch x86_64\nPrefer: a\n%else\nPrefer: b\n%endif\n", "conditional with %else or %elif branch", "Prefer: b"},
		{"ifnarch", "%ifnarch x86_64\nPrefer: a\n%endif\n", "conditional has no structured scope", "%ifnarch x86_64"},
		{"unknown directive", "Keep: foo\n", "directive keep has no structured field", "Keep: foo"},
		{"unknown repository", "%if \"%_repository\" == \"nope\"\nPrefer: a\n%endif\n", "repository nope is not in _meta", "nope"},
		{"multi-line define", "%define big a \\\n b\n", "line is neither a %define nor a known directive", "%define big a \\"},
		{"macro in keyed directive", "Substitute: %{x} y\n", keyedReason, "Substitute: %{x} y"},
		{"stray endif", "Prefer: a\n%endif\n", "conditional keyword without matching %if", "%endif"},
		{"macros reset", "Macros:\n!\n%a 1\n:Macros\n", "Macros: block starting with '!' resets earlier macros", "%a 1"},
		{"nested scope not representable", "%if 0%{?_is_in_project}\n%ifarch x86_64\nPrefer: a\n%endif\n%endif\n", "conditional nesting has no structured scope", "%ifarch x86_64"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := convert(t, tc.prjconf)
			if !strings.Contains(rawReasons(res.Report), tc.wantReason) {
				t.Errorf("reasons %q lack %q", rawReasons(res.Report), tc.wantReason)
			}
			var raw string
			for _, c := range []*manifest.ProjectConfig{res.Manifest.PublicConfig, res.Manifest.ProtectedConfig} {
				if c != nil {
					raw += c.RawConfig
				}
			}
			if !strings.Contains(raw, tc.wantRaw) {
				t.Errorf("raw_config %q lacks %q", raw, tc.wantRaw)
			}
		})
	}
}

const keyedReason = "keyed directive uses macros, a removal form or too few arguments"

func TestConvertMovesStatementsOnlyWhenOrderRequiresIt(t *testing.T) {
	tests := []struct {
		name      string
		prjconf   string
		wantMoves int
	}{
		{"set-like lists commute", "%ifarch x86_64\nPrefer: a\n%endif\nPrefer: b\n", 0},
		{"different flags commute", "%ifarch x86_64\nBuildFlags: signflavor:1\n%endif\nBuildFlags: vmfstype:ext4\n", 0},
		{"same scalar in arch then public", "%ifarch x86_64\nRelease: arch\n%endif\nRelease: pub\n", 2},
		{"same keyed value in arch then public", "%ifarch x86_64\nSubstitute: k a\n%endif\nSubstitute: k b\n", 2},
		{"define after use", "%ifarch x86_64\nPrefer: %{later}\n%endif\n%define later x\n", 2},
		{"macros blocks keep their order", "%ifarch x86_64\nMacros:\n%a 1\n:Macros\n%endif\nMacros:\n%b 2\n:Macros\n", 2},
		{"independent macros after define", "%define a 1\nPrefer: x\n", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := convert(t, tc.prjconf)
			if res.Report.OrderMoves != tc.wantMoves {
				t.Errorf("order moves = %d, want %d (%s)", res.Report.OrderMoves, tc.wantMoves, rawReasons(res.Report))
			}
		})
	}
}

func TestConvertSecondSingleValuedStatementStaysVerbatim(t *testing.T) {
	res := convert(t, "Release: one\nRelease: two\nRepotype: a\nRepotype: b\n")
	if res.Manifest.PublicConfig.Release != "one" || !strings.Contains(res.Manifest.PublicConfig.RawConfig, "Release: two") {
		t.Errorf("public = %+v", res.Manifest.PublicConfig)
	}
	if res.Report.RawByReason["second release for the same scope and key"] != 1 {
		t.Errorf("reasons = %v", res.Report.RawByReason)
	}
}

func TestConvertFromScratchOnlyAsFirstStatement(t *testing.T) {
	first := convert(t, "# header\nFromScratch:\nPrefer: a\n")
	if !first.Manifest.PublicConfig.FromScratch {
		t.Errorf("FromScratch not mapped")
	}
	late := convert(t, "Prefer: a\nFromScratch:\n")
	if late.Manifest.PublicConfig.FromScratch || !strings.Contains(late.Manifest.PublicConfig.RawConfig, "FromScratch:") {
		t.Errorf("late FromScratch must stay verbatim: %+v", late.Manifest.PublicConfig)
	}
}

func TestConvertErrors(t *testing.T) {
	meta, err := obsimport.ParseMeta([]byte(testMeta))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := obsimport.Convert(meta, "%if 1\nPrefer: a\n"); err == nil {
		t.Error("unterminated %if must fail")
	}
}

func TestParseMeta(t *testing.T) {
	meta, err := obsimport.ParseMeta([]byte(testMeta))
	if err != nil {
		t.Fatal(err)
	}
	res, err := obsimport.Convert(meta, "")
	if err != nil {
		t.Fatal(err)
	}
	m := res.Manifest
	if m.Project.Name != "test:prj" || len(m.Project.Maintainers) != 1 || m.Project.Maintainers[0] != "alice" {
		t.Errorf("project = %+v", m.Project)
	}
	std, img := m.Targets["standard"], m.Targets["images"]
	if len(std.Architectures) != 2 || len(std.Dependencies) != 1 || std.Dependencies[0] != (manifest.DependencySource{Project: "base", Target: "standard"}) {
		t.Errorf("standard = %+v", std)
	}
	if !std.Flags.Build || !std.Flags.Publish || img.Flags.Build || img.Flags.Publish {
		t.Errorf("flags: standard %+v images %+v", std.Flags, img.Flags)
	}
	for _, bad := range []string{"", "<project/>", "<project name='x'"} {
		if _, err := obsimport.ParseMeta([]byte(bad)); err == nil {
			t.Errorf("ParseMeta(%q) must fail", bad)
		}
	}
}

func TestConversionIsDeterministic(t *testing.T) {
	conf := "%define a 1\nPrefer: x y\n%ifarch x86_64 aarch64\nPrefer: z\n%endif\n"
	first := convert(t, conf)
	for i := 0; i < 10; i++ {
		again := convert(t, conf)
		if len(again.Report.Raw) != len(first.Report.Raw) || again.Report.StructuredStatements != first.Report.StructuredStatements {
			t.Fatal("report differs between runs")
		}
	}
}
