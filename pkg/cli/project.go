package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/spf13/cobra"
)

func newProjectCmd(store storage.Store, opts *OutputOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Manage bsgo projects",
	}

	cmd.AddCommand(newProjectListCmd(store, opts))
	cmd.AddCommand(newProjectShowCmd(store, opts))
	cmd.AddCommand(newProjectAddCmd(store, opts))
	return cmd
}

func newProjectAddCmd(store storage.Store, opts *OutputOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "add <manifest.yaml>",
		Short: "Add or import a project from a YAML manifest",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			manifestPath := filepath.Clean(args[0])

			// #nosec G304 -- CLI argument specifying manifest path to import
			data, err := os.ReadFile(manifestPath)
			if err != nil {
				return fmt.Errorf("failed to read project manifest: %w", err)
			}

			prj, err := manifest.ParseProject(data)
			if err != nil {
				return fmt.Errorf("invalid project manifest: %w", err)
			}

			if err := store.CreateProject(ctx, prj); err != nil && !errors.Is(err, storage.ErrConflict) {
				return fmt.Errorf("failed to store project: %w", err)
			}

			result := map[string]any{
				"status":  "ADDED",
				"project": prj.Project.Name,
				"title":   prj.Project.Title,
			}

			var sb strings.Builder
			sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#04B575")).Render("✔ Project added successfully!") + "\n\n")
			fmt.Fprintf(&sb, "  Project: %s\n", prj.Project.Name)
			fmt.Fprintf(&sb, "  Title:   %s\n", prj.Project.Title)

			return Render(cmd.OutOrStdout(), sb.String(), result, prj.Project.Name, *opts)
		},
	}
}

func newProjectListCmd(store storage.Store, opts *OutputOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all registered projects",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			prjs, err := store.ListProjects(ctx)
			if err != nil {
				return err
			}

			var sb strings.Builder
			sb.WriteString(titleStyle.Render("📦 Projects") + "\n")

			var quietList []string
			for _, p := range prjs {
				var targets []string
				for t := range p.Targets {
					targets = append(targets, t)
				}
				targetsStr := subtleStyle.Render("[" + strings.Join(targets, ", ") + "]")
				fmt.Fprintf(&sb, "  • %s %s\n    %s\n",
					lipgloss.NewStyle().Bold(true).Render(p.Project.Name),
					targetsStr,
					subtleStyle.Render(p.Project.Title),
				)
				quietList = append(quietList, p.Project.Name)
			}

			return Render(cmd.OutOrStdout(), sb.String(), prjs, strings.Join(quietList, "\n"), *opts)
		},
	}
}

func newProjectShowCmd(store storage.Store, opts *OutputOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "show <project>",
		Short: "Show details for a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			name := args[0]
			p, err := store.GetProject(ctx, name)
			if err != nil {
				return fmt.Errorf("project '%s' not found: %w", name, err)
			}

			var targets []string
			for t := range p.Targets {
				targets = append(targets, t)
			}

			var sb strings.Builder
			sb.WriteString(titleStyle.Render("Project: "+p.Project.Name) + "\n\n")
			fmt.Fprintf(&sb, "  Title:       %s\n", p.Project.Title)
			fmt.Fprintf(&sb, "  Description: %s\n", p.Project.Description)
			fmt.Fprintf(&sb, "  Targets:     %s\n", strings.Join(targets, ", "))

			return Render(cmd.OutOrStdout(), sb.String(), p, p.Project.Name, *opts)
		},
	}
}
