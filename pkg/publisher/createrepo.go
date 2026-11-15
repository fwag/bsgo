package publisher

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CreateRepoOptions describes one metadata generation. All paths live in the
// publish temp dir.
type CreateRepoOptions struct {
	// Dir holds the RPM files that are new or changed.
	Dir string
	// OutputDir receives the generated repodata/ directory.
	OutputDir string
	// OldRepoDir, when set, is a directory with the previously published
	// repodata/. Packages of Pkglist that are not in Dir are taken from it
	// without reading the RPM file.
	OldRepoDir string
	// Pkglist is the exact list of package file names of the repository.
	Pkglist []string
}

// CreateRepoExecutor generates rpm-md metadata.
type CreateRepoExecutor interface {
	Run(ctx context.Context, opts CreateRepoOptions) error
}

// RealCreateRepoExecutor calls createrepo_c.
type RealCreateRepoExecutor struct {
	BinPath string
}

// NewRealCreateRepoExecutor returns an executor that runs createrepo_c from
// PATH.
func NewRealCreateRepoExecutor() *RealCreateRepoExecutor {
	return &RealCreateRepoExecutor{BinPath: "createrepo_c"}
}

// Run executes createrepo_c. The package list is passed with --pkglist, so
// packages that exist only in the old metadata need no RPM file
// (--skip-stat); additional metadata of the old repository (updateinfo, ...) is discarded because enrichment regenerates it. The metadata is compressed with gzip, which the publisher
// can read back without third-party decompressors, and checksummed with
// sha256, the identity of a binary in bsgo.
func (e *RealCreateRepoExecutor) Run(ctx context.Context, opts CreateRepoOptions) error {
	if err := os.MkdirAll(opts.OutputDir, 0o750); err != nil {
		return fmt.Errorf("creating createrepo output dir: %w", err)
	}
	listFile := filepath.Join(opts.OutputDir, "pkglist.txt")
	list := strings.Join(opts.Pkglist, "\n")
	if list != "" {
		list += "\n"
	}
	if err := os.WriteFile(listFile, []byte(list), 0o600); err != nil {
		return fmt.Errorf("writing pkglist: %w", err)
	}
	args := []string{
		"--no-database",
		"--unique-md-filenames",
		"--checksum=sha256",
		"--general-compress-type=gz",
		"--pkglist=" + listFile,
		"--outputdir=" + opts.OutputDir,
	}
	if opts.OldRepoDir != "" {
		args = append(args, "--update", "--skip-stat", "--discard-additional-metadata", "--update-md-path="+opts.OldRepoDir)
	}
	args = append(args, "--", opts.Dir)

	// #nosec G204 -- the binary path is configuration, the arguments are built from temp dir paths
	cmd := exec.CommandContext(ctx, e.BinPath, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("createrepo_c failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}
