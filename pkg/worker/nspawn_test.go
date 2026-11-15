package worker

import (
	"errors"
	"reflect"
	"testing"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

func TestSandboxArgsNspawn(t *testing.T) {
	s := &ProcessSandbox{SandboxMode: "nspawn"}
	job := &v1.BuildJob{Resources: v1.ResourceRequirements{MemoryMB: 8192, DiskGB: 40, VCPUs: 4}}
	got := s.sandboxArgs(job, "/root", "/d.img", "/d.swap", "/cache")
	want := []string{"--clean", "--changelog", "--vm-type", "nspawn", "--root", "/root", "--cachedir", "/cache"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestCheckHermeticAllowedNspawn(t *testing.T) {
	job := &v1.BuildJob{BuildInfo: &v1.BuildInfo{}}
	s := &ProcessSandbox{SandboxMode: "nspawn"}
	if err := s.checkHermeticAllowed(job); err != nil {
		t.Fatalf("nspawn must be accepted without allow_unisolated_chroot: %v", err)
	}
}

func TestValidateSandboxMode(t *testing.T) {
	found := func(string) (string, error) { return "/usr/bin/systemd-nspawn", nil }
	missing := func(string) (string, error) { return "", errors.New("not found") }
	tests := []struct {
		name     string
		mode     string
		lookPath func(string) (string, error)
		wantErr  bool
	}{
		{"nspawn present", "nspawn", found, false},
		{"nspawn missing", "nspawn", missing, true},
		{"kvm needs no nspawn", "kvm", missing, false},
		{"obs-build needs no nspawn", "obs-build", missing, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateSandboxMode(tc.mode, tc.lookPath); (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestGrantResources(t *testing.T) {
	total := v1.WorkerHardware{TotalVCPUs: 4, TotalMemoryMB: 8192}
	tests := []struct {
		name      string
		req       v1.ResourceRequirements
		wantVCPUs int32
		wantMemMB int64
	}{
		{"within capacity", v1.ResourceRequirements{VCPUs: 2, MemoryMB: 4096}, 2, 4096},
		{"above capacity", v1.ResourceRequirements{VCPUs: 16, MemoryMB: 99999}, 4, 8192},
		{"unset gets defaults", v1.ResourceRequirements{}, 1, 2048},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, m := grantResources(tc.req, total)
			if v != tc.wantVCPUs || m != tc.wantMemMB {
				t.Fatalf("got %d vcpus %d MiB, want %d / %d", v, m, tc.wantVCPUs, tc.wantMemMB)
			}
		})
	}
}
