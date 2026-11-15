package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/spf13/cobra"
)

// maxStatusSample bounds the per-state package samples in human output.
const maxStatusSample = 15

// PackageStateEntry is the persisted build state of one package/flavor in one repo and arch.
type PackageStateEntry struct {
	Package   string `json:"package"`
	Flavor    string `json:"flavor,omitempty"`
	Repo      string `json:"repo"`
	Arch      string `json:"arch"`
	State     string `json:"state"`
	Details   string `json:"details,omitempty"`
	JobID     string `json:"job_id,omitempty"`
	UpdatedAt string `json:"updated_at"`
}

// ProjectStatusResult is the machine contract of `bsgo project status --json`.
// Counts always equals `SELECT state, count(*) FROM package_build_state` for the same filters.
type ProjectStatusResult struct {
	Project       string              `json:"project"`
	Repo          string              `json:"repo,omitempty"`
	Arch          string              `json:"arch,omitempty"`
	StateFilter   string              `json:"state_filter,omitempty"`
	TotalPackages int                 `json:"total_packages"`
	Counts        map[string]int      `json:"counts"`
	Succeeded     int                 `json:"succeeded"`
	Building      int                 `json:"building"`
	Failed        int                 `json:"failed"`
	Unresolvable  int                 `json:"unresolvable"`
	Pending       int                 `json:"pending"`
	Packages      []PackageStateEntry `json:"packages"`
}

func newProjectStatusCmd(store storage.Store, opts *OutputOptions) *cobra.Command {
	var filter storage.BuildStateFilter

	cmd := &cobra.Command{
		Use:   "status <project>",
		Short: "Display persisted build state for the packages of a project",
		Long: `Display the build state of every package of a project as persisted in the
control-plane database (table package_build_state).

Valid values for --state: ` + strings.Join(storage.ValidBuildStates, ", ") + `.
With --json the command prints one deterministic JSON document on stdout:
per-state counts and one entry per package with its state and details.`,
		Example: `  bsgo project status SUSE:SLFO:1.2
  bsgo project status SUSE:SLFO:1.2 --repo standard --arch x86_64 --state unresolvable --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if filter.State != "" && !storage.IsValidBuildState(filter.State) {
				return fmt.Errorf("invalid --state %q (valid: %s)", filter.State, strings.Join(storage.ValidBuildStates, ", "))
			}
			res, err := loadProjectStatus(cmd.Context(), store, args[0], filter)
			if err != nil {
				return err
			}
			humanView := formatProjectStatusHuman(res)
			return Render(cmd.OutOrStdout(), humanView, res, fmt.Sprintf("%d/%d", res.Succeeded, res.TotalPackages), *opts)
		},
	}

	cmd.Flags().StringVar(&filter.Repo, "repo", "", "Only show states for this repository (build target)")
	cmd.Flags().StringVar(&filter.Arch, "arch", "", "Only show states for this architecture")
	cmd.Flags().StringVar(&filter.State, "state", "", "Only show packages in this state")
	return cmd
}

func loadProjectStatus(ctx context.Context, store storage.Store, projectName string, filter storage.BuildStateFilter) (*ProjectStatusResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := store.GetProject(ctx, projectName); err != nil {
		return nil, fmt.Errorf("project '%s' not found: %w", projectName, err)
	}
	pkgs, err := store.ListPackages(ctx, projectName)
	if err != nil {
		return nil, fmt.Errorf("failed listing packages: %w", err)
	}
	counts, err := store.CountBuildStates(ctx, projectName, filter)
	if err != nil {
		return nil, fmt.Errorf("failed counting build states: %w", err)
	}
	states, err := store.ListBuildStates(ctx, projectName, filter)
	if err != nil {
		return nil, fmt.Errorf("failed listing build states: %w", err)
	}

	res := &ProjectStatusResult{
		Project:       projectName,
		Repo:          filter.Repo,
		Arch:          filter.Arch,
		StateFilter:   filter.State,
		TotalPackages: len(pkgs),
		Counts:        counts,
		Succeeded:     counts[storage.StateSucceeded],
		Building:      counts[storage.StateBuilding],
		Failed:        counts[storage.StateFailed],
		Unresolvable:  counts[storage.StateUnresolvable],
		Pending:       counts[storage.StateScheduled],
		Packages:      make([]PackageStateEntry, 0, len(states)),
	}
	for _, st := range states {
		res.Packages = append(res.Packages, PackageStateEntry{
			Package:   st.Package,
			Flavor:    st.Flavor,
			Repo:      st.Repo,
			Arch:      st.Arch,
			State:     st.State,
			Details:   st.Details,
			JobID:     st.JobID,
			UpdatedAt: st.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	return res, nil
}

// sumCounts adds up the per-state counts. With multibuild flavors a package
// can have more than one row, so the "no state yet" line is clamped at zero.
func sumCounts(counts map[string]int) int {
	n := 0
	for _, c := range counts {
		n += c
	}
	return n
}

func statusLine(sb io.Writer, label string, n int, color string) {
	fmt.Fprintf(sb, "  • %-20s %s\n", label, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(color)).Render(fmt.Sprintf("%d", n)))
}

func formatProjectStatusHuman(res *ProjectStatusResult) string {
	var sb strings.Builder
	sb.WriteString(titleStyle.Render(fmt.Sprintf("📦 Project Status: %s", res.Project)) + "\n\n")

	sb.WriteString(lipgloss.NewStyle().Bold(true).Render("Summary:") + "\n")
	fmt.Fprintf(&sb, "  • %-20s %s\n", "Total Packages:", lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("%d", res.TotalPackages)))
	statusLine(&sb, "Succeeded:", res.Succeeded, "#04B575")
	statusLine(&sb, "Currently Building:", res.Building, "#00BFFF")
	statusLine(&sb, "Failed:", res.Failed, "#FF3333")
	statusLine(&sb, "Unresolvable:", res.Unresolvable, "#E5C07B")
	statusLine(&sb, "Scheduled:", res.Pending, "#FFA500")
	statusLine(&sb, "Blocked:", res.Counts[storage.StateBlocked], "#C678DD")
	statusLine(&sb, "Broken:", res.Counts[storage.StateBroken], "#FF3333")
	statusLine(&sb, "Excluded:", res.Counts[storage.StateExcluded], "#808080")
	statusLine(&sb, "Disabled:", res.Counts[storage.StateDisabled], "#808080")
	statusLine(&sb, "No state yet:", max(0, res.TotalPackages-sumCounts(res.Counts)), "#808080")

	writeStateSample(&sb, res, "building", "Currently Building:", "#00BFFF")
	writeStateSample(&sb, res, "failed", "Failed Packages (Sample):", "#FF3333")
	writeStateSample(&sb, res, "unresolvable", "Unresolvable Packages (Sample):", "#E5C07B")
	writeStateSample(&sb, res, "blocked", "Blocked Packages (Sample):", "#C678DD")
	writeStateSample(&sb, res, "broken", "Broken Packages (Sample):", "#FF3333")
	return sb.String()
}

func writeStateSample(sb *strings.Builder, res *ProjectStatusResult, state, title, color string) {
	var lines []string
	for _, e := range res.Packages {
		if e.State != state {
			continue
		}
		line := fmt.Sprintf("%s (%s/%s)", e.Package, e.Repo, e.Arch)
		if e.Details != "" {
			line += ": " + firstLine(e.Details)
		}
		lines = append(lines, line)
		if len(lines) == maxStatusSample {
			break
		}
	}
	if len(lines) == 0 {
		return
	}
	sb.WriteString("\n" + lipgloss.NewStyle().Bold(true).Render(title) + "\n")
	for _, l := range lines {
		fmt.Fprintf(sb, "  • %s\n", lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(l))
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
