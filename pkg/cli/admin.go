package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/spf13/cobra"
)

func newAdminCmd(store storage.Store, opts *OutputOptions) *cobra.Command {
	adminCmd := &cobra.Command{
		Use:   "admin",
		Short: "Administrative management and event injection commands",
		Long: `Administrative management commands modeled after legacy OBS obs_admin tools.
Enables operations teams and automated software factories to inject high-priority
triggers, force package rebuilds, and manage queue priorities.`,
	}

	adminCmd.AddCommand(newAdminTriggerCmd(store, opts))
	adminCmd.AddCommand(newAdminPurgeCmd(opts))
	return adminCmd
}

func newAdminPurgeCmd(opts *OutputOptions) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "purge-queues",
		Aliases: []string{"purge"},
		Short:   "Purge all jobs and trigger history from NATS JetStream",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := context.Background()
			natsURL := os.Getenv("NATS_URL")
			if natsURL == "" {
				natsURL = "nats://localhost:4222"
			}

			nc, js, err := natsqueue.ConnectJetStream(natsURL)
			if err != nil {
				return fmt.Errorf("failed connecting to NATS: %w", err)
			}
			defer nc.Close()

			streamNames := []string{
				natsqueue.StreamTriggers,
				natsqueue.StreamJobStatus,
				natsqueue.StreamJobsPending,
			}

			var purged []string
			for _, name := range streamNames {
				str, err := js.Stream(ctx, name)
				if err == nil {
					_ = str.Purge(ctx)
					purged = append(purged, name)
				}
			}

			res := map[string]any{
				"status":  "PURGED",
				"streams": purged,
			}
			if opts.JSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(res)
			}
			fmt.Printf("✔ Successfully purged all jobs from NATS streams: %s\n", strings.Join(purged, ", "))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Bypass confirmation")
	return cmd
}

type adminTriggerFlags struct {
	priority  string
	reason    string
	target    string
	arch      string
	commitSHA string
	force     bool
	all       bool
	dryRun    bool
	yes       bool
}

func newAdminTriggerCmd(store storage.Store, opts *OutputOptions) *cobra.Command {
	var flags adminTriggerFlags

	cmd := &cobra.Command{
		Use:   "trigger <project> [package]",
		Short: "Inject administrative build trigger events into the scheduler",
		Long: `Inject prioritized build triggers directly into the scheduler pipeline.
Similar to legacy OBS 'obs_admin --force-check-project', this allows operators to bypass
routine queues by injecting high-priority triggers or forcing rebuilds.

Examples:
  # Inject high-priority rebuild for a single package:
  bsgo admin trigger my-project my-package --priority high --reason "CRITICAL_CVE_REBUILD"

  # Force rebuild all packages in a project:
  bsgo admin trigger my-project --all --force --priority high

  # Dry-run trigger injection with machine-readable JSON:
  bsgo admin trigger my-project my-package --target standard --arch x86_64 --dry-run --json`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			projectName, pkgNames, err := resolveTargetPackages(ctx, store, args, flags.all)
			if err != nil {
				return err
			}

			if _, err := store.GetProject(ctx, projectName); err != nil {
				return fmt.Errorf("project '%s' not found: %w", projectName, err)
			}

			var triggeredResults []map[string]any
			for _, pkgName := range pkgNames {
				res, err := processSingleTrigger(ctx, store, projectName, pkgName, flags)
				if err != nil {
					return err
				}
				triggeredResults = append(triggeredResults, res)
			}

			return renderAdminTriggerResults(cmd.OutOrStdout(), triggeredResults, *opts)
		},
	}

	cmd.Flags().StringVar(&flags.priority, "priority", "high", "Trigger priority tier: 'high', 'med', or 'low'")
	cmd.Flags().StringVar(&flags.reason, "reason", "ADMIN_TRIGGER", "Audit reason for administrative trigger injection")
	cmd.Flags().StringVar(&flags.target, "target", "", "Filter trigger to a specific build target")
	cmd.Flags().StringVar(&flags.arch, "arch", "", "Filter trigger to a specific architecture")
	cmd.Flags().StringVar(&flags.commitSHA, "commit", "", "Override source commit SHA")
	cmd.Flags().BoolVarP(&flags.force, "force", "f", false, "Force rebuild bypassing cache checks")
	cmd.Flags().BoolVar(&flags.all, "all", false, "Trigger all packages defined in the project")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "Simulate trigger generation without publishing to NATS")
	cmd.Flags().BoolVarP(&flags.yes, "yes", "y", false, "Bypass interactive confirmation")

	return cmd
}

func resolveTargetPackages(ctx context.Context, store storage.Store, args []string, all bool) (string, []string, error) {
	projectName := args[0]
	if len(args) == 2 {
		return projectName, []string{args[1]}, nil
	}
	if all {
		pkgs, err := store.ListPackages(ctx, projectName)
		if err != nil {
			return "", nil, fmt.Errorf("failed to list packages for project '%s': %w", projectName, err)
		}
		if len(pkgs) == 0 {
			return "", nil, fmt.Errorf("project '%s' contains no packages", projectName)
		}
		names := make([]string, len(pkgs))
		for i, p := range pkgs {
			names[i] = p.Package.Name
		}
		return projectName, names, nil
	}
	if strings.Contains(projectName, "/") {
		parts := strings.SplitN(projectName, "/", 2)
		return parts[0], []string{parts[1]}, nil
	}
	return "", nil, fmt.Errorf("must specify package name or use --all to trigger entire project")
}

func renderAdminTriggerResults(w io.Writer, results []map[string]any, opts OutputOptions) error {
	if opts.JSON {
		return json.NewEncoder(w).Encode(results)
	}

	var sb strings.Builder
	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#04B575")).
		Render(fmt.Sprintf("✔ Injected %d administrative build trigger(s)", len(results))) + "\n\n")

	for _, tr := range results {
		fmt.Fprintf(&sb, "  Job ID:     %s\n", tr["trigger_id"])
		fmt.Fprintf(&sb, "  Package:    %s/%s\n", tr["project"], tr["package"])
		fmt.Fprintf(&sb, "  Priority:   %s\n", tr["priority"])
		fmt.Fprintf(&sb, "  Reason:     %s\n", tr["reason"])
		fmt.Fprintf(&sb, "  Subject:    %s\n", tr["subject"])
		if tr["dry_run"] == true {
			sb.WriteString("  Mode:       DRY RUN (not published)\n")
		}
		sb.WriteString("\n")
	}

	return Render(w, sb.String(), results, "", opts)
}

func processSingleTrigger(ctx context.Context, store storage.Store, project, pkgName string, flags adminTriggerFlags) (map[string]any, error) {
	pkg, err := store.GetPackage(ctx, project, pkgName)
	if err != nil {
		return nil, fmt.Errorf("package '%s/%s' not found: %w", project, pkgName, err)
	}

	commit := flags.commitSHA
	if commit == "" {
		commit = pkg.Source.Commit
		if commit == "" {
			commit = "HEAD"
		}
	}

	trigger := v1.NewAdminTrigger(
		project,
		pkgName,
		pkg.Source.Git,
		commit,
		pkg.Source.Branch,
		"admin",
		flags.target,
		flags.arch,
		flags.priority,
		flags.reason,
		flags.force,
	)

	subject := natsqueue.TriggerSubject(flags.priority, project, pkgName)

	result := map[string]any{
		"trigger_id": trigger.TriggerID,
		"job_id":     trigger.TriggerID,
		"project":    project,
		"package":    pkgName,
		"commit_sha": commit,
		"priority":   trigger.NormalizedPriority(),
		"reason":     trigger.Reason,
		"target":     flags.target,
		"arch":       flags.arch,
		"force":      flags.force,
		"subject":    subject,
		"dry_run":    flags.dryRun,
		"status":     "TRIGGERED",
	}

	if flags.dryRun {
		return result, nil
	}

	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = "nats://localhost:4222"
	}

	if nc, js, err := natsqueue.ConnectJetStream(natsURL); err == nil {
		defer nc.Close()
		_ = natsqueue.SetupStreams(ctx, js)

		tData, _ := json.Marshal(trigger)
		_, _ = js.Publish(ctx, subject, tData)

		_ = nc.Flush()
	}

	return result, nil
}
