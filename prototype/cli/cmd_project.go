package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var projectCmd = &cobra.Command{
	Use:   "project",
	Short: "Manage bsgo projects",
}

var projectListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all accessible projects",
	Run: func(cmd *cobra.Command, args []string) {
		var sb strings.Builder
		sb.WriteString(titleStyle.Render("📦 Projects") + "\n")

		for _, p := range mockProjects {
			targetsStr := subtleStyle.Render("[" + strings.Join(p.Targets, ", ") + "]")
			pkgStr := fmt.Sprintf("(%d packages)", p.PackageCount)
			fmt.Fprintf(&sb, "  • %s %s %s\n    %s\n",
				lipgloss.NewStyle().Bold(true).Render(p.Name),
				targetsStr,
				subtleStyle.Render(pkgStr),
				subtleStyle.Render(p.Title),
			)
		}

		var quietList []string
		for _, p := range mockProjects {
			quietList = append(quietList, p.Name)
		}

		renderOutput(sb.String(), mockProjects, strings.Join(quietList, "\n"))
	},
}

var projectShowCmd = &cobra.Command{
	Use:   "show <project>",
	Short: "Show details for a project",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		for _, p := range mockProjects {
			if p.Name == name {
				var sb strings.Builder
				sb.WriteString(titleStyle.Render("Project: "+p.Name) + "\n\n")
				fmt.Fprintf(&sb, "  Title:        %s\n", p.Title)
				fmt.Fprintf(&sb, "  Packages:     %d\n", p.PackageCount)
				fmt.Fprintf(&sb, "  Targets:      %s\n", strings.Join(p.Targets, ", "))
				fmt.Fprintf(&sb, "  Git Repo:     https://git.infra.local/%s.git\n", p.Name)
				renderOutput(sb.String(), p, p.Name)
				return
			}
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Error: project '%s' not found\n", name)
	},
}

func init() {
	projectCmd.AddCommand(projectListCmd)
	projectCmd.AddCommand(projectShowCmd)
}
