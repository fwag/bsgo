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
		}},
		{"proc placeholder", []string{"--bind-ro={proc_filesystems}:/x"}, []string{"--bind-ro=/w/proc-filesystems:/x"}},
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
	if len(got) != 3 || got[0] != "--hostname="+host {
		t.Fatalf("args = %v", got)
	}
	for _, a := range got {
		if strings.Contains(a, "/proc/filesystems") {
			t.Errorf("default args must not bind over /proc (nspawn mounts /proc after binds): %q", a)
		}
	}
	// The filtered file is generated when a configured entry uses it.
	if _, err := PrepareNspawnArgs([]string{"--bind-ro={proc_filesystems}:/x"}, dir); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, procFilesystemsName)
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
