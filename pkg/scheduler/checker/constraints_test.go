package checker

import (
	"reflect"
	"testing"

	"github.com/openSUSE/bsgo/pkg/buildconfig"
	"github.com/openSUSE/bsgo/pkg/constraints"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/recipe"
	"github.com/openSUSE/bsgo/pkg/recipe/fetch"
)

func TestResolveConstraints(t *testing.T) {
	const xml = `<constraints><sandbox>kvm</sandbox>` +
		`<overwrite><conditions><package>util-linux</package></conditions><hardware><processors>4</processors></hardware></overwrite></constraints>`
	tests := []struct {
		name     string
		files    fetch.Files
		recipe   []string
		prjconf  []string
		override string
		pkg      string
		sandbox  []constraints.Match
		procs    int
		wantErr  bool
	}{
		{name: "nothing", pkg: "bash"},
		{name: "prjconf", prjconf: []string{"sandbox kvm"}, pkg: "bash", sandbox: []constraints.Match{{Value: "kvm"}}},
		{name: "constraints file", files: fetch.Files{"_constraints": []byte(xml)}, pkg: "util-linux",
			sandbox: []constraints.Match{{Value: "kvm"}}, procs: 4},
		{name: "overwrite by package name", files: fetch.Files{"_constraints": []byte(xml)}, pkg: "bash",
			sandbox: []constraints.Match{{Value: "kvm"}}},
		{name: "manifest override replaces", files: fetch.Files{"_constraints": []byte(xml)}, prjconf: []string{"sandbox xen"},
			override: "none", pkg: "bash"},
		{name: "manifest override", recipe: []string{"sandbox xen"}, override: "kvm", pkg: "bash",
			sandbox: []constraints.Match{{Value: "kvm"}}},
		{name: "bad file", files: fetch.Files{"_constraints": []byte("<")}, pkg: "bash", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ps := &pass{prpa: Prpa{Arch: "x86_64"}, sched: &buildconfig.Config{Constraint: tc.prjconf}}
			u := &unit{name: tc.pkg, info: &recipe.Info{Constraints: tc.recipe},
				manifest: &manifest.PackageManifest{Build: manifest.PackageBuild{Constraints: manifest.BuildConstraints{Sandbox: tc.override}}}}
			err := ps.resolveConstraints(u, tc.files)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if !reflect.DeepEqual(u.constraints.Sandbox, tc.sandbox) || u.constraints.Hardware.Processors != tc.procs {
				t.Fatalf("got %+v", u.constraints)
			}
		})
	}
}

func TestScheduledDetails(t *testing.T) {
	tests := []struct {
		name string
		in   []constraints.Match
		want string
	}{
		{"unconstrained", nil, "new build"},
		{"kvm", []constraints.Match{{Value: "kvm"}}, "new build, needs sandbox kvm"},
		{"secure not chroot", []constraints.Match{{Value: "secure"}, {Value: "chroot", Exclude: true}},
			"new build, needs sandbox secure and not chroot"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u := &unit{constraints: constraints.Constraints{Sandbox: tc.in}}
			if got := scheduledDetails(u, "new build"); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
