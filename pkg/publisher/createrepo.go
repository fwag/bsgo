package publisher

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type CreateRepoOptions struct {
	Dir            string
	OutputDir      string
	OldMetadataDir string
	CacheDir       string
	Update         bool
	RecyclePkgList bool
}

type CreateRepoExecutor interface {
	Run(ctx context.Context, opts CreateRepoOptions) error
}

type RealCreateRepoExecutor struct {
	BinPath string
}

func NewRealCreateRepoExecutor() *RealCreateRepoExecutor {
	return &RealCreateRepoExecutor{
		BinPath: "createrepo_c",
	}
}

func (e *RealCreateRepoExecutor) Run(ctx context.Context, opts CreateRepoOptions) error {
	args := []string{
		"--no-database",
		"--unique-md-filenames",
	}

	if opts.OutputDir != "" {
		args = append(args, "--outputdir="+opts.OutputDir)
	}
	if opts.Update {
		args = append(args, "--update")
	}
	if opts.OldMetadataDir != "" {
		args = append(args, "--update-md-path="+opts.OldMetadataDir)
	}
	if opts.RecyclePkgList {
		args = append(args, "--recycle-pkglist")
	}
	if opts.CacheDir != "" {
		args = append(args, "--cachedir="+opts.CacheDir)
	}

	args = append(args, "--", opts.Dir)

	cmd := exec.CommandContext(ctx, e.BinPath, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("createrepo_c failed: %s: %w", strings.TrimSpace(string(out)), err)
	}

	return nil
}
