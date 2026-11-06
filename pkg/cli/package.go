package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/spf13/cobra"
)

func newPackageCmd(store storage.Store, opts *OutputOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "package",
		Short: "Manage bsgo packages within projects",
	}

	cmd.AddCommand(newPackageListCmd(store, opts))
	cmd.AddCommand(newPackageShowCmd(store, opts))
	cmd.AddCommand(newPackageSetResourcesCmd(store, opts))
	return cmd
}

func newPackageListCmd(store storage.Store, opts *OutputOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "list <project>",
		Short: "List packages within a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			projectName := args[0]
			pkgs, err := store.ListPackages(ctx, projectName)
			if err != nil {
				return err
			}

			var sb strings.Builder
			sb.WriteString(titleStyle.Render(fmt.Sprintf("Packages in %s (%d)", projectName, len(pkgs))) + "\n")

			var quietList []string
			for _, p := range pkgs {
				shardStr := subtleStyle.Render("[shard: " + manifest.ShardPath(p.Package.Name) + "]")
				status := successBadge.Render()
				if !p.Build.Enabled {
					status = disabledBadge.Render()
				}
				fmt.Fprintf(&sb, "  • %-20s %s %s\n    %s (branch: %s)\n",
					lipgloss.NewStyle().Bold(true).Render(p.Package.Name),
					status,
					shardStr,
					subtleStyle.Render(p.Source.Git),
					lipgloss.NewStyle().Foreground(lipgloss.Color("#00BFFF")).Render(p.Source.Branch),
				)
				quietList = append(quietList, p.Package.Name)
			}

			return Render(cmd.OutOrStdout(), sb.String(), pkgs, strings.Join(quietList, "\n"), *opts)
		},
	}
}

func newPackageShowCmd(store storage.Store, opts *OutputOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "show <project/package>",
		Short: "Show manifest details for a package",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			parts := strings.SplitN(args[0], "/", 2)
			if len(parts) != 2 {
				return fmt.Errorf("package must be in <project>/<package> format")
			}
			projectName, pkgName := parts[0], parts[1]

			p, err := store.GetPackage(ctx, projectName, pkgName)
			if err != nil {
				return fmt.Errorf("package '%s' not found: %w", args[0], err)
			}

			var sb strings.Builder
			sb.WriteString(titleStyle.Render("Package: "+p.Package.Name) + "\n\n")
			fmt.Fprintf(&sb, "  Project:       %s\n", projectName)
			fmt.Fprintf(&sb, "  Shard File:    %s\n", manifest.ShardPath(p.Package.Name))
			fmt.Fprintf(&sb, "  Git Source:    %s\n", p.Source.Git)
			fmt.Fprintf(&sb, "  Branch:        %s\n", p.Source.Branch)
			fmt.Fprintf(&sb, "  Pinned Commit: %s\n", p.Source.Commit)
			fmt.Fprintf(&sb, "  Build Enabled: %v\n", p.Build.Enabled)
			fmt.Fprintf(&sb, "  Resources:     RAM: %s | vCPUs: %d | Disk: %s | Timeout: %s\n",
				p.Resources.Memory, p.Resources.VCPUs, p.Resources.Disk, p.Resources.Timeout)

			return Render(cmd.OutOrStdout(), sb.String(), p, p.Package.Name, *opts)
		},
	}
}

func newPackageSetResourcesCmd(store storage.Store, opts *OutputOptions) *cobra.Command {
	var (
		resMemory string
		resVCPUs  int
		resDisk   string
	)

	cmd := &cobra.Command{
		Use:   "set-resources <project/package>",
		Short: "Update hardware constraints on worker VM",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			parts := strings.SplitN(args[0], "/", 2)
			if len(parts) != 2 {
				return fmt.Errorf("package must be in <project>/<package> format")
			}
			projectName, pkgName := parts[0], parts[1]

			p, err := store.GetPackage(ctx, projectName, pkgName)
			if err != nil {
				return fmt.Errorf("package '%s' not found: %w", args[0], err)
			}

			if resMemory != "" {
				p.Resources.Memory = resMemory
			}
			if resVCPUs > 0 {
				p.Resources.VCPUs = resVCPUs
			}
			if resDisk != "" {
				p.Resources.Disk = resDisk
			}

			if err := store.UpsertPackage(ctx, projectName, p); err != nil {
				return fmt.Errorf("failed to save updated package: %w", err)
			}

			result := map[string]any{
				"status":        "UPDATED",
				"project":       projectName,
				"package":       pkgName,
				"manifest":      manifest.ShardPath(pkgName),
				"new_resources": p.Resources,
			}

			var sb strings.Builder
			sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#04B575")).Render("✔ Resources updated successfully!") + "\n\n")
			fmt.Fprintf(&sb, "  Shard File: %s\n", manifest.ShardPath(pkgName))
			fmt.Fprintf(&sb, "  Resources:  RAM: %s | vCPUs: %d | Disk: %s\n",
				p.Resources.Memory, p.Resources.VCPUs, p.Resources.Disk)

			return Render(cmd.OutOrStdout(), sb.String(), result, "UPDATED", *opts)
		},
	}

	cmd.Flags().StringVar(&resMemory, "memory", "", "Worker VM memory (e.g. 16Gi)")
	cmd.Flags().IntVar(&resVCPUs, "vcpus", 0, "Worker VM vCPUs (e.g. 8)")
	cmd.Flags().StringVar(&resDisk, "disk", "", "Worker VM disk (e.g. 50Gi)")
	return cmd
}
