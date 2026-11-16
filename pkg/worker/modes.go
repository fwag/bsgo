package worker

import (
	"fmt"
	"os/exec"
)

// ValidateSandboxMode fails fast when the host cannot run the configured
// sandbox mode. lookPath is exec.LookPath in production.
func ValidateSandboxMode(mode string, lookPath func(string) (string, error)) error {
	if mode != "nspawn" {
		return nil
	}
	if _, err := lookPath("systemd-nspawn"); err != nil {
		return fmt.Errorf("sandbox mode nspawn requires systemd-nspawn in PATH: %w", err)
	}
	return nil
}

// CheckSandboxMode validates mode against the real PATH.
func CheckSandboxMode(mode string) error {
	return ValidateSandboxMode(mode, exec.LookPath)
}

// SandboxTypeOf maps a worker sandbox mode to the sandbox type it advertises
// and job constraints match against, using the OBS names: the plain obs-build
// chroot is "chroot", kvm and nspawn keep their names.
func SandboxTypeOf(mode string) string {
	if mode == "obs-build" {
		return "chroot"
	}
	return mode
}
