package worker

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

func TestKVMSandboxArgs(t *testing.T) {
	tests := []struct {
		name   string
		s      ProcessSandbox
		res    v1.ResourceRequirements
		wantKV map[string]string
	}{
		{"defaults", ProcessSandbox{SandboxMode: "kvm"}, v1.ResourceRequirements{}, map[string]string{
			"--vm-type": "kvm", "--vm-memory": "4096", "--vm-disk-size": "10240", "--vm-user": "qemu",
			"--vm-args": "-sandbox on -net none", "--vm-disk": "/d.img", "--vm-swap": "/d.swap", "--root": "/root", "--cachedir": "/cache",
		}},
		{"job resources", ProcessSandbox{SandboxMode: "kvm"}, v1.ResourceRequirements{MemoryMB: 16384, DiskGB: 40}, map[string]string{
			"--vm-memory": "16384", "--vm-disk-size": "40960",
		}},
		{"configured user and vm args", ProcessSandbox{SandboxMode: "kvm", KVMUser: "builder", KVMVMArgs: "-net none"}, v1.ResourceRequirements{}, map[string]string{
			"--vm-user": "builder", "--vm-args": "-net none",
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := tc.s.sandboxArgs(&v1.BuildJob{Resources: tc.res}, "/root", "/d.img", "/d.swap", "/cache")
			for flag, want := range tc.wantKV {
				i := slices.Index(args, flag)
				if i < 0 || i+1 >= len(args) || args[i+1] != want {
					t.Errorf("%s: got %v, want value %q", flag, args, want)
				}
			}
			for _, bad := range []string{"--vm-kernel", "--vm-initrd"} {
				if slices.Contains(args, bad) {
					t.Errorf("%s must not be passed: obs-build takes the kernel from the vminstall packages of the build root", bad)
				}
			}
		})
	}
}

// TestKVMWorkersDoNotCollide checks that two workers with separate
// directories never share a VM disk, a job directory or a build root.
func TestKVMWorkersDoNotCollide(t *testing.T) {
	base := t.TempDir()
	mk := func(name string) *ProcessSandbox {
		root := filepath.Join(base, name)
		return &ProcessSandbox{SandboxMode: "kvm", WorkDir: root, DiskDir: filepath.Join(root, "disks")}
	}
	a, b := mk("a"), mk("b")
	job := &v1.BuildJob{JobID: "11111111-2222-3333-4444-555555555555"}
	da, sa, err := a.setupKVMDisks(job)
	if err != nil {
		t.Fatal(err)
	}
	db, sb, err := b.setupKVMDisks(job)
	if err != nil {
		t.Fatal(err)
	}
	if da == db || sa == sb {
		t.Fatalf("workers share VM disks: %s %s", da, db)
	}
	for _, p := range []string{da, sa} {
		if !strings.HasPrefix(p, filepath.Join(base, "a")) {
			t.Errorf("disk %s outside the worker directory", p)
		}
	}
	rootA := jobBuildRoot(filepath.Join(a.WorkDir, "jobs", job.JobID))
	rootB := jobBuildRoot(filepath.Join(b.WorkDir, "jobs", job.JobID))
	if rootA == rootB {
		t.Fatalf("workers share build root %s", rootA)
	}
}

func TestWorkerConfigsAreDisjoint(t *testing.T) {
	kvm, err := LoadConfigFile("../../config/worker-kvm.yaml")
	if err != nil {
		t.Fatal(err)
	}
	nspawn, err := LoadConfigFile("../../config/worker-nspawn.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if kvm.Worker.ID == nspawn.Worker.ID {
		t.Errorf("both workers are named %s", kvm.Worker.ID)
	}
	if kvm.Sandbox.AcceptUnconstrained == nil || *kvm.Sandbox.AcceptUnconstrained {
		t.Error("the kvm worker must set accept_unconstrained: false")
	}
	if kvm.Sandbox.Mode != "kvm" {
		t.Errorf("mode = %q", kvm.Sandbox.Mode)
	}
	for _, p := range []struct{ name, a, b string }{
		{"work_dir", kvm.Worker.WorkDir, nspawn.Worker.WorkDir},
		{"state_dir", kvm.Worker.StateDir, nspawn.Worker.StateDir},
		{"cache_dir", kvm.Worker.CacheDir, nspawn.Worker.CacheDir},
	} {
		if p.a == p.b || strings.HasPrefix(p.a, p.b+"/") || strings.HasPrefix(p.b, p.a+"/") {
			t.Errorf("%s of the two workers overlap: %s and %s", p.name, p.a, p.b)
		}
	}
	if d := kvm.Worker.DiskDir; !strings.HasPrefix(d, kvm.Worker.WorkDir+"/") {
		t.Errorf("disk_dir %s is outside work_dir", d)
	}
}

func TestSandboxTypeOf(t *testing.T) {
	for mode, want := range map[string]string{"kvm": "kvm", "nspawn": "nspawn", "obs-build": "chroot", "rpmbuild": "rpmbuild"} {
		if got := SandboxTypeOf(mode); got != want {
			t.Errorf("SandboxTypeOf(%q) = %q, want %q", mode, got, want)
		}
	}
}
