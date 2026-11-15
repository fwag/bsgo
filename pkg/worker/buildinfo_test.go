package worker

import (
	"slices"
	"strings"
	"testing"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

func fixtureBuildInfo() *v1.BuildInfo {
	return &v1.BuildInfo{
		Config:       "Macros:\n%is_opensuse 1\n:Macros\n",
		ArchPath:     "x86_64:i686:i586:noarch",
		Release:      "1.2",
		DistURL:      "obs://build.example/Proj/repo/abc-pkg",
		Flavor:       "gcc13",
		ObsPackage:   "gcc",
		SourceCommit: strings.Repeat("a", 40),
		Meta:         "meta-digest",
		Bdeps: []v1.Bdep{
			{Name: "filesystem", Version: "84.87", Release: "1.1", Arch: "x86_64", Preinstall: true, RunScripts: true},
			{Name: "glibc", Version: "2.38", Release: "3.2", Arch: "x86_64", Preinstall: true, RunScripts: true, VMInstall: true},
			{Name: "bash", Version: "5.2", Release: "1.1", Arch: "x86_64", Preinstall: true},
			{Name: "gcc13", Version: "13.2", Release: "1.1", Arch: "x86_64", NoInstall: true},
			{Name: "texinfo", Version: "7.0", Release: "1.1", Arch: "x86_64", InstallOnly: true},
			{Name: "rpm-build", Version: "4.18", Release: "2.1", Arch: "x86_64", NotMeta: true},
		},
	}
}

// TestRenderRPMListGolden pins the rpmlist to what bs_worker writes
// (src/backend/bs_worker, "build rpmlist for build script"):
//
//	for each bdep:           push "$bin $pkgdir/$bin.$suffix"      -> "<name> <path>"
//	push "preinstall: "  . join(' ', names of bdeps with preinstall)
//	push "vminstall: "   . join(' ', names of bdeps with vminstall)
//	push "runscripts: "  . join(' ', names of bdeps with runscripts)
//	push "noinstall: "   . join(' ', names of bdeps with noinstall)   (not in image mode)
//	push "installonly: " . join(' ', names of bdeps with installonly) (not in image mode)
//	writestr(".build.rpmlist", join("\n", @rpmlist) . "\n")
//
// bs_worker writes no "dist:" line and notmeta never reaches the rpmlist
// (it only filters the meta digest), so neither appears below. Empty flag
// lines keep their trailing space.
func TestRenderRPMListGolden(t *testing.T) {
	want := `filesystem /w/bdeps/filesystem-84.87-1.1.x86_64.rpm
glibc /w/bdeps/glibc-2.38-3.2.x86_64.rpm
bash /w/bdeps/bash-5.2-1.1.x86_64.rpm
gcc13 /w/bdeps/gcc13-13.2-1.1.x86_64.rpm
texinfo /w/bdeps/texinfo-7.0-1.1.x86_64.rpm
rpm-build /w/bdeps/rpm-build-4.18-2.1.x86_64.rpm
preinstall: filesystem glibc bash
vminstall: glibc
runscripts: filesystem glibc
noinstall: gcc13
installonly: texinfo
`
	if got := renderRPMList(fixtureBuildInfo(), "/w/bdeps"); got != want {
		t.Errorf("rpmlist mismatch\n got:\n%s\nwant:\n%s", got, want)
	}

	empty := renderRPMList(&v1.BuildInfo{}, "/w/bdeps")
	wantEmpty := "preinstall: \nvminstall: \nrunscripts: \nnoinstall: \ninstallonly: \n"
	if empty != wantEmpty {
		t.Errorf("empty rpmlist = %q, want %q", empty, wantEmpty)
	}
}

func TestHermeticArgs(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*v1.BuildJob)
		want    []string
		without []string
	}{
		{
			name:   "full",
			mutate: func(*v1.BuildJob) {},
			want: []string{
				"--dist", "/c", "--rpmlist", "/r", "--arch", "x86_64:i686:i586:noarch", "--jobs", "6", "--statistics",
				"--release", "1.2", "--disturl", "obs://build.example/Proj/repo/abc-pkg",
				"--buildflavor=gcc13", "--obspackage=gcc", "/s/pkg.spec",
			},
		},
		{
			name: "minimal falls back to vcpus and omits optional flags",
			mutate: func(j *v1.BuildJob) {
				j.BuildInfo.Release, j.BuildInfo.DistURL, j.BuildInfo.Flavor, j.BuildInfo.ObsPackage = "", "", "", ""
				j.BuildInfo.Jobs = 0
				j.Resources.VCPUs = 3
			},
			want: []string{
				"--dist", "/c", "--rpmlist", "/r", "--arch", "x86_64:i686:i586:noarch", "--jobs", "3", "--statistics", "/s/pkg.spec",
			},
			without: []string{"--release", "--disturl", "--buildflavor=gcc13", "--obspackage=gcc"},
		},
		{
			name:   "explicit jobs win",
			mutate: func(j *v1.BuildJob) { j.BuildInfo.Jobs = 12 },
			want: []string{
				"--dist", "/c", "--rpmlist", "/r", "--arch", "x86_64:i686:i586:noarch", "--jobs", "12", "--statistics",
				"--release", "1.2", "--disturl", "obs://build.example/Proj/repo/abc-pkg",
				"--buildflavor=gcc13", "--obspackage=gcc", "/s/pkg.spec",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			job := &v1.BuildJob{BuildInfo: fixtureBuildInfo(), Resources: v1.ResourceRequirements{VCPUs: 6}}
			job.BuildInfo.Jobs = 6
			tc.mutate(job)
			got := hermeticArgs(job, "/c", "/r", "/s/pkg.spec")
			if !slices.Equal(got, tc.want) {
				t.Errorf("args = %v\nwant   %v", got, tc.want)
			}
			for _, bad := range append(tc.without, "--repo", "--logidlelimit") {
				if slices.Contains(got, bad) {
					t.Errorf("unexpected %s in %v", bad, got)
				}
			}
		})
	}
}

// TestBuildArgsForHermeticHasNoRepo checks the complete command line, VM and
// chroot flavours: no --repo, KVM stays on "-net none".
func TestBuildArgsForHermeticHasNoRepo(t *testing.T) {
	for _, mode := range []string{"kvm", "obs-build"} {
		t.Run(mode, func(t *testing.T) {
			s := &ProcessSandbox{SandboxMode: mode, WorkDir: t.TempDir()}
			job := &v1.BuildJob{BuildInfo: fixtureBuildInfo(), Resources: v1.ResourceRequirements{VCPUs: 2}}
			args := s.sandboxArgs(job, "/root", "/d.img", "/d.swap", "/cache")
			args = append(args, hermeticArgs(job, "/c", "/r", "/s.spec")...)
			if slices.Contains(args, "--repo") {
				t.Fatalf("--repo present in %v", args)
			}
			joined := strings.Join(args, " ")
			if mode == "kvm" && !strings.Contains(joined, "--vm-args -sandbox on -net none") {
				t.Errorf("kvm args lost -net none: %s", joined)
			}
			if mode == "obs-build" && strings.Contains(joined, "--vm-type") {
				t.Errorf("chroot args contain vm options: %s", joined)
			}
		})
	}
}

func TestCheckHermeticAllowed(t *testing.T) {
	tests := []struct {
		name    string
		sandbox *ProcessSandbox
		engine  string
		wantErr bool
	}{
		{"kvm", &ProcessSandbox{SandboxMode: "kvm"}, "", false},
		{"chroot denied by default", &ProcessSandbox{SandboxMode: "obs-build"}, "", true},
		{"chroot explicitly allowed", &ProcessSandbox{SandboxMode: "obs-build", AllowUnisolatedChroot: true}, "", false},
		{"host rpmbuild path denied", &ProcessSandbox{}, "", true},
		{"podman engine unaffected", &ProcessSandbox{SandboxMode: "obs-build"}, "podman", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			job := &v1.BuildJob{BuildEngine: tc.engine, BuildInfo: &v1.BuildInfo{}}
			if err := tc.sandbox.checkHermeticAllowed(job); (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateBuildInfo(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*v1.BuildJob)
		wantErr bool
	}{
		{"valid", func(*v1.BuildJob) {}, false},
		{"no config", func(j *v1.BuildJob) { j.BuildInfo.Config = "" }, true},
		{"no archpath", func(j *v1.BuildJob) { j.BuildInfo.ArchPath = "" }, true},
		{"short commit", func(j *v1.BuildJob) { j.BuildInfo.SourceCommit = "abc123" }, true},
		{"branch as commit", func(j *v1.BuildJob) { j.BuildInfo.SourceCommit = "main" }, true},
		{"no git url", func(j *v1.BuildJob) { j.Source.GitURL = "" }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			job := &v1.BuildJob{BuildInfo: fixtureBuildInfo(), Source: v1.SourceSpec{GitURL: "https://example.com/r.git"}}
			tc.mutate(job)
			if err := validateBuildInfo(job); (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestArtifactPrefix(t *testing.T) {
	tests := []struct {
		name string
		job  *v1.BuildJob
		want string
	}{
		{"legacy", &v1.BuildJob{Project: "P", Package: "pkg"}, "artifacts/P/tgt/arc/pkg/"},
		{"no flavor", &v1.BuildJob{Project: "P", Package: "pkg", BuildInfo: &v1.BuildInfo{}}, "artifacts/P/tgt/arc/pkg/"},
		{"flavor", &v1.BuildJob{Project: "P", Package: "gcc", BuildInfo: &v1.BuildInfo{Flavor: "13"}}, "artifacts/P/tgt/arc/gcc:13/"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := artifactPrefix(tc.job, "tgt", "arc"); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}
