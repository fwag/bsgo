package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/openSUSE/bsgo/pkg/manifest"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
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
	cmd.AddCommand(newProjectStatusCmd(store, opts))
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

			if err := store.UpsertProject(ctx, prj); err != nil {
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

func newProjectStatusCmd(store storage.Store, opts *OutputOptions) *cobra.Command {
	var natsURL string

	defaultNATSURL := os.Getenv("NATS_URL")
	if defaultNATSURL == "" {
		defaultNATSURL = "nats://localhost:4222"
	}

	cmd := &cobra.Command{
		Use:   "status <project>",
		Short: "Display comprehensive build and package status for a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			projectName := args[0]
			if _, err := store.GetProject(ctx, projectName); err != nil {
				return fmt.Errorf("project '%s' not found: %w", projectName, err)
			}

			pkgs, err := store.ListPackages(ctx, projectName)
			if err != nil {
				return fmt.Errorf("failed listing packages: %w", err)
			}

			return executeProjectStatus(ctx, cmd.OutOrStdout(), projectName, pkgs, natsURL, opts)
		},
	}

	cmd.Flags().StringVar(&natsURL, "nats-url", defaultNATSURL, "NATS JetStream connection URL")
	return cmd
}

type ProjectStatusResult struct {
	Project        string   `json:"project"`
	TotalPackages  int      `json:"total_packages"`
	Succeeded      int      `json:"succeeded"`
	Building       int      `json:"building"`
	Failed         int      `json:"failed"`
	Unresolvable   int      `json:"unresolvable"`
	Pending        int      `json:"pending"`
	ActiveBuilds   []string `json:"active_builds,omitempty"`
	RecentFailures []string `json:"recent_failures,omitempty"`
}

func executeProjectStatus(
	ctx context.Context,
	out io.Writer,
	projectName string,
	pkgs []*manifest.PackageManifest,
	natsURL string,
	opts *OutputOptions,
) error {
	var js jetstream.JetStream
	nc, jsConn, err := natsqueue.ConnectJetStream(natsURL)
	if err == nil {
		defer nc.Close()
		js = jsConn
	}

	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	pkgPhases := fetchProjectPackagePhases(fetchCtx, js, projectName)

	res := &ProjectStatusResult{
		Project:       projectName,
		TotalPackages: len(pkgs),
	}

	for _, p := range pkgs {
		name := p.Package.Name
		phase := pkgPhases[name]
		switch phase {
		case "BUILDING":
			res.Building++
			res.ActiveBuilds = append(res.ActiveBuilds, name)
		case "SUCCEEDED":
			res.Succeeded++
		case "UNRESOLVABLE":
			res.Unresolvable++
		case "FAILED", "ABORTED":
			res.Failed++
			if len(res.RecentFailures) < 15 {
				res.RecentFailures = append(res.RecentFailures, name)
			}
		default:
			res.Pending++
		}
	}

	humanView := formatProjectStatusHuman(res)
	return Render(out, humanView, res, fmt.Sprintf("%d/%d", res.Succeeded, res.TotalPackages), *opts)
}

func fetchProjectPackagePhases(ctx context.Context, js jetstream.JetStream, projectName string) map[string]string {
	phases := make(map[string]string)
	if js == nil {
		return phases
	}

	str, err := js.Stream(ctx, natsqueue.StreamJobStatus)
	if err != nil {
		return phases
	}
	info, err := str.Info(ctx)
	if err != nil || info.State.Msgs == 0 {
		return phases
	}

	cons, err := js.OrderedConsumer(ctx, natsqueue.StreamJobStatus, jetstream.OrderedConsumerConfig{
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return phases
	}

	it, err := cons.Messages()
	if err != nil {
		return phases
	}
	defer it.Stop()

	total := int(info.State.Msgs)
	for i := 0; i < total; i++ {
		select {
		case <-ctx.Done():
			return phases
		default:
		}

		msg, err := it.Next()
		if err != nil {
			break
		}
		processStatusMsg(msg.Data(), projectName, phases)
	}
	return phases
}

func processStatusMsg(data []byte, projectName string, phases map[string]string) {
	var st map[string]any
	if err := json.Unmarshal(data, &st); err != nil {
		return
	}
	prj, _ := st["project"].(string)
	pkg, _ := st["package"].(string)
	phase, _ := st["phase"].(string)
	if prj == projectName && pkg != "" && phase != "" {
		phases[pkg] = phase
	}
}

func formatProjectStatusHuman(res *ProjectStatusResult) string {
	var sb strings.Builder
	sb.WriteString(titleStyle.Render(fmt.Sprintf("📦 Project Status: %s", res.Project)) + "\n\n")

	sb.WriteString(lipgloss.NewStyle().Bold(true).Render("Summary:") + "\n")
	fmt.Fprintf(&sb, "  • Total Packages:      %s\n", lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("%d", res.TotalPackages)))
	fmt.Fprintf(&sb, "  • Succeeded:           %s\n", lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#04B575")).Render(fmt.Sprintf("%d", res.Succeeded)))
	fmt.Fprintf(&sb, "  • Currently Building:  %s\n", lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00BFFF")).Render(fmt.Sprintf("%d", res.Building)))
	fmt.Fprintf(&sb, "  • Failed:              %s\n", lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF3333")).Render(fmt.Sprintf("%d", res.Failed)))
	fmt.Fprintf(&sb, "  • Unresolvable:        %s\n", lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#E5C07B")).Render(fmt.Sprintf("%d", res.Unresolvable)))
	fmt.Fprintf(&sb, "  • Pending:             %s\n", lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFA500")).Render(fmt.Sprintf("%d", res.Pending)))

	if len(res.ActiveBuilds) > 0 {
		sb.WriteString("\n" + lipgloss.NewStyle().Bold(true).Render("Currently Building:") + "\n")
		for _, b := range res.ActiveBuilds {
			fmt.Fprintf(&sb, "  • %s\n", lipgloss.NewStyle().Foreground(lipgloss.Color("#00BFFF")).Render(b))
		}
	}

	if len(res.RecentFailures) > 0 {
		sb.WriteString("\n" + lipgloss.NewStyle().Bold(true).Render("Failed Packages (Sample):") + "\n")
		for _, f := range res.RecentFailures {
			fmt.Fprintf(&sb, "  • %s\n", lipgloss.NewStyle().Foreground(lipgloss.Color("#FF3333")).Render(f))
		}
	}
	return sb.String()
}
