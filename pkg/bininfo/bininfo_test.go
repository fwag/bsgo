package bininfo_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/openSUSE/bsgo/pkg/bininfo"
)

const testSpec = `Name: bitest
Version: 1.2
Release: 3
Epoch: 2
Summary: bininfo test package
License: MIT
BuildArch: noarch
Requires: libfoo.so.1()(64bit)
Requires(pre): /bin/sh
Requires: bash >= 4.0
Requires: (foo or bar)
Conflicts: old < 1.0
Obsoletes: older <= 0.5
Recommends: rec
Suggests: sug > 2
Supplements: sup
Enhances: enh = 1
Provides: virt = 1.0

%description
bininfo test package

%files
`

func buildTestRPM(t *testing.T, spec string) string {
	t.Helper()
	if _, err := exec.LookPath("rpmbuild"); err != nil {
		t.Skip("rpmbuild not available")
	}
	if _, err := exec.LookPath("rpm"); err != nil {
		t.Skip("rpm not available")
	}
	top := t.TempDir()
	if err := os.MkdirAll(filepath.Join(top, "SPECS"), 0o750); err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(top, "SPECS", "t.spec")
	if err := os.WriteFile(specPath, []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("rpmbuild", "--define", "_topdir "+top, "-bb", specPath).CombinedOutput()
	if err != nil {
		t.Skipf("rpmbuild failed (%v): %s", err, out)
	}
	matches, err := filepath.Glob(filepath.Join(top, "RPMS", "*", "*.rpm"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected one built rpm, got %v (%v)", matches, err)
	}
	return matches[0]
}

func hasDep(deps []bininfo.Dep, want bininfo.Dep) bool {
	for _, d := range deps {
		if reflect.DeepEqual(d, want) {
			return true
		}
	}
	return false
}

func TestExtract(t *testing.T) {
	rpmPath := buildTestRPM(t, testSpec)
	info, err := bininfo.Extract(context.Background(), rpmPath)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	data, err := os.ReadFile(rpmPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)

	if info.Name != "bitest" || info.Epoch != "2" || info.Version != "1.2" || info.Release != "3" || info.Arch != "noarch" {
		t.Errorf("unexpected NEVRA: %+v", info)
	}
	if info.EVR() != "2:1.2-3" {
		t.Errorf("EVR = %q", info.EVR())
	}
	if info.SourceRPM != "bitest-1.2-3.src.rpm" || bininfo.SourceName(info.SourceRPM) != "bitest" {
		t.Errorf("sourcerpm = %q", info.SourceRPM)
	}
	if info.SHA256 != hex.EncodeToString(sum[:]) || info.Size != int64(len(data)) || info.Filename != filepath.Base(rpmPath) {
		t.Errorf("file identity wrong: %s %d %s", info.SHA256, info.Size, info.Filename)
	}

	tests := []struct {
		name string
		deps []bininfo.Dep
		want bininfo.Dep
	}{
		{"self provide", info.Provides, bininfo.Dep{Name: "bitest", Op: "=", EVR: "2:1.2-3"}},
		{"explicit provide", info.Provides, bininfo.Dep{Name: "virt", Op: "=", EVR: "1.0"}},
		{"soname require", info.Requires, bininfo.Dep{Name: "libfoo.so.1()(64bit)"}},
		{"versioned require", info.Requires, bininfo.Dep{Name: "bash", Op: ">=", EVR: "4.0"}},
		{"pre require", info.Requires, bininfo.Dep{Name: "/bin/sh", Pre: true}},
		{"rich require", info.Requires, bininfo.Dep{Name: "(foo or bar)"}},
		{"conflict", info.Conflicts, bininfo.Dep{Name: "old", Op: "<", EVR: "1.0"}},
		{"obsolete", info.Obsoletes, bininfo.Dep{Name: "older", Op: "<=", EVR: "0.5"}},
		{"recommend", info.Recommends, bininfo.Dep{Name: "rec"}},
		{"suggest", info.Suggests, bininfo.Dep{Name: "sug", Op: ">", EVR: "2"}},
		{"supplement", info.Supplements, bininfo.Dep{Name: "sup"}},
		{"enhance", info.Enhances, bininfo.Dep{Name: "enh", Op: "=", EVR: "1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !hasDep(tc.deps, tc.want) {
				t.Errorf("missing %+v in %+v", tc.want, tc.deps)
			}
		})
	}
}

func TestExtractErrors(t *testing.T) {
	if _, err := exec.LookPath("rpm"); err != nil {
		t.Skip("rpm not available")
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.rpm")
	if err := os.WriteFile(bad, []byte("not an rpm"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := map[string]string{
		"garbage": bad,
		"missing": filepath.Join(dir, "missing.rpm"),
	}
	for name, path := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := bininfo.Extract(context.Background(), path); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	if _, err := bininfo.ExtractAll(context.Background(), []string{bad}); err == nil {
		t.Fatal("ExtractAll must fail on a bad rpm")
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	in := []bininfo.BinInfo{
		{Name: "b", Version: "1", Release: "1", Arch: "noarch", Filename: "b.rpm"},
		{Name: "a", Version: "1", Release: "1", Arch: "x86_64", Filename: "a.rpm",
			Requires: []bininfo.Dep{{Name: "x", Op: ">=", EVR: "1", Pre: true}}},
	}
	data, err := bininfo.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	data2, err := bininfo.Marshal([]bininfo.BinInfo{in[1], in[0]})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(data2) {
		t.Error("Marshal is not deterministic")
	}
	out, err := bininfo.Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Name != "a" || !reflect.DeepEqual(out[0].Requires, in[1].Requires) {
		t.Errorf("round trip mismatch: %+v", out)
	}
	if _, err := bininfo.Unmarshal([]byte("{")); err == nil {
		t.Error("expected decode error")
	}
}

func TestDepString(t *testing.T) {
	tests := []struct {
		dep  bininfo.Dep
		want string
	}{
		{bininfo.Dep{Name: "foo"}, "foo"},
		{bininfo.Dep{Name: "foo", Op: ">=", EVR: "1.0-2"}, "foo >= 1.0-2"},
		{bininfo.Dep{Name: "(a or b)"}, "(a or b)"},
		{bininfo.Dep{Name: "foo", Op: ">="}, "foo"},
	}
	for _, tc := range tests {
		if got := tc.dep.String(); got != tc.want {
			t.Errorf("String() = %q, want %q", got, tc.want)
		}
	}
}

func TestSourceName(t *testing.T) {
	tests := map[string]string{
		"foo-1.0-1.src.rpm":       "foo",
		"foo-bar-1.0-1.1.src.rpm": "foo-bar",
		"foo-1.0-1.nosrc.rpm":     "foo",
		"":                        "",
		"nodash.src.rpm":          "",
	}
	for in, want := range tests {
		if got := bininfo.SourceName(in); got != want {
			t.Errorf("SourceName(%q) = %q, want %q", in, got, want)
		}
	}
}
