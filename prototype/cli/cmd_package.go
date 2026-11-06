package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var packageCmd = &cobra.Command{
	Use:   "package",
	Short: "Manage bsgo packages within projects",
}

var packageListCmd = &cobra.Command{
	Use:   "list <project>",
	Short: "List packages in a project",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		prj := args[0]
		var results []Package
		for _, pkg := range mockPackages {
			if pkg.Project == prj {
				results = append(results, pkg)
			}
		}

		var sb strings.Builder
		sb.WriteString(titleStyle.Render(fmt.Sprintf("Packages in %s (%d shown)", prj, len(results))) + "\n")

		var quietList []string
		for _, pkg := range results {
			shardStr := subtleStyle.Render("[shard: " + pkg.Shard + "]")
			statusStr := successBadge.Render()
			if !pkg.Enabled {
				statusStr = failedBadge.SetString("DISABLED").Render()
			}
			fmt.Fprintf(&sb, "  • %-20s %s %s\n    %s (branch: %s)\n",
				lipgloss.NewStyle().Bold(true).Render(pkg.Name),
				statusStr,
				shardStr,
				subtleStyle.Render(pkg.GitURL),
				lipgloss.NewStyle().Foreground(lipgloss.Color("#00BFFF")).Render(pkg.Branch),
			)
			quietList = append(quietList, pkg.Name)
		}

		renderOutput(sb.String(), results, strings.Join(quietList, "\n"))
	},
}

var packageShowCmd = &cobra.Command{
	Use:   "show <project/package>",
	Short: "Show package manifest and shard info",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		key := args[0]
		pkg, found := mockPackages[key]
		if !found {
			fmt.Fprintf(cmd.ErrOrStderr(), "Error: package '%s' not found\n", key)
			return
		}

		var sb strings.Builder
		sb.WriteString(titleStyle.Render("Package: "+pkg.Name) + "\n\n")
		fmt.Fprintf(&sb, "  Project:       %s\n", pkg.Project)
		fmt.Fprintf(&sb, "  Consistent Shard: packages/%s/%s.yaml\n", pkg.Shard, pkg.Name)
		fmt.Fprintf(&sb, "  Git Source:    %s\n", pkg.GitURL)
		fmt.Fprintf(&sb, "  Branch:        %s\n", pkg.Branch)
		fmt.Fprintf(&sb, "  Pinned Commit: %s\n", pkg.Commit)
		fmt.Fprintf(&sb, "  Build Enabled: %v\n", pkg.Enabled)
		fmt.Fprintf(&sb, "  Resources:     RAM: %s | vCPUs: %d | Disk: %s | Timeout: %s\n",
			pkg.Resources.Memory, pkg.Resources.VCPUs, pkg.Resources.Disk, pkg.Resources.Timeout)

		renderOutput(sb.String(), pkg, pkg.Name)
	},
}

var (
	resMemory string
	resVCPUs  int
	resDisk   string
)

var packageSetResourcesCmd = &cobra.Command{
	Use:   "set-resources <project/package>",
	Short: "Update hardware constraints via API (creates Git commit automatically)",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		key := args[0]
		pkg, found := mockPackages[key]
		if !found {
			fmt.Fprintf(cmd.ErrOrStderr(), "Error: package '%s' not found\n", key)
			return
		}

		// Update in mock
		if resMemory != "" {
			pkg.Resources.Memory = resMemory
		}
		if resVCPUs > 0 {
			pkg.Resources.VCPUs = resVCPUs
		}
		if resDisk != "" {
			pkg.Resources.Disk = resDisk
		}
		mockPackages[key] = pkg

		result := map[string]any{
			"status":        "COMMITTED",
			"project":       pkg.Project,
			"package":       pkg.Name,
			"manifest":      fmt.Sprintf("packages/%s/%s.yaml", pkg.Shard, pkg.Name),
			"git_commit":    "a9b8c7d6e5f4a3b2c1d0e9f8a7b6c5d4e3f2a1b0",
			"author":        "agent@infra.local",
			"new_resources": pkg.Resources,
		}

		var sb strings.Builder
		sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#04B575")).Render("✔ Resource requirements updated successfully!") + "\n\n")
		fmt.Fprintf(&sb, "  Shard File: packages/%s/%s.yaml\n", pkg.Shard, pkg.Name)
		fmt.Fprintf(&sb, "  Git Commit: %s (auto-committed to %s)\n",
			lipgloss.NewStyle().Foreground(lipgloss.Color("#00BFFF")).Render("a9b8c7d6"), pkg.Project)
		fmt.Fprintf(&sb, "  Resources:  RAM: %s | vCPUs: %d | Disk: %s\n",
			pkg.Resources.Memory, pkg.Resources.VCPUs, pkg.Resources.Disk)

		renderOutput(sb.String(), result, "a9b8c7d6")
	},
}

var packageTriggerCmd = &cobra.Command{
	Use:   "trigger <project/package>",
	Short: "Trigger a build for a package",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		key := args[0]
		pkg, found := mockPackages[key]
		if !found {
			fmt.Fprintf(cmd.ErrOrStderr(), "Error: package '%s' not found\n", key)
			return
		}

		newJobID := "job-5a6b7c"
		result := map[string]any{
			"status":  "TRIGGERED",
			"job_id":  newJobID,
			"project": pkg.Project,
			"package": pkg.Name,
			"commit":  pkg.Commit,
			"subject": fmt.Sprintf("bsgo.triggers.%s.%s", pkg.Project, pkg.Name),
		}

		var sb strings.Builder
		sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#04B575")).Render("✔ Build triggered successfully!") + "\n\n")
		fmt.Fprintf(&sb, "  Job ID:   %s\n", lipgloss.NewStyle().Bold(true).Render(newJobID))
		fmt.Fprintf(&sb, "  Project:  %s\n", pkg.Project)
		fmt.Fprintf(&sb, "  Package:  %s\n", pkg.Name)
		fmt.Fprintf(&sb, "  NATS:     bsgo.triggers.%s.%s\n", pkg.Project, pkg.Name)
		sb.WriteString(subtleStyle.Render(fmt.Sprintf("  Run 'bsgo build logs -f %s' to stream logs\n", newJobID)))

		renderOutput(sb.String(), result, newJobID)
	},
}

func init() {
	packageSetResourcesCmd.Flags().StringVar(&resMemory, "memory", "", "Worker VM memory (e.g. 16Gi)")
	packageSetResourcesCmd.Flags().IntVar(&resVCPUs, "vcpus", 0, "Worker VM vCPUs (e.g. 8)")
	packageSetResourcesCmd.Flags().StringVar(&resDisk, "disk", "", "Worker VM disk (e.g. 50Gi)")

	packageCmd.AddCommand(packageListCmd)
	packageCmd.AddCommand(packageShowCmd)
	packageCmd.AddCommand(packageSetResourcesCmd)
	packageCmd.AddCommand(packageTriggerCmd)
}
