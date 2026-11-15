package buildconfig

import (
	"reflect"
	"strings"
	"testing"
)

func mustParse(t *testing.T, text, arch string) *Config {
	t.Helper()
	cfg, err := Parse(text, arch)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return cfg
}

func TestListDirectives(t *testing.T) {
	tests := []struct {
		name string
		text string
		get  func(*Config) []string
		want []string
	}{
		{"append", "Prefer: a b\nprefer: c", func(c *Config) []string { return c.Prefer }, []string{"a", "b", "c"}},
		{"case insensitive key", "PREFER: a", func(c *Config) []string { return c.Prefer }, []string{"a"}},
		{"remove entry", "Prefer: a b c\nPrefer: !b", func(c *Config) []string { return c.Prefer }, []string{"a", "c"}},
		{"reset", "Prefer: a b\nPrefer: !*\nPrefer: z", func(c *Config) []string { return c.Prefer }, []string{"z"}},
		{"prefer keeps duplicates", "Prefer: a a", func(c *Config) []string { return c.Prefer }, []string{"a", "a"}},
		{"preinstall unified", "Preinstall: a b a", func(c *Config) []string { return c.Preinstall }, []string{"a", "b"}},
		{"required unified", "Required: x y x", func(c *Config) []string { return c.Required }, []string{"x", "y"}},
		{"vminstall", "VMinstall: k", func(c *Config) []string { return c.Vminstall }, []string{"k"}},
		{"runscripts", "Runscripts: aaa_base", func(c *Config) []string { return c.Runscripts }, []string{"aaa_base"}},
		{"support", "Support: s1 s2", func(c *Config) []string { return c.Support }, []string{"s1", "s2"}},
		{"keep", "Keep: k1", func(c *Config) []string { return c.Keep }, []string{"k1"}},
		{"ignore", "Ignore: i1", func(c *Config) []string { return c.Ignore }, []string{"i1"}},
		{"conflict", "Conflict: a:b", func(c *Config) []string { return c.Conflict }, []string{"a:b"}},
		{"expandflags", "ExpandFlags: x module:m", func(c *Config) []string { return c.ExpandFlags }, []string{"x", "module:m"}},
		{"buildflags", "BuildFlags: nodebug", func(c *Config) []string { return c.BuildFlags }, []string{"nodebug"}},
		{"publishflags", "PublishFlags: a:1", func(c *Config) []string { return c.PublishFlags }, []string{"a:1"}},
		{"repotype replaces", "Repotype: a b\nRepotype: c d d", func(c *Config) []string { return c.Repotype }, []string{"c", "d"}},
		{"patterntype replaces", "Patterntype: ymp", func(c *Config) []string { return c.Patterntype }, []string{"ymp"}},
		{"constraint joined", "Constraint: a b\nConstraint: c", func(c *Config) []string { return c.Constraint }, []string{"a b", "c"}},
		{"constraint reset", "Constraint: a\nConstraint: !*", func(c *Config) []string { return c.Constraint }, []string{}},
		{"publishfilter replaces", "PublishFilter: a b\nPublishFilter: c", func(c *Config) []string { return c.PublishFilter }, []string{"c"}},
		{"urls", "RepoURL: u1\nRegistryURL: u2\nAssetsURL: u3", func(c *Config) []string {
			return []string{c.RepoURL[0], c.RegistryURL[0], c.AssetsURL[0]}
		}, []string{"u1", "u2", "u3"}},
		{"native", "AlsoNative: a\nOnlyNative: b\nIgnoreRebuild: c c", func(c *Config) []string {
			return append(append(c.AlsoNative, c.OnlyNative...), c.IgnoreRebuild...)
		}, []string{"a", "b", "c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.get(mustParse(t, tt.text, "x86_64"))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestScalarDirectives(t *testing.T) {
	tests := []struct {
		name string
		text string
		get  func(*Config) string
		want string
	}{
		{"default release", "", func(c *Config) string { return c.Release }, "<CI_CNT>.<B_CNT>"},
		{"release", "Release: 1.<CI_CNT> x", func(c *Config) string { return c.Release }, "1.<CI_CNT>"},
		{"release list", "Release: 1 x", func(c *Config) string { return strings.Join(c.ReleaseList, ",") }, "1,x"},
		{"type", "Type: kiwi extra", func(c *Config) string { return c.Type }, "kiwi"},
		{"type guessed spec", "Preinstall: rpm", func(c *Config) string { return c.Type }, "spec"},
		{"type guessed dsc", "Preinstall: debianutils", func(c *Config) string { return c.Type }, "dsc"},
		{"type guessed arch", "Preinstall: pacman", func(c *Config) string { return c.Type }, "arch"},
		{"type guessed apk", "Preinstall: apk-tools", func(c *Config) string { return c.Type }, "apk"},
		{"type undefined", "", func(c *Config) string { return c.Type }, "UNDEFINED"},
		{"binarytype from spec", "Type: spec", func(c *Config) string { return c.BinaryType }, "rpm"},
		{"binarytype from dsc", "Type: dsc", func(c *Config) string { return c.BinaryType }, "deb"},
		{"binarytype kiwi preinstall", "Type: kiwi\nPreinstall: debianutils", func(c *Config) string { return c.BinaryType }, "deb"},
		{"binarytype explicit", "Type: spec\nBinaryType: deb", func(c *Config) string { return c.BinaryType }, "deb"},
		{"binarytype undefined", "Type: kiwi", func(c *Config) string { return c.BinaryType }, "UNDEFINED"},
		{"buildengine", "BuildEngine: mock", func(c *Config) string { return c.BuildEngine }, "mock"},
		{"hostarch joined", "HostArch: a b", func(c *Config) string { return c.HostArch }, "a b"},
		{"target", "Target: armv7hl-suse-linux", func(c *Config) string { return c.Target }, "armv7hl-suse-linux"},
		{"changetarget", "ChangeTarget: i586-x", func(c *Config) string { return c.Target }, "i586-x"},
		{"cicntstart", "CIcntstart: 7", func(c *Config) string { return c.CICntStart }, "7"},
		{"releaseprg", "ReleasePrg: p", func(c *Config) string { return c.ReleasePrg }, "p"},
		{"releasesuffix", "ReleaseSuffix: a b", func(c *Config) string { return c.ReleaseSuffix }, "a b"},
		{"singleexport", "SingleExport: y", func(c *Config) string { return c.SingleExport }, "y"},
		{"rawmacros", "Macros:\n%a b\n:Macros", func(c *Config) string { return c.RawMacros }, "%a b\n"},
		{"rawmacros reset", "Macros:\n%a b\n:Macros\nMacros:\n!\n%c d\n:Macros", func(c *Config) string { return c.RawMacros }, "%c d\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.get(mustParse(t, tt.text, "x86_64")); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMapDirectives(t *testing.T) {
	cfg := mustParse(t, strings.Join([]string{
		"Substitute: a b c",
		"Substitute: gone x",
		"Substitute: !gone",
		"Substitute: v p= q p p=",
		"FileProvides: /bin/sh bash",
		"FileProvides: /x y",
		"FileProvides: !/x",
		"ExportFilter: re$ aarch64 i586",
		"ExportFilter: bare",
		"Optflags: x86_64 -O2 -g",
		"Optflags: noflags",
		"Order: a b c !b",
		"Preinstall: x",
		"Ignore: p ign:d1,d2",
		"Conflict: c1 c2:c3 c4,c5",
		"Prefer: pa pb",
		"ExpandFlags: module:m1 module:m2 plain k:v",
		"BuildFlags: bf k:v",
	}, "\n"), "x86_64")
	checks := []struct {
		name      string
		got, want any
	}{
		{"substitute a", cfg.Substitute["a"], []string{"b", "c"}},
		{"substitute removed", cfg.Substitute["gone"], []string(nil)},
		{"substitute unified and stripped", cfg.Substitute["v"], []string{"p", "q", "p"}},
		{"fileprovides", cfg.FileProvides, map[string][]string{"/bin/sh": {"bash"}}},
		{"exportfilter", cfg.ExportFilter, map[string][]string{"re$": {"aarch64", "i586"}, "bare": {}}},
		{"optflags", cfg.Optflags, map[string]string{"x86_64": "-O2 -g", "noflags": ""}},
		{"order", cfg.Order, map[string]bool{"a": true, "c": true}},
		{"preferh", cfg.PreferH, map[string]bool{"pa": true, "pb": true}},
		{"ignoreh", cfg.IgnoreH, map[string]bool{"p": true, "ign:d1": true, "ign:d2": true}},
		{"conflicth", cfg.ConflictH, map[string][]string{
			":": {"c1"}, "c2": {"c3"}, "c3": {"c2"}, "c4": {"c5"}, "c5": {"c4"},
		}},
		{"expandflags_h", cfg.ExpandFlagsH, map[string]string{"module": "m2", "plain": "1", "k": "v"}},
		{"modules", cfg.Modules, []string{"m1", "m2"}},
		{"buildflags_h", cfg.BuildFlagsH, map[string]string{"bf": "1", "k": "v"}},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s: got %#v, want %#v", c.name, c.got, c.want)
		}
	}
	sv := cfg.SubstituteVers["v"]
	if len(sv) != 6 || sv[1] == nil || *sv[1] != "=" || sv[3] != nil {
		t.Errorf("substitute_vers: unexpected %v", sv)
	}
	if got := cfg.Substitute["system-packages:repo-creation"]; got != nil {
		t.Errorf("unexpected default substitute without rpm type: %v", got)
	}
}

func TestDefaultRepoCreationSubstitute(t *testing.T) {
	cfg := mustParse(t, "Type: spec", "x86_64")
	if got := cfg.Substitute["system-packages:repo-creation"]; !reflect.DeepEqual(got, []string{"createrepo"}) {
		t.Errorf("got %v", got)
	}
	cfg = mustParse(t, "Type: spec\nSubstitute: system-packages:repo-creation mine", "x86_64")
	if got := cfg.Substitute["system-packages:repo-creation"]; !reflect.DeepEqual(got, []string{"mine"}) {
		t.Errorf("got %v", got)
	}
}

func TestMacrosList(t *testing.T) {
	cfg := mustParse(t, "DistMacro: dist .x\nTarget: armv7hl-suse\nMacros:\n%v 1\nplain\n:Macros", "i586")
	tail := cfg.Macros[len(cfg.Macros)-6:]
	want := []string{
		"%define _target_cpu i586", "%define _target_os linux",
		"%define dist .x", "%define _target_cpu armv7hl", "%define v 1", "plain",
	}
	if !reflect.DeepEqual(tail, want) {
		t.Errorf("got %q, want %q", tail, want)
	}
}

func TestConditionalsAndMacros(t *testing.T) {
	tests := []struct {
		name string
		arch string
		text string
		want []string
	}{
		{"ifarch hit", "x86_64", "%ifarch x86_64 i586\nPrefer: a\n%endif", []string{"a"}},
		{"ifarch miss", "aarch64", "%ifarch x86_64\nPrefer: a\n%endif", nil},
		{"ifnarch", "aarch64", "%ifnarch x86_64\nPrefer: a\n%endif", []string{"a"}},
		{"ifarch else", "i586", "%ifarch x86_64\nPrefer: a\n%else\nPrefer: b\n%endif", []string{"b"}},
		{"elifarch", "i586", "%ifarch x86_64\nPrefer: a\n%elifarch i586\nPrefer: b\n%else\nPrefer: c\n%endif", []string{"b"}},
		{"ifos", "i586", "%ifos linux\nPrefer: a\n%endif", []string{"a"}},
		{"ifnos", "i586", "%ifnos linux\nPrefer: a\n%else\nPrefer: b\n%endif", []string{"b"}},
		{"elifos", "i586", "%ifnos linux\nPrefer: a\n%elifos linux\nPrefer: b\n%endif", []string{"b"}},
		{"nested skipped", "x86_64", "%if 0\n%if 1\nPrefer: a\n%else\nPrefer: b\n%endif\n%endif\nPrefer: c", []string{"c"}},
		{"if expr", "x86_64", "%if 1 + 1 == 2 && \"a\" < \"b\"\nPrefer: a\n%endif", []string{"a"}},
		{"if false", "x86_64", "%if 0\nPrefer: a\n%elif 1\nPrefer: b\n%endif", []string{"b"}},
		{"elif after taken", "x86_64", "%if 1\nPrefer: a\n%elif 1\nPrefer: b\n%endif", []string{"a"}},
		{"define", "x86_64", "%define v hello\nPrefer: %{v} %v", []string{"hello", "hello"}},
		{"global expands", "x86_64", "%define a 1\n%global b %{a}\n%define a 2\nPrefer: %{b}", []string{"1"}},
		{"define lazy", "x86_64", "%define a 1\n%define b %{a}\n%define a 2\nPrefer: %{b}", []string{"2"}},
		{"optional macro", "x86_64", "%define v 1\nPrefer: %{?v} %{?w} x%{?w:y}", []string{"1", "x"}},
		{"negated macro", "x86_64", "Prefer: %{!?w:fallback} %{!?_target_cpu:no}", []string{"fallback"}},
		{"zero prefix", "x86_64", "%if 0%{?nosuchmacro}\nPrefer: a\n%else\nPrefer: b\n%endif", []string{"b"}},
		{"expand", "x86_64", "%define a b\n%define b c\nPrefer: %{expand:%%{%{a}}}", []string{"c"}},
		{"parametric", "x86_64", "%define p(a:) [%1][%{-a*}][%*]\nPrefer: %{p -a X Y Z}", []string{"[Y][X][Y", "Z]"}},
		{"target cpu", "aarch64", "Prefer: %{_target_cpu} %_target_os", []string{"aarch64", "linux"}},
		{"std macro", "x86_64", "Prefer: %{arm64} %{ifnarch_missing}", []string{"aarch64", "%{ifnarch_missing}"}},
		{"percent escape", "x86_64", "Prefer: a%%b", []string{"a%b"}},
		{"string builtins", "x86_64", "Prefer: %{upper:a} %{lower:B} %{len:abc} %{sub abcdef 2 3} %{rep x 2}", []string{"A", "b", "3", "bc", "xx"}},
		{"gsub", "x86_64", "Prefer: %{gsub a%.b%.c %. -}", []string{"a-b-c"}},
		{"path builtins", "x86_64", "Prefer: %{basename:/a/b.c} %{dirname:/a/b.c} %{suffix:/a/b.c}", []string{"b.c", "/a", "c"}},
		{"expr builtin", "x86_64", "Prefer: %{expr:2*(3+1)}", []string{"8"}},
		{"bracket expr at end", "x86_64", "Prefer: x %[1 + 2]", []string{"x", "3"}},
		{"defined builtin", "x86_64", "%define a 1\nPrefer: %{defined:a} %{undefined:a} %{defined:b}", []string{"1", "0", "0"}},
		{"with without", "x86_64", "%bcond_with t\n%bcond_without d\nPrefer: %{with t} %{with d} %{without d}", []string{"0", "1", "0"}},
		{"multiline define", "x86_64", "%define m a \\\n b\nPrefer: %{m}", []string{"a"}},
		{"comment ignored", "x86_64", "# Prefer: a\nPrefer: b", []string{"b"}},
		{"dnl", "x86_64", "Prefer: a%dnl hidden\nPrefer: b", []string{"a", "b"}},
		{"build flavor", "x86_64", "Prefer: a@BUILD_FLAVOR@b @OBS_PACKAGE@", []string{"ab", "@OBS_PACKAGE@"}},
		{"line mark", "x86_64", "#!!line f:10\nPrefer: a", []string{"a"}},
		{"macros block verbatim", "x86_64", "Macros:\nPrefer: no\n%if 0\n:Macros\nPrefer: yes", []string{"yes"}},
		{"macros skipped in if", "x86_64", "%if 0\nMacros:\n%x 1\n:Macros\n%endif\nPrefer: p", []string{"p"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, _ := Parse(tt.text, tt.arch)
			if !reflect.DeepEqual(cfg.Prefer, nonNil(tt.want)) {
				t.Errorf("got %q, want %q", cfg.Prefer, tt.want)
			}
		})
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"fromscratch first is ok", "FromScratch:\nPreinstall: a", ""},
		{"fromscratch late", "Preinstall: a\nFromScratch:", "FromScratch directive must come first"},
		{"bad expression", "%if 1 +\n%endif", "line 1: syntax error in expression"},
		{"unmatched paren", "%if (1\n%endif", "line 1: unmatched ("},
		{"division by zero", "%if 1/0\n%endif", "line 1: division by zero"},
		{"non numeric", "%if \"a\" * 2\n%endif", "line 1: argument of * is not a number"},
		{"single ampersand", "%if 1 & 1\n%endif", "line 1: syntax error while parsing &&"},
		{"line mark names file", "#!!line f.conf:41\n%if (1\n%endif", "line f.conf:42: unmatched ("},
		{"unsupported builtin", "Prefer: %{load:x}", "line 1: unsupported builtin macro %load"},
		{"unknown keyword only warns", "Bogus: x", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse(tt.text, "x86_64")
			if cfg == nil {
				t.Fatal("config must not be nil")
			}
			if tt.want == "" {
				if err != nil {
					t.Fatalf("unexpected error %v", err)
				}
				return
			}
			if err == nil || err.Error() != tt.want || cfg.ParseError != tt.want {
				t.Errorf("got err=%v ParseError=%q, want %q", err, cfg.ParseError, tt.want)
			}
		})
	}
}

func TestUnknownKeywordWarning(t *testing.T) {
	cfg := mustParse(t, "Bogus: x", "x86_64")
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "bogus:") {
		t.Errorf("warnings: %v", cfg.Warnings)
	}
}

func evalOK(t *testing.T, expr string) string {
	t.Helper()
	v, ok := evalExpr(expr, nil, func(m string) { t.Errorf("warning for %q: %s", expr, m) })
	if !ok {
		t.Fatalf("evalExpr(%q) failed", expr)
	}
	return v
}

func TestExpressions(t *testing.T) {
	tests := []struct {
		expr string
		want string
	}{
		{"1", "1"},
		{"1 + 2 * 3", "7"},
		{"(1 + 2) * 3", "9"},
		{"7 / 2", "3.5"},
		{"10 - 2 - 3", "5"},
		{"-3 + 5", "2"},
		{"!0", "1"},
		{"!1", "0"},
		{"1 && 0", "0"},
		{"0 || 5", "5"},
		{"1 < 2", "1"},
		{"2 <= 1", "0"},
		{"2 >= 2", "1"},
		{"3 > 4", "0"},
		{"3 != 4", "1"},
		{"3 == 3", "1"},
		{`"a" == "a"`, "1"},
		{`"a" != "a"`, "0"},
		{`"b" > "a"`, "1"},
		{`"ab" + "cd"`, `"abcd`},
		{`v"1.2" < v"1.10"`, "1"},
		{`v"2" == v"2"`, "1"},
		{`v"1.0" >= v"1.0"`, "1"},
		{`v"1~rc1" < v"1"`, "1"},
		{`v"1" != v"2"`, "1"},
		{"1 ? 2 : 3", "2"},
		{"0 ? 2 : 3", "3"},
		{"bareword", `"bareword`},
		{`lua:string.upper("abc")`, `"ABC`},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			if got := evalOK(t, tt.expr); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExpressionMacroExpansion(t *testing.T) {
	e := newMacroEngine([]string{"%define v 5"}, func(m string) { t.Errorf("warning: %s", m) })
	xp := func(s string) string { return e.expand(s, 0) }
	v, ok := evalExpr("%{v} + 1", xp, func(m string) { t.Errorf("warning: %s", m) })
	if !ok || v != "6" {
		t.Errorf("got %q ok=%v", v, ok)
	}
	v, _ = evalExpr(`"%{v}x" == "5x"`, xp, func(m string) { t.Errorf("warning: %s", m) })
	if v != "1" {
		t.Errorf("string expansion: got %q", v)
	}
	v, _ = evalExpr("0 && %{v}", xp, func(m string) { t.Errorf("warning: %s", m) })
	if v != "0" {
		t.Errorf("short circuit: got %q", v)
	}
}

func TestVerscmp(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1", "1", 0},
		{"1.0", "1.1", -1},
		{"1.10", "1.9", 1},
		{"1.0a", "1.0", 1},
		{"1.0~rc1", "1.0", -1},
		{"1.0^git1", "1.0", 1},
		{"1.0^git1", "1.0.1", -1},
		{"1:1.0", "2.0", 1},
		{"1.0-1", "1.0-2", -1},
		{"1.0-10", "1.0-9", 1},
		{"1.0a", "1.0b", -1},
		{"1.a", "1.1", -1},
		{"1.01", "1.1", 0},
		{"", "1", -1},
	}
	for _, tt := range tests {
		t.Run(tt.a+"_"+tt.b, func(t *testing.T) {
			if got := verscmp(tt.a, tt.b); got != tt.want {
				t.Errorf("verscmp(%q,%q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
			if got := verscmp(tt.b, tt.a); got != -tt.want {
				t.Errorf("verscmp(%q,%q) = %d, want %d", tt.b, tt.a, got, -tt.want)
			}
		})
	}
}

// FuzzParse checks that arbitrary input never panics the parser.
func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"%if 1 +\n%endif", "Macros:\n%a\n:Macros", "%{expand:%{", "%[", "%define a(b",
		"%{sub:abc -5 9}", "%{gsub:a ( x}", "%ifarch\n%elif\n%else\n%endif", "Substitute: !", "%{?a:%{?b:",
		"%define a %{a}\nPrefer: %{a}", "%if v\"1\" < v\"", "%{p -a", "%{quote:a b} %{rep:x 99}",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(_ *testing.T, text string) {
		_, _ = Parse(text, "x86_64")
	})
}

func TestMacroTooDeep(t *testing.T) {
	cfg, err := Parse("%define a %{a}\nPrefer: %{a}", "x86_64")
	if err == nil || !strings.Contains(err.Error(), "macro too deeply nested") {
		t.Errorf("expected nesting error, got %v (prefer %v)", err, cfg.Prefer)
	}
}
