package solv_test

import (
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/solv"
)

// tpkg is a synthetic package. Dependency strings use OBS syntax:
// "name", "name op evr" or a rich dependency "(a or b)".
type tpkg struct {
	name, evr, arch                string
	prov, req, conf, obs, rec, fil []string
}

var depRe = regexp.MustCompile(`^(\S+)\s+(<=|>=|=|<|>)\s+(\S+)$`)

func depEntry(d string) string {
	pre := ""
	if rest, ok := strings.CutPrefix(d, "pre:"); ok {
		d, pre = rest, ` pre="1"`
	}
	m := depRe.FindStringSubmatch(d)
	if m == nil {
		return fmt.Sprintf(`<rpm:entry name="%s"%s/>`, html.EscapeString(d), pre)
	}
	flags := map[string]string{"<=": "LE", ">=": "GE", "=": "EQ", "<": "LT", ">": "GT"}[m[2]]
	epoch, rest := "0", m[3]
	if e, r, ok := strings.Cut(rest, ":"); ok {
		epoch, rest = e, r
	}
	ver, rel, _ := strings.Cut(rest, "-")
	attr := fmt.Sprintf(`name="%s" flags="%s" epoch="%s" ver="%s"`, m[1], flags, epoch, ver)
	if rel != "" {
		attr += fmt.Sprintf(` rel="%s"`, rel)
	}
	return "<rpm:entry " + attr + pre + "/>"
}

func depSection(tag string, deps []string) string {
	if len(deps) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<rpm:" + tag + ">")
	for _, d := range deps {
		sb.WriteString(depEntry(d))
	}
	sb.WriteString("</rpm:" + tag + ">")
	return sb.String()
}

func primary(pkgs ...tpkg) []byte {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?><metadata xmlns="http://linux.duke.edu/metadata/common" xmlns:rpm="http://linux.duke.edu/metadata/rpm">`)
	for _, p := range pkgs {
		if p.arch == "" {
			p.arch = "x86_64"
		}
		if p.evr == "" {
			p.evr = "1.0-1"
		}
		ver, rel, _ := strings.Cut(p.evr, "-")
		prov := append([]string{p.name + " = " + p.evr}, p.prov...)
		fmt.Fprintf(&sb, `<package type="rpm"><name>%s</name><arch>%s</arch><version epoch="0" ver="%s" rel="%s"/>`, p.name, p.arch, ver, rel)
		sb.WriteString(`<format>`)
		sb.WriteString(depSection("provides", prov))
		sb.WriteString(depSection("requires", p.req))
		sb.WriteString(depSection("conflicts", p.conf))
		sb.WriteString(depSection("obsoletes", p.obs))
		sb.WriteString(depSection("recommends", p.rec))
		for _, f := range p.fil {
			sb.WriteString("<file>" + f + "</file>")
		}
		sb.WriteString(`</format></package>`)
	}
	sb.WriteString(`</metadata>`)
	return []byte(sb.String())
}

func newTestPool(t *testing.T, arch string, repos ...[]tpkg) *solv.PathPool {
	t.Helper()
	var blobs []solv.RepoBlob
	for i, r := range repos {
		blobs = append(blobs, solv.RepoBlob{Name: fmt.Sprintf("repo%d", i), Data: primary(r...)})
	}
	pp, err := solv.NewPathPool(arch, blobs)
	if err != nil {
		t.Fatalf("NewPathPool: %v", err)
	}
	t.Cleanup(pp.Close)
	return pp
}

func sortedPkgNames(r *solv.ExpansionResult) string {
	n := r.Names()
	for i := range n {
		for j := i + 1; j < len(n); j++ {
			if n[j] < n[i] {
				n[i], n[j] = n[j], n[i]
			}
		}
	}
	return strings.Join(n, " ")
}

type expandCase struct {
	name    string
	repo    []tpkg
	cfg     *solv.ExpandConfig
	deps    []string
	want    string // sorted package names
	wantErr string // exact OBS error text
}

func runExpandCases(t *testing.T, cases []expandCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pp := newTestPool(t, "x86_64", tc.repo)
			res, err := pp.Expand(tc.cfg, tc.deps...)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error %q, got packages %v", tc.wantErr, res.Names())
				}
				if err.Error() != tc.wantErr {
					t.Fatalf("error = %q, want %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := sortedPkgNames(res); got != tc.want {
				t.Fatalf("packages = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExpandVersionedDeps(t *testing.T) {
	repo := []tpkg{
		{name: "libfoo", evr: "1.0-1", prov: []string{"foo = 1.0"}},
		{name: "libfoo-ng", evr: "2.0-1", prov: []string{"foo = 2.0"}},
		{name: "app", req: []string{"foo >= 1.5"}},
		{name: "oldapp", req: []string{"foo < 1.5"}},
		{name: "exact", req: []string{"foo = 2.0"}},
		{name: "never", req: []string{"foo > 3"}},
		{name: "epoch", req: []string{"foo >= 1:0"}},
	}
	runExpandCases(t, []expandCase{
		{name: "ge", repo: repo, deps: []string{"app"}, want: "app libfoo-ng"},
		{name: "lt", repo: repo, deps: []string{"oldapp"}, want: "libfoo oldapp"},
		{name: "eq", repo: repo, deps: []string{"exact"}, want: "exact libfoo-ng"},
		{name: "direct versioned", repo: repo, deps: []string{"foo >= 1.5"}, want: "libfoo-ng"},
		{name: "direct double equals", repo: repo, deps: []string{"foo == 1.0"}, want: "libfoo"},
		{name: "direct shift ops", repo: repo, deps: []string{"foo >> 1.5"}, want: "libfoo-ng"},
		{name: "unsatisfiable", repo: repo, deps: []string{"never"}, wantErr: "nothing provides foo > 3 needed by never"},
		{name: "epoch bigger", repo: repo, deps: []string{"epoch"}, wantErr: "nothing provides foo >= 1:0 needed by epoch"},
	})
}

func TestExpandRichDeps(t *testing.T) {
	repo := []tpkg{
		{name: "a"}, {name: "b"}, {name: "c", req: []string{"x"}}, {name: "x"},
		{name: "or-only-a", req: []string{"(a or zzz)"}},
		{name: "and-both", req: []string{"(a and b)"}},
		{name: "and-missing", req: []string{"(a and zzz)"}},
		{name: "if-cond-installed", req: []string{"(b if a)", "a"}},
		{name: "if-cond-absent", req: []string{"(zzz if a)"}},
		{name: "unless", req: []string{"(a unless zzz)"}},
		{name: "or-versioned", req: []string{"(zzz >= 1 or a >= 1.0)"}},
		{name: "none-of", req: []string{"(zzz or yyy)"}},
	}
	runExpandCases(t, []expandCase{
		{name: "or", repo: repo, deps: []string{"or-only-a"}, want: "a or-only-a"},
		{name: "and", repo: repo, deps: []string{"and-both"}, want: "a and-both b"},
		{name: "and missing", repo: repo, deps: []string{"and-missing"}, wantErr: "nothing provides (a and zzz) needed by and-missing"},
		{name: "if with installed condition", repo: repo, deps: []string{"if-cond-installed"}, want: "a b if-cond-installed"},
		{name: "if with absent condition", repo: repo, deps: []string{"if-cond-absent"}, want: "if-cond-absent"},
		{name: "unless", repo: repo, deps: []string{"unless"}, want: "a unless"},
		{name: "or versioned", repo: repo, deps: []string{"or-versioned"}, want: "a or-versioned"},
		{name: "none of", repo: repo, deps: []string{"none-of"}, wantErr: "nothing provides (zzz or yyy) needed by none-of"},
		{name: "direct rich", repo: repo, deps: []string{"(b or zzz)"}, want: "b"},
	})
}

func TestExpandFileDeps(t *testing.T) {
	repo := []tpkg{
		{name: "bash", prov: []string{"/bin/sh"}},
		{name: "coreutils"},
		{name: "needs-sh", req: []string{"/bin/sh"}},
		{name: "needs-ls", req: []string{"/usr/bin/ls"}},
		{name: "needs-sh-file", req: []string{"/usr/bin/ls2"}, fil: nil},
		{name: "filepkg", fil: []string{"/usr/bin/real"}},
		{name: "needs-real", req: []string{"/usr/bin/real"}},
	}
	mapped := &solv.ExpandConfig{FileProvides: map[string][]string{"/usr/bin/ls": {"coreutils"}}}
	keep := &solv.ExpandConfig{ExpandFlags: []string{"keepfilerequires"}}
	runExpandCases(t, []expandCase{
		{name: "file provide in metadata", repo: repo, deps: []string{"needs-sh"}, want: "bash needs-sh"},
		{name: "FileProvides mapping", repo: repo, cfg: mapped, deps: []string{"needs-ls"}, want: "coreutils needs-ls"},
		{name: "unprovided file dep dropped", repo: repo, deps: []string{"needs-ls"}, want: "needs-ls"},
		{name: "keepfilerequires reports", repo: repo, cfg: keep, deps: []string{"needs-ls"}, wantErr: "nothing provides /usr/bin/ls needed by needs-ls"},
		{name: "primary file entries are not provides", repo: repo, deps: []string{"needs-real"}, want: "needs-real"},
		{name: "primary file entry via FileProvides", repo: repo, cfg: &solv.ExpandConfig{FileProvides: map[string][]string{"/usr/bin/real": {"filepkg"}}}, deps: []string{"needs-real"}, want: "filepkg needs-real"},
		{name: "direct file dep", repo: repo, cfg: mapped, deps: []string{"/usr/bin/ls"}, want: "coreutils"},
	})
}

func TestExpandIgnore(t *testing.T) {
	repo := []tpkg{
		{name: "a", req: []string{"b", "c"}},
		{name: "d", req: []string{"c"}},
		{name: "b"},
		{name: "c"},
		{name: "e", req: []string{"missing"}},
	}
	runExpandCases(t, []expandCase{
		{name: "global ignore", repo: repo, cfg: &solv.ExpandConfig{Ignore: []string{"b"}}, deps: []string{"a"}, want: "a c"},
		{name: "scoped ignore applies to a", repo: repo, cfg: &solv.ExpandConfig{Ignore: []string{"a:c"}}, deps: []string{"a"}, want: "a b"},
		{name: "scoped ignore does not apply to d", repo: repo, cfg: &solv.ExpandConfig{Ignore: []string{"a:c"}}, deps: []string{"d"}, want: "c d"},
		{name: "scoped ignore with comma list", repo: repo, cfg: &solv.ExpandConfig{Ignore: []string{"a:b,c"}}, deps: []string{"a"}, want: "a"},
		{name: "ignore of nonexistent dep", repo: repo, cfg: &solv.ExpandConfig{Ignore: []string{"missing"}}, deps: []string{"e"}, want: "e"},
		{name: "ignore does not apply to direct", repo: repo, cfg: &solv.ExpandConfig{Ignore: []string{"b"}}, deps: []string{"b"}, want: "b"},
		{name: "negative dep ignores", repo: repo, deps: []string{"a", "-b"}, want: "a c"},
		{name: "ignoreignore", repo: repo, cfg: &solv.ExpandConfig{Ignore: []string{"b"}}, deps: []string{"a", "--ignoreignore--"}, want: "a b c"},
	})
}

func TestExpandPrefer(t *testing.T) {
	repo := []tpkg{
		{name: "sendmail", prov: []string{"smtp"}},
		{name: "postfix", prov: []string{"smtp"}},
		{name: "exim", prov: []string{"smtp"}},
		{name: "app", req: []string{"smtp"}},
		{name: "other", req: []string{"smtp"}},
	}
	cases := []expandCase{
		{name: "no prefer", repo: repo, deps: []string{"app"}, wantErr: "have choice for smtp needed by app: exim postfix sendmail"},
		{name: "direct no prefer", repo: repo, deps: []string{"smtp"}, wantErr: "have choice for smtp: exim postfix sendmail"},
		{name: "positive prefer", repo: repo, cfg: &solv.ExpandConfig{Prefer: []string{"postfix"}}, deps: []string{"app"}, want: "app postfix"},
		{name: "negative prefer leaves one", repo: repo, cfg: &solv.ExpandConfig{Prefer: []string{"-sendmail", "-exim"}}, deps: []string{"app"}, want: "app postfix"},
		{name: "negative prefer still ambiguous", repo: repo, cfg: &solv.ExpandConfig{Prefer: []string{"-sendmail"}}, deps: []string{"app"}, wantErr: "have choice for smtp needed by app: exim postfix"},
		{name: "context prefer for app", repo: repo, cfg: &solv.ExpandConfig{Prefer: []string{"app:exim"}}, deps: []string{"app"}, want: "app exim"},
		{name: "context prefer not for other", repo: repo, cfg: &solv.ExpandConfig{Prefer: []string{"app:exim"}}, deps: []string{"other"}, wantErr: "have choice for smtp needed by other: exim postfix sendmail"},
		{name: "context negative prefer", repo: repo, cfg: &solv.ExpandConfig{Prefer: []string{"-app:sendmail", "-app:postfix"}}, deps: []string{"app"}, want: "app exim"},
		{name: "prefer list order decides", repo: repo, cfg: &solv.ExpandConfig{Prefer: []string{"sendmail", "postfix"}}, deps: []string{"app"}, want: "app sendmail"},
		{name: "exact name beats prefer", repo: repo, cfg: &solv.ExpandConfig{Prefer: []string{"postfix"}}, deps: []string{"sendmail"}, want: "sendmail"},
	}
	runExpandCases(t, cases)
}

func TestExpandConflicts(t *testing.T) {
	repo := []tpkg{
		{name: "a"}, {name: "b"},
		{name: "c", conf: []string{"d"}},
		{name: "d"},
		{name: "needs-d", req: []string{"d"}},
		{name: "e", conf: []string{"f = 1.0"}},
		{name: "f", evr: "1.0-1"},
		{name: "prov1", prov: []string{"virt"}, conf: []string{"a"}},
		{name: "prov2", prov: []string{"virt"}},
		{name: "wants-virt", req: []string{"virt"}},
	}
	runExpandCases(t, []expandCase{
		{name: "config conflict pair", repo: repo, cfg: &solv.ExpandConfig{Conflict: []string{"a:b"}}, deps: []string{"a", "b"}, wantErr: "a conflicts with b, b conflicts with a"},
		{name: "config conflict only one side", repo: repo, cfg: &solv.ExpandConfig{Conflict: []string{"a:b"}}, deps: []string{"a"}, want: "a"},
		{name: "config conflict single name", repo: repo, cfg: &solv.ExpandConfig{Conflict: []string{"a"}}, deps: []string{"a"}, wantErr: "a is in conflict"},
		{name: "package conflict with installed", repo: repo, deps: []string{"d", "c"}, wantErr: "c conflicts with d"},
		{name: "package conflict prunes only provider", repo: repo, deps: []string{"c", "needs-d"}, wantErr: "conflict for providers of d needed by needs-d, (provider d is in conflict with c)"},
		{name: "package conflict ignored", repo: repo, cfg: &solv.ExpandConfig{ExpandFlags: []string{"ignoreconflicts"}}, deps: []string{"c", "d"}, want: "c d"},
		{name: "versioned conflict", repo: repo, deps: []string{"e", "f"}, wantErr: "e conflicts with f"},
		{name: "conflict prunes provider", repo: repo, deps: []string{"a", "wants-virt"}, want: "a prov2 wants-virt"},
		{name: "direct conflict dep picks other provider", repo: repo, deps: []string{"virt", "!prov1"}, want: "prov2"},
		{name: "direct conflict dep resolves", repo: repo, deps: []string{"wants-virt", "!prov1"}, want: "prov2 wants-virt"},
		{name: "direct conflict install", repo: repo, deps: []string{"a", "!a"}, wantErr: "a is in conflict"},
		{name: "conflict for providers", repo: repo, deps: []string{"a", "prov1", "!prov1"}, wantErr: "prov1 is in conflict"},
	})
}

func TestExpandErrorTexts(t *testing.T) {
	repo := []tpkg{
		{name: "app", req: []string{"nope", "libfoo.so.1()(64bit)"}},
		{name: "two-a", prov: []string{"ch"}}, {name: "two-b", prov: []string{"ch"}},
		{name: "needs-ch", req: []string{"ch"}},
		{name: "rpmreq", req: []string{"rpmlib(PayloadFilesHavePrefix) <= 4.0-1"}},
	}
	runExpandCases(t, []expandCase{
		{name: "nothing provides direct", repo: repo, deps: []string{"zzz"}, wantErr: "nothing provides zzz"},
		{name: "nothing provides needed by", repo: repo, deps: []string{"app"}, wantErr: "nothing provides nope needed by app, nothing provides libfoo.so.1()(64bit) needed by app"},
		{name: "have choice needed by", repo: repo, deps: []string{"needs-ch"}, wantErr: "have choice for ch needed by needs-ch: two-a two-b"},
		{name: "rpmlib ignored", repo: repo, deps: []string{"rpmreq"}, want: "rpmreq"},
	})
}

func TestExpandPrereqMarkerDeps(t *testing.T) {
	repo := []tpkg{
		{name: "needs-pre", req: []string{"plain", "pre:early", "pre:early2 >= 1"}},
		{name: "plain"}, {name: "early"}, {name: "early2"},
		{name: "only-pre", req: []string{"pre:missing-pre"}},
	}
	runExpandCases(t, []expandCase{
		{name: "pre requires followed", repo: repo, deps: []string{"needs-pre"}, want: "early early2 needs-pre plain"},
		{name: "missing pre requires reported", repo: repo, deps: []string{"only-pre"}, wantErr: "nothing provides missing-pre needed by only-pre"},
	})
}

func TestExpandErrorAs(t *testing.T) {
	pp := newTestPool(t, "x86_64", []tpkg{{name: "a", prov: []string{"v"}}, {name: "b", prov: []string{"v"}}})
	_, err := pp.Expand(nil, "zzz >= 1")
	var np *solv.NoProviderError
	if !errors.As(err, &np) || np.Requirement != "zzz >= 1" {
		t.Fatalf("NoProviderError not extracted from %v: %+v", err, np)
	}
	_, err = pp.Expand(nil, "v")
	var amb *solv.AmbiguityError
	if !errors.As(err, &amb) || amb.Requirement != "v" {
		t.Fatalf("AmbiguityError not extracted from %v: %+v", err, amb)
	}
	var ee *solv.ExpandError
	if !errors.As(err, &ee) || len(ee.Messages) != 1 {
		t.Fatalf("ExpandError not extracted: %v", err)
	}
}

func TestExpandRecommendsChoice(t *testing.T) {
	repo := []tpkg{
		{name: "x1", prov: []string{"x"}}, {name: "x2", prov: []string{"x"}},
		{name: "base", rec: []string{"x2"}},
		{name: "app", req: []string{"x"}},
	}
	runExpandCases(t, []expandCase{
		{name: "recommends of installed pick provider", repo: repo, deps: []string{"base", "app"}, want: "app base x2"},
		{name: "without recommending package ambiguous", repo: repo, deps: []string{"app"}, wantErr: "have choice for x needed by app: x1 x2"},
	})
}

func TestExpandOrder(t *testing.T) {
	pp := newTestPool(t, "x86_64", []tpkg{
		{name: "top", req: []string{"mid"}},
		{name: "mid", req: []string{"low"}},
		{name: "low"},
		{name: "cyc1", req: []string{"cyc2"}},
		{name: "cyc2", req: []string{"cyc1"}},
	})
	res, err := pp.Expand(nil, "top")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(res.Names(), " "); got != "low mid top" {
		t.Fatalf("order = %q", got)
	}
	res, err = pp.Expand(nil, "cyc1")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(res.Names(), " "); got != "cyc1 cyc2" {
		t.Fatalf("cycle order = %q", got)
	}
}

func TestGetBuildSubstituteAndSets(t *testing.T) {
	repo := []tpkg{
		{name: "gcc"}, {name: "make"}, {name: "glibc"},
		{name: "rpm-build", req: []string{"glibc"}},
		{name: "util"},
		{name: "self-sub-a"}, {name: "self-sub-b"},
		{name: "mysub"},
	}
	cfg := &solv.ExpandConfig{
		Substitute: map[string][]string{
			"c_compiler":   {"gcc", "make"},
			"self":         {"self", "self-sub-b"},
			"drop":         {},
			"nested":       {"c_compiler", "util"},
			"versioned-sb": {"gcc="},
		},
		Preinstall: []string{"glibc"},
		Required:   []string{"rpm-build"},
		Support:    []string{"util"},
	}
	cases := []struct {
		name string
		deps []string
		want string
		fn   string
	}{
		{"substitute list", []string{"c_compiler"}, "gcc glibc make rpm-build util", "build"},
		{"nested substitute", []string{"nested"}, "gcc glibc make rpm-build util", "build"},
		{"empty substitute drops dep", []string{"drop"}, "glibc rpm-build util", "build"},
		{"substitute with trailing equals", []string{"versioned-sb"}, "gcc glibc rpm-build util", "build"},
		{"substitute may keep itself", []string{"mysub", "c_compiler"}, "gcc glibc make mysub rpm-build util", "build"},
		{"get_deps drops support and preinstall", []string{"gcc"}, "gcc rpm-build", "deps"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pp := newTestPool(t, "x86_64", repo)
			var res *solv.ExpansionResult
			var err error
			if tc.fn == "build" {
				res, err = pp.GetBuild(cfg, nil, tc.deps)
			} else {
				res, err = pp.GetDeps(cfg, nil, tc.deps)
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := sortedPkgNames(res); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestGetBuildPreinstallExpand(t *testing.T) {
	repo := []tpkg{
		{name: "filesystem"}, {name: "glibc", req: []string{"filesystem"}}, {name: "bash", req: []string{"glibc"}},
		{name: "app"},
	}
	cfg := &solv.ExpandConfig{Preinstall: []string{"bash"}, ExpandFlags: []string{"preinstallexpand"}}
	pp := newTestPool(t, "x86_64", repo)
	res, err := pp.GetBuild(cfg, nil, []string{"app"})
	if err != nil {
		t.Fatal(err)
	}
	if got := sortedPkgNames(res); got != "app bash filesystem glibc" {
		t.Fatalf("got %q", got)
	}
	bad := &solv.ExpandConfig{Preinstall: []string{"missing"}, ExpandFlags: []string{"preinstallexpand"}}
	_, err = pp.GetBuild(bad, nil, []string{"app"})
	if err == nil || err.Error() != "preinstalls: nothing provides missing" {
		t.Fatalf("got %v", err)
	}
}

func TestPathPoolShadowing(t *testing.T) {
	own := []tpkg{
		{name: "foo", evr: "1.0-1", prov: []string{"x"}},
	}
	base := []tpkg{
		{name: "foo", evr: "2.0-1", prov: []string{"y"}},
		{name: "bar", req: []string{"foo >= 2"}},
		{name: "baz", req: []string{"y"}},
		{name: "qux", req: []string{"x"}},
		{name: "alt", prov: []string{"x"}},
	}
	pp := newTestPool(t, "x86_64", own, base)
	res, err := pp.Expand(nil, "foo")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Packages) != 1 || res.Packages[0].EVR != "1.0-1" || res.Packages[0].Repo != 0 {
		t.Fatalf("expected own foo 1.0-1 from repo 0, got %+v", res.Packages)
	}
	if _, err = pp.Expand(nil, "bar"); err == nil || err.Error() != "nothing provides foo >= 2 needed by bar" {
		t.Fatalf("hidden foo 2.0 must not satisfy bar: %v", err)
	}
	if _, err = pp.Expand(nil, "baz"); err == nil || err.Error() != "nothing provides y needed by baz" {
		t.Fatalf("hidden foo must not provide y: %v", err)
	}
	// qux requires x: own foo and base alt both provide it
	if _, err = pp.Expand(nil, "qux"); err == nil || err.Error() != "have choice for x needed by qux: alt foo" {
		t.Fatalf("expected choice, got %v", err)
	}
	res, err = pp.Expand(nil, "alt")
	if err != nil || res.Packages[0].Repo != 1 {
		t.Fatalf("alt must come from repo 1: %+v %v", res, err)
	}
}

func TestPathPoolHighestVersionWins(t *testing.T) {
	pp := newTestPool(t, "x86_64", []tpkg{
		{name: "foo", evr: "1.0-1"}, {name: "foo", evr: "1.10-1"}, {name: "foo", evr: "1.9-1"},
	})
	res, err := pp.Expand(nil, "foo")
	if err != nil {
		t.Fatal(err)
	}
	if res.Packages[0].EVR != "1.10-1" {
		t.Fatalf("got %s", res.Packages[0].EVR)
	}
}

func TestPathPoolArchPolicy(t *testing.T) {
	repo := []tpkg{
		{name: "native", arch: "x86_64"},
		{name: "compat", arch: "i586"},
		{name: "compat686", arch: "i686"},
		{name: "arm", arch: "aarch64"},
		{name: "any", arch: "noarch"},
		{name: "both", arch: "noarch", evr: "2.0-1"},
		{name: "both", arch: "x86_64", evr: "1.0-1"},
		{name: "srcpkg", arch: "src"},
	}
	cases := []struct {
		pool    string
		dep     string
		wantArc string
		wantErr string
	}{
		{"x86_64", "native", "x86_64", ""},
		{"x86_64", "compat", "i586", ""},
		{"x86_64", "compat686", "i686", ""},
		{"x86_64", "arm", "", "nothing provides arm"},
		{"x86_64", "any", "noarch", ""},
		{"x86_64", "both", "x86_64", ""},
		{"x86_64", "srcpkg", "", "nothing provides srcpkg"},
		{"i586", "native", "", "nothing provides native"},
		{"i586", "compat", "i586", ""},
		{"aarch64", "arm", "aarch64", ""},
		{"aarch64", "native", "", "nothing provides native"},
	}
	for _, tc := range cases {
		t.Run(tc.pool+"/"+tc.dep, func(t *testing.T) {
			pp := newTestPool(t, tc.pool, repo)
			res, err := pp.Expand(nil, tc.dep)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if res.Packages[0].Arch != tc.wantArc {
				t.Fatalf("arch = %s, want %s", res.Packages[0].Arch, tc.wantArc)
			}
		})
	}
}

func TestPoolCacheAndSolvBlobs(t *testing.T) {
	xml1 := primary(tpkg{name: "foo", prov: []string{"cap"}})
	xml2 := primary(tpkg{name: "bar", prov: []string{"cap"}})
	blob, err := solv.RPMMDToSolv("r", xml1)
	if err != nil {
		t.Fatal(err)
	}
	cache := solv.NewPoolCache(2)
	defer cache.Close()

	p1, err := cache.Get("x86_64", []solv.RepoBlob{{Name: "r", Data: blob, Hash: "h1"}})
	if err != nil {
		t.Fatal(err)
	}
	p1b, err := cache.Get("x86_64", []solv.RepoBlob{{Name: "r", Data: nil, Hash: "h1"}})
	if err != nil || p1 != p1b {
		t.Fatalf("same key must return the cached pool: %v", err)
	}
	p2, err := cache.Get("x86_64", []solv.RepoBlob{{Name: "r", Data: xml2, Hash: "h2"}})
	if err != nil || p2 == p1 {
		t.Fatalf("changed hash must produce a new pool: %v", err)
	}
	p3, err := cache.Get("i586", []solv.RepoBlob{{Name: "r", Data: xml2, Hash: "h2"}})
	if err != nil || p3 == p2 {
		t.Fatalf("different arch must produce a new pool: %v", err)
	}
	if cache.Len() != 2 {
		t.Fatalf("cache must evict beyond capacity, has %d", cache.Len())
	}
	res, err := p2.Expand(nil, "cap")
	if err != nil || res.Packages[0].Name != "bar" {
		t.Fatalf("unexpected: %+v %v", res, err)
	}
	res, err = p1.Expand(nil, "cap")
	if err == nil && res.Packages[0].Name != "foo" {
		t.Fatalf("unexpected: %+v", res)
	}
}

func TestPathPoolConcurrentResolve(t *testing.T) {
	pp := newTestPool(t, "x86_64", []tpkg{{name: "a", req: []string{"b"}}, {name: "b"}})
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		go func() {
			res, err := pp.Expand(nil, "a")
			if err == nil && len(res.Packages) != 2 {
				err = fmt.Errorf("got %v", res.Names())
			}
			errs <- err
		}()
	}
	for i := 0; i < 16; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestResultChecksumOnlySHA256(t *testing.T) {
	xml := `<?xml version="1.0"?><metadata xmlns="http://linux.duke.edu/metadata/common" xmlns:rpm="http://linux.duke.edu/metadata/rpm">
<package type="rpm"><name>s</name><arch>x86_64</arch><version epoch="0" ver="1" rel="1"/><checksum type="sha256" pkgid="YES">e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855</checksum><format/></package>
<package type="rpm"><name>m</name><arch>x86_64</arch><version epoch="0" ver="1" rel="1"/><checksum type="md5" pkgid="YES">d41d8cd98f00b204e9800998ecf8427e</checksum><format/></package>
</metadata>`
	pp, err := solv.NewPathPool("x86_64", []solv.RepoBlob{{Name: "r", Data: []byte(xml)}})
	if err != nil {
		t.Fatal(err)
	}
	defer pp.Close()
	res, err := pp.Expand(nil, "s", "m")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range res.Packages {
		switch p.Name {
		case "s":
			if p.SHA256 != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
				t.Errorf("sha256 missing: %q", p.SHA256)
			}
		case "m":
			if p.SHA256 != "" {
				t.Errorf("md5 checksum must not be reported: %q", p.SHA256)
			}
		}
	}
}
