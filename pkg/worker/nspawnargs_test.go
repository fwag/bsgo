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
	got := s.sandboxArgs(&v1.BuildJob{}, "/jobs/a/root-a", "", "", "/cache")
	want := []string{"--clean", "--changelog", "--vm-type", "nspawn", "--root", "/jobs/a/root-a", "--cachedir", "/cache",
		"--vm-args", `'--hostname=h' '--bind-ro=/a b:/c' '--x=it'\''s' '--bind=/jobs/a/tmp:/tmp'`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestNspawnVMArgsTmp(t *testing.T) {
	bind := "--bind=/jobs/a/tmp:/tmp"
	tests := []struct {
		name       string
		configured []string
		root       string
		want       []string
	}{
		{"default binds the root tmp", nil, "/jobs/a/root-a", []string{bind}},
		{"added after configured", []string{"--hostname=h"}, "/jobs/a/root-a", []string{"--hostname=h", bind}},
		{"colon in path escaped", nil, "/jobs/a:b/root-a", []string{`--bind=/jobs/a\:b/tmp:/tmp`}},
		{"own bind wins", []string{"--bind=/x:/tmp"}, "/jobs/a/root-a", []string{"--bind=/x:/tmp"}},
		{"own bind with options wins", []string{"--bind=/x:/tmp:norbind"}, "/jobs/a/root-a", []string{"--bind=/x:/tmp:norbind"}},
		{"own bind with trailing slash wins", []string{"--bind=/x:/tmp/"}, "/jobs/a/root-a", []string{"--bind=/x:/tmp/"}},
		{"own tmpfs with trailing slash wins", []string{"--tmpfs=/tmp/"}, "/jobs/a/root-a", []string{"--tmpfs=/tmp/"}},
		{"own tmpfs wins", []string{"--tmpfs=/tmp:size=20G"}, "/jobs/a/root-a", []string{"--tmpfs=/tmp:size=20G"}},
		{"other tmp paths do not count", []string{"--bind=/x:/var/tmp", "--tmpfs=/tmpx"}, "/jobs/a/root-a",
			[]string{"--bind=/x:/var/tmp", "--tmpfs=/tmpx", bind}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			orig := append([]string(nil), tc.configured...)
			got := nspawnVMArgs(tc.configured, tc.root)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
			if !reflect.DeepEqual(tc.configured, orig) && len(orig) > 0 {
				t.Errorf("configured arguments modified: %v", tc.configured)
			}
		})
	}
}

func TestPrepareNspawnTmp(t *testing.T) {
	jobDir := t.TempDir()
	root := filepath.Join(jobDir, "root-x")
	if err := prepareNspawnTmp(root); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(filepath.Join(jobDir, "tmp"))
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSticky == 0 || fi.Mode().Perm() != 0o777 {
		t.Fatalf("want a sticky 1777 directory, got %v, %v", fi, err)
	}
	if err := prepareNspawnTmp(root); err == nil {
		t.Error("an existing directory must be refused")
	}
	other := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(other, "tmp")); err != nil {
		t.Fatal(err)
	}
	if err := prepareNspawnTmp(filepath.Join(other, "root-y")); err == nil {
		t.Error("a symlink must be refused")
	}
}
