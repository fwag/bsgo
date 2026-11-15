package indexer_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/buildconfig"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/scheduler/indexer"
)

// slfoExports routes with the real SLFO 1.2 project configuration.
type slfoExports struct {
	text string
	arch []string
}

func (s slfoExports) Exports(_ context.Context, p buildrepo.Prpa) (indexer.ExportPolicy, error) {
	cfg, err := buildconfig.Parse(s.text, p.Arch)
	if err != nil {
		return indexer.ExportPolicy{}, err
	}
	r, err := buildconfig.NewExportRouter(cfg.ExportFilter, p.Arch)
	return indexer.ExportPolicy{Router: r, Archs: s.arch}, err
}

func newExportEnv(t *testing.T) *env {
	t.Helper()
	text, err := os.ReadFile("../../../test/fixtures/prjconf/SUSE_SLFO_1.2.prjconf")
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	e.prpa.Arch = "i586"
	e.ix.Exports = slfoExports{text: string(text), arch: []string{"x86_64", "i586"}}
	return e
}

func rpmArch(name, ver, arch string) bininfo.BinInfo {
	b := rpm(name, ver)
	b.Arch = arch
	b.Filename = name + "-" + ver + "-1." + arch + ".rpm"
	return b
}

func (e *env) namesOf(arch string) string {
	e.t.Helper()
	idx, err := e.ix.Reader.Index(context.Background(), buildrepo.Prpa{Project: e.prpa.Project, Repo: e.prpa.Repo, Arch: arch})
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, en := range idx.Entries {
		out = append(out, en.Unit+"="+en.Filename()+"@"+en.Key)
	}
	return strings.Join(out, ",")
}

func TestIntegrateExportsToOtherArch(t *testing.T) {
	e := newExportEnv(t)
	prefix := buildrepo.ArtifactPrefix(e.prpa, "glibc")
	i686 := rpmArch("glibc", "2.40", "i686")
	steps := []struct {
		name      string
		bins      []bininfo.BinInfo
		wantI586  string
		wantX8664 string
		wantEvent int
	}{
		{"baselibs split, ia32 dropped",
			[]bininfo.BinInfo{i686, rpmArch("glibc-32bit", "2.40", "x86_64"), rpmArch("glibc-ia32", "2.40", "i586")},
			"glibc=" + i686.Filename + "@" + prefix + i686.Filename,
			"glibc:import:i586=glibc-32bit-2.40-1.x86_64.rpm@" + prefix + "glibc-32bit-2.40-1.x86_64.rpm", 2},
		{"identical rebuild changes nothing",
			[]bininfo.BinInfo{i686, rpmArch("glibc-32bit", "2.40", "x86_64"), rpmArch("glibc-ia32", "2.40", "i586")},
			"glibc=" + i686.Filename + "@" + prefix + i686.Filename,
			"glibc:import:i586=glibc-32bit-2.40-1.x86_64.rpm@" + prefix + "glibc-32bit-2.40-1.x86_64.rpm", 0},
		{"rebuild replaces the import set",
			[]bininfo.BinInfo{i686, rpmArch("glibc-32bit", "2.41", "x86_64")},
			"glibc=" + i686.Filename + "@" + prefix + i686.Filename,
			"glibc:import:i586=glibc-32bit-2.41-1.x86_64.rpm@" + prefix + "glibc-32bit-2.41-1.x86_64.rpm", 1},
		{"rebuild without baselibs clears the import",
			[]bininfo.BinInfo{i686}, "glibc=" + i686.Filename + "@" + prefix + i686.Filename, "", 1},
	}
	for _, tc := range steps {
		before := len(e.rec.prpas)
		if _, err := e.integrate("glibc", e.upload("glibc", tc.bins...)); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := e.namesOf("i586"); got != tc.wantI586 {
			t.Errorf("%s: i586 index %q, want %q", tc.name, got, tc.wantI586)
		}
		if got := e.namesOf("x86_64"); got != tc.wantX8664 {
			t.Errorf("%s: x86_64 index %q, want %q", tc.name, got, tc.wantX8664)
		}
		if n := len(e.rec.prpas) - before; n != tc.wantEvent {
			t.Errorf("%s: %d repo changed events, want %d", tc.name, n, tc.wantEvent)
		}
	}
}

func TestIntegrateExportKeepsOtherUnitsAndNeverReexports(t *testing.T) {
	e := newExportEnv(t)
	// A native x86_64 unit named like the exporting unit coexists with the import unit.
	native := e.prpa
	native.Arch = "x86_64"
	nb := rpmArch("glibc", "2.40", "x86_64")
	prefix := buildrepo.ArtifactPrefix(native, "glibc")
	data, err := bininfo.Marshal([]bininfo.BinInfo{nb})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.blobs.PutObject(context.Background(), prefix+bininfo.FileName, data); err != nil {
		t.Fatal(err)
	}
	arts := []indexer.Artifact{
		{Filename: bininfo.FileName, S3Key: prefix + bininfo.FileName},
		{Filename: nb.Filename, S3Key: prefix + nb.Filename, SHA256: nb.SHA256, Size: nb.Size},
	}
	if _, err := e.ix.Integrate(context.Background(), indexer.Finished{Prpa: native, Unit: "glibc", Artifacts: arts}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.integrate("glibc", e.upload("glibc", rpmArch("glibc-32bit", "2.40", "x86_64"))); err != nil {
		t.Fatal(err)
	}
	got := e.namesOf("x86_64")
	if !strings.Contains(got, "glibc=glibc-2.40-1.x86_64.rpm") || !strings.Contains(got, "glibc:import:i586=glibc-32bit") {
		t.Errorf("x86_64 index %q lacks the native unit or the import", got)
	}
	// Integrating the native unit again must neither touch nor re-export the import.
	if _, err := e.ix.Integrate(context.Background(), indexer.Finished{Prpa: native, Unit: "glibc", Artifacts: arts}); err != nil {
		t.Fatal(err)
	}
	if got := e.namesOf("i586"); strings.Contains(got, "import") {
		t.Errorf("i586 index %q holds imports", got)
	}
}

func TestIsImportUnit(t *testing.T) {
	for unit, want := range map[string]bool{
		"glibc": false, "gcc:flavor": false,
		buildrepo.ImportUnit("glibc", "i586"): true, buildrepo.ImportUnit("gcc:f", "i586"): true,
	} {
		if buildrepo.IsImportUnit(unit) != want {
			t.Errorf("IsImportUnit(%q) != %v", unit, want)
		}
	}
}
