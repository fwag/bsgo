package checker_test

import (
	"testing"

	"github.com/openSUSE/bsgo/pkg/bininfo"
	"github.com/openSUSE/bsgo/pkg/manifest"
)

// A package named in Preinstall, VMinstall and the build dependencies is one
// bdep carrying all flags, like the single rpmlist entry of OBS
// (BSSched::BuildJob::create keeps one entry per name).
func TestBdepsAreOnePerNameWithMergedFlags(t *testing.T) {
	tests := []struct {
		name         string
		pre, vm      []string
		requires     []string
		wantNames    []string
		wantPre      map[string]bool
		wantVMInstal map[string]bool
	}{
		{"preinstall and vminstall of one package", []string{"lua", "base"}, []string{"lua"}, []string{"lib"},
			[]string{"lua", "base", "lib"}, map[string]bool{"lua": true, "base": true}, map[string]bool{"lua": true}},
		{"also a build dependency", []string{"lua"}, []string{"lua"}, []string{"lua", "lib"},
			[]string{"lua", "lib"}, map[string]bool{"lua": true}, map[string]bool{"lua": true}},
		{"vminstall only", nil, []string{"lua"}, []string{"lua"},
			[]string{"lua"}, nil, map[string]bool{"lua": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			boot := []bininfo.BinInfo{binary("lua", "1"), binary("base", "1"), binary("lib", "1")}
			w := newWorld(t, boot, func(w *world) {
				w.prj.PublicConfig = &manifest.ProjectConfig{Preinstall: tt.pre, VMinstall: tt.vm}
			})
			w.addPackage("app", map[string]string{"app.spec": spec("app", tt.requires)})
			w.run()
			bdeps := w.wantJobs("app")[0].Bdeps

			seen := map[string]bool{}
			for _, b := range bdeps {
				if seen[b.Name] {
					t.Errorf("bdep %s listed twice in %+v", b.Name, bdeps)
				}
				seen[b.Name] = true
				if b.Preinstall != tt.wantPre[b.Name] || b.VMInstall != tt.wantVMInstal[b.Name] {
					t.Errorf("%s: preinstall %v vminstall %v", b.Name, b.Preinstall, b.VMInstall)
				}
			}
			if len(bdeps) != len(tt.wantNames) {
				t.Errorf("%d bdeps, want %v", len(bdeps), tt.wantNames)
			}
			for i, n := range tt.wantNames {
				if i < len(bdeps) && bdeps[i].Name != n {
					t.Errorf("bdep %d is %s, want %s (preinstalls, vminstalls, then the rest)", i, bdeps[i].Name, n)
				}
			}
		})
	}
}
