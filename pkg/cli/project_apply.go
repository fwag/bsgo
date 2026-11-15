package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/spf13/cobra"
)

const applyLong = `Apply a project directory, as written by "bsgo project import-obs-git", to the
control plane: upsert <dir>/project.yaml, then every package manifest under
<dir>/packages/. Existing entries are replaced (upsert), so the command is safe to repeat.

Every package file is validated, and must sit at its shard path
(packages/<sha256[:2]>/<name>.yaml). Failures never abort the run: all files are tried,
each failure is reported with its file name, and the exit code is non-zero if any failed.

With --dry-run nothing is stored; the files are parsed and validated only.
With --json, stdout carries one JSON document and nothing else.`

const applyExample = `  # Store project and packages
  bsgo project apply ./SLFO-bsgo

  # Validate only, machine-readable
  bsgo project apply ./SLFO-bsgo --dry-run --json`

type applyFailure struct {
	File  string `json:"file"`
	Error string `json:"error"`
}

type applyOutput struct {
	Dir      string         `json:"dir"`
	DryRun   bool           `json:"dry_run"`
	Project  string         `json:"project"`
	Applied  int            `json:"packages_applied"`
	Failures []applyFailure `json:"failures"`
}

func newProjectApplyCmd(store storage.Store, opts *OutputOptions) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:     "apply <dir> [--dry-run]",
		Short:   "Upsert project.yaml and all package manifests of a directory",
		Long:    applyLong,
		Example: applyExample,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			out := applyDir(ctx, store, filepath.Clean(args[0]), dryRun)
			return reportApply(cmd, opts, out)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "parse and validate, but store nothing")
	return cmd
}

func applyDir(ctx context.Context, store storage.Store, dir string, dryRun bool) *applyOutput {
	out := &applyOutput{Dir: dir, DryRun: dryRun, Failures: []applyFailure{}}
	fail := func(file string, err error) {
		out.Failures = append(out.Failures, applyFailure{File: file, Error: err.Error()})
	}
	prjFile := filepath.Join(dir, "project.yaml")
	prj, err := loadProjectFile(prjFile)
	if err != nil {
		fail(prjFile, err)
		return out
	}
	out.Project = prj.Project.Name
	if !dryRun {
		if err := store.UpsertProject(ctx, prj); err != nil {
			fail(prjFile, err)
			return out
		}
	}
	files, err := packageFiles(dir)
	if err != nil {
		fail(filepath.Join(dir, "packages"), err)
		return out
	}
	for _, file := range files {
		if err := applyPackage(ctx, store, dir, file, prj.Project.Name, dryRun); err != nil {
			fail(file, err)
			continue
		}
		out.Applied++
	}
	return out
}

func loadProjectFile(file string) (*manifest.ProjectManifest, error) {
	data, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		return nil, err
	}
	return manifest.ParseProject(data)
}

func packageFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(filepath.Join(dir, "packages"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".yaml") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

func applyPackage(ctx context.Context, store storage.Store, dir, file, project string, dryRun bool) error {
	data, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		return err
	}
	pkg, err := manifest.ParsePackage(data)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(dir, file)
	if err != nil {
		return err
	}
	if want := manifest.ShardPath(pkg.Package.Name); filepath.ToSlash(rel) != want {
		return fmt.Errorf("package %q must be stored at %s", pkg.Package.Name, want)
	}
	if dryRun {
		return nil
	}
	return store.UpsertPackage(ctx, project, pkg)
}

func reportApply(cmd *cobra.Command, opts *OutputOptions, out *applyOutput) error {
	if opts.JSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			return err
		}
	} else {
		for _, f := range out.Failures {
			fmt.Fprintf(cmd.ErrOrStderr(), "failed %s: %s\n", f.File, f.Error)
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "applied project %q and %d packages from %s (dry-run=%t), %d failures\n",
			out.Project, out.Applied, out.Dir, out.DryRun, len(out.Failures))
	}
	if len(out.Failures) > 0 {
		return errors.New("apply finished with failures")
	}
	return nil
}
