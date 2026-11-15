package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/obsimport"
	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newProjectDependencyCmd(store storage.Store, opts *OutputOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dependency",
		Short: "Manage the dependencies of a project target",
	}
	cmd.AddCommand(newProjectDependencyRemoveCmd(store, opts))
	return cmd
}

const dependencyRemoveLong = `Remove the dependency with the given name from one target of a stored project.

Dependencies that carry a "name" (url entries such as the Bootstrap Dependency
"SUSE:SLFO:1.1/standard") are matched by that name. Removing the Bootstrap
Dependency ends bootstrap mode; with "rebuild: transitive" the project then converges
by rebuilding what was built against the bootstrap binaries.

The command mutates the stored project, so it needs --yes unless --dry-run is given.
It fails when the project or target does not exist, or when no dependency has that name.
With --json, stdout carries {"project","target","name","removed","dry_run","remaining"}.`

const dependencyRemoveExample = `  # Preview
  bsgo project dependency remove SUSE:SLFO:1.2 --target standard --name SUSE:SLFO:1.1/standard --dry-run

  # Drop the Bootstrap Dependency
  bsgo project dependency remove SUSE:SLFO:1.2 --target standard --name SUSE:SLFO:1.1/standard --yes --json`

type dependencyRemoveOutput struct {
	Project   string   `json:"project"`
	Target    string   `json:"target"`
	Name      string   `json:"name"`
	Removed   bool     `json:"removed"`
	DryRun    bool     `json:"dry_run"`
	Remaining []string `json:"remaining"`
}

func newProjectDependencyRemoveCmd(store storage.Store, opts *OutputOptions) *cobra.Command {
	var target, name string
	var yes, dryRun bool
	cmd := &cobra.Command{
		Use:     "remove <project> --target <target> --name <name> [--yes]",
		Short:   "Remove a named dependency from a project target",
		Long:    dependencyRemoveLong,
		Example: dependencyRemoveExample,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes && !dryRun {
				return errors.New("refusing to modify the project without --yes (or use --dry-run)")
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			out, err := removeDependency(ctx, store, args[0], target, name, dryRun)
			if err != nil {
				return err
			}
			if opts.JSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "removed dependency %q from %s target %s (dry-run=%t)\n", name, args[0], target, dryRun)
			return nil
		},
	}
	cmd.Flags().StringVar(&target, "target", "", "target (repository) of the project (required)")
	cmd.Flags().StringVar(&name, "name", "", "name of the dependency to remove (required)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "confirm the modification")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be removed, store nothing")
	_ = cmd.MarkFlagRequired("target")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func removeDependency(ctx context.Context, store storage.Store, project, target, name string, dryRun bool) (*dependencyRemoveOutput, error) {
	prj, err := store.GetProject(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("project %q not found: %w", project, err)
	}
	prj, err = cloneProject(prj)
	if err != nil {
		return nil, err
	}
	removed, err := obsimport.RemoveDependency(prj, target, name)
	if err != nil {
		return nil, err
	}
	if !removed {
		return nil, fmt.Errorf("target %q of %s has no dependency named %q", target, project, name)
	}
	if !dryRun {
		if err := store.UpsertProject(ctx, prj); err != nil {
			return nil, fmt.Errorf("store project: %w", err)
		}
	}
	remaining := []string{}
	for _, d := range prj.Targets[target].Dependencies {
		switch {
		case d.Name != "":
			remaining = append(remaining, d.Name)
		case d.URL != "":
			remaining = append(remaining, d.URL)
		default:
			remaining = append(remaining, d.Project+"/"+d.Target)
		}
	}
	return &dependencyRemoveOutput{Project: project, Target: target, Name: name, Removed: true, DryRun: dryRun, Remaining: remaining}, nil
}

// cloneProject copies a manifest through its YAML form so a dry run never
// mutates an object the store may share.
func cloneProject(prj *manifest.ProjectManifest) (*manifest.ProjectManifest, error) {
	data, err := yaml.Marshal(prj)
	if err != nil {
		return nil, fmt.Errorf("encode project: %w", err)
	}
	return manifest.ParseProject(data)
}
