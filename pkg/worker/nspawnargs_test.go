package worker

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

func TestExpandNspawnArgs(t *testing.T) {
	env := nspawnEnv{hostname: "w1", procFilesystems: "/w/proc-filesystems"}
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"defaults", DefaultNspawnArgs(), []string{
			"--hostname=w1", "--capability=CAP_IPC_LOCK", "--rlimit=RLIMIT_MEMLOCK=infinity",
			"--bind-ro=/w/proc-filesystems:/proc/filesystems",
		}},
		{"subset", []string{"--hostname={hostname}"}, []string{"--hostname=w1"}},
		{"literal", []string{"--bind=/a:/b"}, []string{"--bind=/a:/b"}},
		{"empty", []string{}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := expandNspawnArgs(tt.in, env); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFilterProcFilesystems(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"drops selinuxfs", "nodev\tsysfs\nnodev\tselinuxfs\n\text4\n", "nodev\tsysfs\n\text4\n"},
		{"keeps others", "nodev\tproc\n", "nodev\tproc\n"},
		{"no trailing newline", "nodev\tselinuxfs", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(filterProcFilesystems([]byte(tt.in))); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPrepareNspawnArgs(t *testing.T) {
	dir := t.TempDir()
	got, err := PrepareNspawnArgs(nil, dir)
	if err != nil {
		t.Fatal(err)
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0] != "--hostname="+host {
		t.Fatalf("args = %v", got)
	}
	file := filepath.Join(dir, procFilesystemsName)
	if got[3] != "--bind-ro="+file+":/proc/filesystems" {
		t.Errorf("bind arg = %q", got[3])
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "selinuxfs") {
		t.Error("filtered file still lists selinuxfs")
	}

	custom, err := PrepareNspawnArgs([]string{"--capability=CAP_IPC_LOCK"}, t.TempDir())
	if err != nil || !reflect.DeepEqual(custom, []string{"--capability=CAP_IPC_LOCK"}) {
		t.Errorf("configured list must replace the defaults: %v, %v", custom, err)
	}
	none, err := PrepareNspawnArgs([]string{}, t.TempDir())
	if err != nil || len(none) != 0 {
		t.Errorf("empty list must disable all defaults: %v, %v", none, err)
	}
}

func TestSandboxArgsNspawnVMArgs(t *testing.T) {
	s := &ProcessSandbox{SandboxMode: "nspawn", NspawnArgs: []string{"--hostname=h", "--bind-ro=/a b:/c", "--x=it's"}}
	got := s.sandboxArgs(&v1.BuildJob{}, "/root", "", "", "/cache")
	want := []string{"--clean", "--changelog", "--vm-type", "nspawn", "--root", "/root", "--cachedir", "/cache",
		"--vm-args", `'--hostname=h' '--bind-ro=/a b:/c' '--x=it'\''s'`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestRenderRPMListNonVM(t *testing.T) {
	bi := &v1.BuildInfo{Bdeps: []v1.Bdep{
		{Name: "glibc", Version: "1", Release: "1", Arch: "x86_64", Preinstall: true, VMInstall: true},
		{Name: "kernel-obs-build", Version: "1", Release: "1", Arch: "x86_64", VMInstall: true},
		{Name: "make", Version: "1", Release: "1", Arch: "x86_64"},
	}}
	got := renderRPMList(bi, "/w", false)
	if strings.Contains(got, "kernel-obs-build") || !strings.Contains(got, "vminstall: \n") || !strings.Contains(got, "glibc /w/") {
		t.Errorf("rpmlist = %q", got)
	}
	if vm := renderRPMList(bi, "/w", true); !strings.Contains(vm, "vminstall: glibc kernel-obs-build\n") {
		t.Errorf("vm rpmlist = %q", vm)
	}
}
