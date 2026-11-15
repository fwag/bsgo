package solv_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/solv"
)

func TestBuildSolvFileProvides(t *testing.T) {
	infos := []bininfo.BinInfo{
		{Name: "python3-base", Version: "3.11", Release: "1", Arch: "x86_64", Filename: "p.rpm",
			Files: []string{"/usr/bin/python3", "/usr/share/doc/py/README"}},
		{Name: "mta", Version: "1", Release: "1", Arch: "x86_64", Filename: "m.rpm",
			Files: []string{"/usr/lib/sendmail", "/etc/mta.conf", "/var/lib/mta/spool"}},
		{Name: "script", Version: "1", Release: "1", Arch: "noarch", Filename: "s.rpm",
			Requires: []bininfo.Dep{{Name: "/usr/bin/python3"}}},
	}
	blob, err := solv.BuildSolv("x86_64", infos)
	if err != nil {
		t.Fatal(err)
	}
	indexed := provides(t, solv.RepoBlob{Name: "idx", Data: blob})
	tests := []struct {
		entry string
		want  bool
	}{
		{"python3-base: /usr/bin/python3", true},
		{"mta: /usr/lib/sendmail", true},
		{"mta: /etc/mta.conf", true},
		{"python3-base: /usr/share/doc/py/README", false},
		{"mta: /var/lib/mta/spool", false},
	}
	for _, tc := range tests {
		t.Run(tc.entry, func(t *testing.T) {
			if got := slices.Contains(indexed, tc.entry); got != tc.want {
				t.Errorf("indexed = %v, want %v", got, tc.want)
			}
		})
	}

	// A file requirement behaves exactly as with the same packages from
	// primary.xml: file entries are not plain provides (OBS), FileProvides maps them.
	prim := primary(
		tpkg{name: "python3-base", evr: "3.11-1", fil: []string{"/usr/bin/python3"}},
		tpkg{name: "script", evr: "1-1", arch: "noarch", req: []string{"/usr/bin/python3"}},
	)
	cfgs := map[string]*solv.ExpandConfig{
		"plain":        nil,
		"FileProvides": {FileProvides: map[string][]string{"/usr/bin/python3": {"python3-base"}}},
	}
	for name, cfg := range cfgs {
		t.Run("same as primary/"+name, func(t *testing.T) {
			var got []string
			for _, b := range []solv.RepoBlob{{Name: "i", Data: blob}, {Name: "p", Data: prim}} {
				pp, err := solv.NewPathPool("x86_64", []solv.RepoBlob{b})
				if err != nil {
					t.Fatal(err)
				}
				res, err := pp.Expand(cfg, "script")
				pp.Close()
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, sortedPkgNames(res))
			}
			if got[0] != got[1] {
				t.Errorf("bininfo index resolves %q, primary.xml %q", got[0], got[1])
			}
		})
	}
}

const parityFiles = `/usr/bin/tool /usr/sbin/adm /etc/tool.conf /usr/share/doc/README /usr/lib/sendmail /opt/bin/y /usr/bin/sub/z /etc/ftest.d/a /var/bin/v /usr/lib/other`

const paritySpec = `Name: ftest
Version: 1
Release: 1
Summary: f
License: MIT
BuildArch: noarch
Provides: virt = 2
Requires: bash
%description
x
%install
mkdir -p %{buildroot}/usr/bin/sub %{buildroot}/usr/sbin %{buildroot}/etc/ftest.d %{buildroot}/usr/share/doc %{buildroot}/usr/lib %{buildroot}/opt/bin %{buildroot}/var/bin
for f in ` + parityFiles + `; do echo x > %{buildroot}$f; done
%files
%dir /usr/bin/sub
%dir /etc/ftest.d
` + "/usr/bin/tool\n/usr/sbin/adm\n/etc/tool.conf\n/usr/share/doc/README\n/usr/lib/sendmail\n/opt/bin/y\n/usr/bin/sub/z\n/etc/ftest.d/a\n/var/bin/v\n/usr/lib/other\n"

func TestBuildSolvProvidesMatchPrimaryXML(t *testing.T) {
	for _, tool := range []string{"rpm", "rpmbuild", "createrepo_c"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	top := t.TempDir()
	if err := os.MkdirAll(filepath.Join(top, "SPECS"), 0o750); err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(top, "SPECS", "f.spec")
	if err := os.WriteFile(specPath, []byte(paritySpec), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("rpmbuild", "--define", "_topdir "+top, "-bb", specPath).CombinedOutput(); err != nil {
		t.Skipf("rpmbuild failed: %v: %s", err, out)
	}
	repoDir := filepath.Join(top, "RPMS", "noarch")
	if out, err := exec.Command("createrepo_c", "-q", "--general-compress-type=gz", repoDir).CombinedOutput(); err != nil {
		t.Skipf("createrepo_c failed: %v: %s", err, out)
	}
	primaries, err := filepath.Glob(filepath.Join(repoDir, "repodata", "*primary.xml*"))
	if err != nil || len(primaries) != 1 {
		t.Fatalf("primary.xml: %v %v", primaries, err)
	}
	primary, err := os.ReadFile(primaries[0])
	if err != nil {
		t.Fatal(err)
	}
	rpms, _ := filepath.Glob(filepath.Join(repoDir, "*.rpm"))
	info, err := bininfo.Extract(context.Background(), rpms[0])
	if err != nil {
		t.Fatal(err)
	}

	fromPrimary := provides(t, solv.RepoBlob{Name: "primary", Data: primary})
	blob, err := solv.BuildSolv("x86_64", []bininfo.BinInfo{info})
	if err != nil {
		t.Fatal(err)
	}
	fromInfo := provides(t, solv.RepoBlob{Name: "bininfo", Data: blob})
	if !reflect.DeepEqual(fromPrimary, fromInfo) {
		t.Errorf("provides differ\nprimary.xml: %s\nbininfo:     %s",
			strings.Join(fromPrimary, " | "), strings.Join(fromInfo, " | "))
	}
	if len(fromInfo) < 5 {
		t.Errorf("suspiciously few provides: %v", fromInfo)
	}
}

func provides(t *testing.T, b solv.RepoBlob) []string {
	t.Helper()
	pool, err := solv.NewPool("x86_64")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if len(b.Data) >= 4 && string(b.Data[:4]) == "SOLV" {
		_, err = pool.LoadRepoFromMemory(b.Name, b.Data)
	} else {
		_, err = pool.LoadRepoFromRPMMD(b.Name, b.Data)
	}
	if err != nil {
		t.Fatal(err)
	}
	return pool.AllProvides()
}
