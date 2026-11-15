package checker_test

import (
	"testing"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// A per-architecture build flag restricts one architecture only.
func TestArchConfigOnlybuildAppliesToItsArchOnly(t *testing.T) {
	for _, tc := range []struct {
		arch      string
		wantState string
		details   string
	}{
		{"i586", storage.StateExcluded, "package whitelist"},
		{"x86_64", storage.StateScheduled, ""},
	} {
		t.Run(tc.arch, func(t *testing.T) {
			w := newWorld(t, nil, func(w *world) {
				tgt := w.prj.Targets[testRepo]
				tgt.Architectures = []string{"x86_64", "i586"}
				w.prj.Targets[testRepo] = tgt
				w.prj.ArchConfig = map[string]*manifest.ProjectConfig{
					"i586": {BuildFlags: []string{"onlybuild:glibc"}},
				}
				w.prpa.Arch = tc.arch
			})
			w.pkg("other", nil)
			w.run()
			if st := w.state("other"); st.State != tc.wantState || (tc.details != "" && st.Details != tc.details) {
				t.Errorf("other on %s: %s %q, want %s %q", tc.arch, st.State, st.Details, tc.wantState, tc.details)
			}
		})
	}
}

// Binaries imported from another architecture have no producing unit: they
// satisfy dependencies, are never scheduled, and do not make the importing
// unit blocked.
func TestImportUnitSatisfiesDependenciesWithoutProducer(t *testing.T) {
	w := newWorld(t, nil)
	w.pkg("glibc", nil)
	w.pkg("app", []string{"glibc-32bit"})
	// glibc-32bit arrives from the i586 build as an import unit.
	imp := binary("glibc-32bit", "1")
	w.bins[buildrepo.ImportUnit("glibc", "i586")] = []bininfo.BinInfo{imp}
	w.publishIndex()
	w.run()
	w.wantJobs("app", "glibc")
	if st := w.state("app"); st.State != storage.StateScheduled {
		t.Errorf("app: %+v, want scheduled", st)
	}
}

// Without the import the dependency is unresolvable and nothing blocks on
// a cross-architecture producer.
func TestMissingBaselibsIsUnresolvable(t *testing.T) {
	w := newWorld(t, nil)
	w.pkg("glibc", nil)
	w.pkg("app", []string{"glibc-32bit"})
	w.run()
	w.wantJobs("glibc")
	if st := w.state("app"); st.State == storage.StateScheduled {
		t.Errorf("app scheduled without glibc-32bit: %+v", st)
	}
}
