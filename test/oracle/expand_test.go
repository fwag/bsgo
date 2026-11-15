//go:build oracle

package oracle

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/solv"
)

const archPath = "x86_64:i686:i586:noarch"

// parseConf maps the subset of prjconf directives the expander reads onto
// solv.ExpandConfig. The same text is handed to the Perl reference.
func parseConf(text string) *solv.ExpandConfig {
	cfg := &solv.ExpandConfig{Substitute: map[string][]string{}, FileProvides: map[string][]string{}}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		key, vals := strings.ToLower(f[0]), f[1:]
		switch key {
		case "preinstall:":
			cfg.Preinstall = append(cfg.Preinstall, vals...)
		case "required:":
			cfg.Required = append(cfg.Required, vals...)
		case "support:":
			cfg.Support = append(cfg.Support, vals...)
		case "keep:":
			cfg.Keep = append(cfg.Keep, vals...)
		case "prefer:":
			cfg.Prefer = append(cfg.Prefer, vals...)
		case "ignore:":
			cfg.Ignore = append(cfg.Ignore, vals...)
		case "conflict:":
			cfg.Conflict = append(cfg.Conflict, vals...)
		case "ignorerebuild:":
			cfg.IgnoreRebuild = append(cfg.IgnoreRebuild, vals...)
		case "expandflags:":
			cfg.ExpandFlags = append(cfg.ExpandFlags, vals...)
		case "substitute:":
			cfg.Substitute[vals[0]] = append([]string(nil), vals[1:]...)
		case "fileprovides:":
			cfg.FileProvides[vals[0]] = append([]string(nil), vals[1:]...)
		case "type:":
			cfg.BuildType = vals[0]
		}
	}
	return cfg
}

type oracleResult struct {
	names []string
	errs  []string
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func specFor(deps []string) string {
	var sb strings.Builder
	sb.WriteString("Name: oracletest\nVersion: 1\nRelease: 1\nSummary: x\nLicense: MIT\n")
	for _, d := range deps {
		sb.WriteString("BuildRequires: " + d + "\n")
	}
	sb.WriteString("%description\nx\n")
	return sb.String()
}

func runPerl(t *testing.T, f *fixture, conf string, deps []string) oracleResult {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "oracle.conf"), conf)
	spec := filepath.Join(dir, "oracletest.spec")
	writeFile(t, spec, specFor(deps))
	cmd := exec.Command(expanddeps, "--configdir", dir, "--dist", "oracle",
		"--depfile", f.depfile, "--archpath", archPath, spec)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	res := oracleResult{}
	if runErr != nil {
		var ee *exec.ExitError
		if !errors.As(runErr, &ee) {
			t.Fatalf("running expanddeps: %v", runErr)
		}
		for _, l := range strings.Split(stderr.String(), "\n") {
			if strings.HasPrefix(l, "  ") {
				res.errs = append(res.errs, strings.TrimSpace(l))
			}
		}
		return res
	}
	for _, l := range strings.Split(stdout.String(), "\n") {
		name, _, ok := strings.Cut(l, " ")
		if !ok || strings.HasSuffix(name, ":") {
			continue
		}
		res.names = append(res.names, name)
	}
	sort.Strings(res.names)
	return res
}

func runGo(f *fixture, conf string, deps []string) oracleResult {
	res, err := f.pool.GetBuild(parseConf(conf), []string{"oracletest"}, deps)
	if err != nil {
		var ee *solv.ExpandError
		if errors.As(err, &ee) {
			return oracleResult{errs: ee.Messages}
		}
		return oracleResult{errs: []string{err.Error()}}
	}
	names := res.Names()
	sort.Strings(names)
	return oracleResult{names: names}
}

// preferred resolves the choices that a Tumbleweed pool leaves open so most
// cases reach a full expansion.
const preferred = `Prefer: libstdc++6 libz1 zlib-devel libncurses6 libreadline8 libgcc_s1 libopenssl3 openssl-devel
Prefer: libcurl4 libcurl-devel libpcre2-8-0 python313-base python313-devel python313
Prefer: libxml2-2 libxml2-devel ncurses-devel
`

const baseConf = `Type: spec
Preinstall: filesystem glibc bash coreutils
Required: gcc make
FileProvides: /bin/sh bash
` + preferred

type oracleCase struct {
	name string
	conf string
	deps []string
}

var oracleCases = []oracleCase{
	{"basic", baseConf, []string{"gcc", "make", "pkgconfig(zlib)", "python3-devel"}},
	{"versioned", baseConf, []string{"zlib-devel >= 1.2", "gcc >= 10", "make", "bash > 4"}},
	{"versioned unsatisfiable", baseConf, []string{"zlib-devel >= 99"}},
	{"rich or", baseConf, []string{"(gcc or clang)", "make"}},
	{"rich and", baseConf, []string{"(zlib-devel and pkgconfig(zlib))"}},
	{"rich versioned or", baseConf, []string{"(zlib-devel >= 99 or make >= 4)"}},
	{"rich nothing", baseConf, []string{"(nonexistent1 or nonexistent2)"}},
	{"nothing provides", baseConf, []string{"nonexistent-pkg >= 1", "pkgconfig(doesnotexist)"}},
	{"choice error", "Type: spec\nPreinstall: filesystem glibc bash\nFileProvides: /bin/sh bash\n", []string{"pkgconfig(zlib)"}},
	{"substitute", baseConf + "Substitute: c_compiler gcc make\nSubstitute: unwanted\n", []string{"c_compiler", "unwanted", "zlib-devel"}},
	{"substitute nested", baseConf + "Substitute: tc c_compiler cmake\nSubstitute: c_compiler gcc\n", []string{"tc"}},
	{"ignore global", baseConf + "Ignore: libz.so.1()(64bit)\n", []string{"python3-devel"}},
	{"ignore scoped", baseConf + "Ignore: python313-base:libz.so.1()(64bit)\n", []string{"python3-devel"}},
	{"ignore scoped other package", baseConf + "Ignore: gcc:libz.so.1()(64bit)\n", []string{"python3-devel"}},
	{"prefer positive", baseConf + "Prefer: libz-ng-compat1\n", []string{"python3-devel"}},
	{"prefer negative", "Type: spec\nPreinstall: filesystem glibc bash coreutils\nFileProvides: /bin/sh bash\nPrefer: -zlib-ng-compat-devel -libz-ng-compat1\n" + preferred, []string{"pkgconfig(zlib)", "python3-devel"}},
	{"prefer context", "Type: spec\nPreinstall: filesystem glibc bash coreutils\nFileProvides: /bin/sh bash\n" + strings.ReplaceAll(preferred, "libz1 zlib-devel", "") + "Prefer: gcc16:libz1 python313-base:libz1 zlib-devel\n", []string{"gcc", "python3-devel"}},
	{"conflict pair", baseConf + "Conflict: gcc:make\n", []string{"gcc", "make"}},
	{"conflict single side", baseConf + "Conflict: cmake:ninja\n", []string{"cmake"}},
	{"conflict name", baseConf + "Conflict: zlib-devel\n", []string{"zlib-devel"}},
	{"file deps with FileProvides", baseConf + "FileProvides: /usr/bin/ld binutils\n", []string{"gcc"}},
	{"keepfilerequires", baseConf + "ExpandFlags: keepfilerequires\n", []string{"gcc", "make"}},
	{"ignoreconflicts", baseConf + "ExpandFlags: ignoreconflicts\n", []string{"gcc", "make"}},
	{"preinstallexpand", baseConf + "ExpandFlags: preinstallexpand\n", []string{"gcc", "make"}},
	{"support", baseConf + "Support: vim\n", []string{"make"}},
	{"keep", baseConf + "Keep: gcc\n", []string{"make"}},
	{"many deps", baseConf, []string{"cmake", "gcc-c++", "pkgconfig(openssl)", "pkgconfig(libcurl)", "python3-devel", "perl", "pkgconfig(libxml-2.0)", "pkgconfig(ncurses)", "autoconf", "automake", "libtool"}},
	{"negative dep", baseConf, []string{"gcc", "make", "-make"}},
}

func TestExpandMatchesPerl(t *testing.T) {
	f := loadFixture(t)
	t.Logf("repository: %s", f.label)
	identical := 0
	for _, tc := range oracleCases {
		t.Run(tc.name, func(t *testing.T) {
			want := runPerl(t, f, tc.conf, tc.deps)
			got := runGo(f, tc.conf, tc.deps)
			if diff := compare(want, got); diff != "" {
				t.Errorf("diff against Perl expanddeps:\n%s", diff)
				return
			}
			identical++
			t.Logf("identical: %d packages, %d errors", len(want.names), len(want.errs))
		})
	}
	t.Logf("%d/%d cases identical", identical, len(oracleCases))
}

func compare(want, got oracleResult) string {
	sw, sg := sortedCopy(want.errs), sortedCopy(got.errs)
	var sb strings.Builder
	if strings.Join(sw, "\n") != strings.Join(sg, "\n") {
		sb.WriteString("errors:\n  perl: " + strings.Join(sw, " | ") + "\n  go:   " + strings.Join(sg, " | ") + "\n")
	}
	missing, extra := setDiff(want.names, got.names)
	if len(missing) > 0 || len(extra) > 0 {
		sb.WriteString("packages only in perl: " + strings.Join(missing, " ") + "\n")
		sb.WriteString("packages only in go:   " + strings.Join(extra, " ") + "\n")
	}
	return sb.String()
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func setDiff(want, got []string) (onlyWant, onlyGot []string) {
	ws, gs := map[string]bool{}, map[string]bool{}
	for _, n := range want {
		ws[n] = true
	}
	for _, n := range got {
		gs[n] = true
		if !ws[n] {
			onlyGot = append(onlyGot, n)
		}
	}
	for _, n := range want {
		if !gs[n] {
			onlyWant = append(onlyWant, n)
		}
	}
	return onlyWant, onlyGot
}
