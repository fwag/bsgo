package signer

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// ObsSignDriver invokes the 'sign' binary which connects to obs-signd and Thales Luna HSM appliances.
type ObsSignDriver struct {
	signBin string
}

func NewObsSignDriver(signBin string) *ObsSignDriver {
	if signBin == "" {
		signBin = "/usr/bin/sign"
	}
	return &ObsSignDriver{
		signBin: filepath.Clean(signBin),
	}
}

func (d *ObsSignDriver) buildBaseArgs(opts SignOptions) []string {
	var args []string
	if opts.Project != "" {
		args = append(args, "--project", opts.Project)
	}
	if opts.Package != "" {
		args = append(args, "--package", opts.Package)
	}
	if opts.SignFlavor != "" {
		args = append(args, "--signflavor", opts.SignFlavor)
	}
	if opts.SignType != "" {
		args = append(args, "--signtype", opts.SignType)
	}
	if opts.KeyFile != "" {
		args = append(args, "-P", opts.KeyFile)
	}
	return args
}

// PublicKey exports the public key from the obs-signd / Thales Luna HSM partition.
func (d *ObsSignDriver) PublicKey(ctx context.Context, opts SignOptions) ([]byte, error) {
	args := append(d.buildBaseArgs(opts), "-p")
	// #nosec G204 -- parameterized invocation of sign binary
	cmd := exec.CommandContext(ctx, d.signBin, args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("sign -p failed: %w", err)
	}
	return out, nil
}

// SignDetached computes a detached signature over data using obs-signd.
func (d *ObsSignDriver) SignDetached(ctx context.Context, data []byte, opts SignOptions) ([]byte, error) {
	hashAlgo := opts.HashAlgo
	if hashAlgo == "" {
		hashAlgo = "sha256"
	}
	args := append(d.buildBaseArgs(opts), "-O", "-h", hashAlgo)

	// #nosec G204 -- parameterized invocation of sign binary
	cmd := exec.CommandContext(ctx, d.signBin, args...)
	cmd.Stdin = bytes.NewReader(data)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("sign -O failed: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return stdout.Bytes(), nil
}

// SignClearsign generates a cleartext-signed document using obs-signd.
func (d *ObsSignDriver) SignClearsign(ctx context.Context, data []byte, opts SignOptions) ([]byte, error) {
	args := append(d.buildBaseArgs(opts), "-c")

	// #nosec G204 -- parameterized invocation of sign binary
	cmd := exec.CommandContext(ctx, d.signBin, args...)
	cmd.Stdin = bytes.NewReader(data)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("sign -c failed: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return stdout.Bytes(), nil
}

// SignRPM signs an RPM file in-place using obs-signd.
func (d *ObsSignDriver) SignRPM(ctx context.Context, rpmPath string, opts SignOptions) error {
	cleanPath := filepath.Clean(rpmPath)
	args := append(d.buildBaseArgs(opts), "-r", cleanPath)

	// #nosec G204 -- parameterized invocation of sign binary
	cmd := exec.CommandContext(ctx, d.signBin, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sign -r failed on %s: %s: %w", cleanPath, strings.TrimSpace(string(out)), err)
	}
	return nil
}
