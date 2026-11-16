package main

import (
	"testing"

	"github.com/openSUSE/bsgo/pkg/worker"
)

func TestApplyFileConfigSandbox(t *testing.T) {
	no, yes := false, true
	tests := []struct {
		name            string
		mode            string
		accept          *bool
		wantType        string
		wantConstrained bool
	}{
		{"kvm only constrained", "kvm", &no, "kvm", true},
		{"kvm accepts all by default", "kvm", nil, "kvm", false},
		{"kvm explicit accept", "kvm", &yes, "kvm", false},
		{"chroot is named like OBS", "obs-build", nil, "chroot", false},
		{"nspawn", "nspawn", nil, "nspawn", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var file worker.WorkerConfigFile
			file.Sandbox.Mode = tc.mode
			file.Sandbox.AcceptUnconstrained = tc.accept
			cfg := worker.WorkerConfig{}
			applyFileConfig(&cfg, &file)
			if cfg.Hardware.SandboxType != tc.wantType || cfg.ConstrainedOnly != tc.wantConstrained || cfg.SandboxMode != tc.mode {
				t.Errorf("type=%q constrainedOnly=%v mode=%q", cfg.Hardware.SandboxType, cfg.ConstrainedOnly, cfg.SandboxMode)
			}
		})
	}
}
