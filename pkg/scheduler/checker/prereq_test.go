package checker_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/storage"
)

// TestPrereqsJoinTheBuildRoot checks the BSSched::BuildJob::create handling
// of recipe prereqs: they are expanded into the build root behind the direct
// dependencies, and a missing one makes the package unresolvable.
func TestPrereqsJoinTheBuildRoot(t *testing.T) {
	tests := []struct {
		name      string
		boot      []string
		prereq    string
		wantState string
		wantBdeps []string
	}{
		{"prereq is installed", []string{"lib", "pre"}, "pre", "", []string{"lib", "pre"}},
		{"missing prereq is unresolvable", []string{"lib"}, "pre", storage.StateUnresolvable, nil},
		{"prereq equal to a build dependency adds nothing", []string{"lib"}, "lib", "", []string{"lib"}},
		{"unprovided file prereq is dropped", []string{"lib"}, "/bin/sh", "", []string{"lib"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var boot []bininfo.BinInfo
			for _, n := range tt.boot {
				boot = append(boot, binary(n, "0"))
			}
			w := newWorld(t, boot)
			content := spec("app", []string{"lib"}) + "%package -n app-sub\nSummary: s\nPreReq: " + tt.prereq + "\n"
			w.addPackage("app", map[string]string{"app.spec": content})
			w.run()
			if tt.wantState != "" {
				w.wantJobs()
				if got := w.state("app").State; got != tt.wantState {
					t.Fatalf("state %s, want %s", got, tt.wantState)
				}
				return
			}
			var got []string
			for _, b := range w.wantJobs("app")[0].Bdeps {
				got = append(got, b.Name)
			}
			sort.Strings(got)
			if !reflect.DeepEqual(got, tt.wantBdeps) {
				t.Errorf("bdeps %v, want %v", got, tt.wantBdeps)
			}
		})
	}
}
