package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/spf13/cobra"
)

// BlockingBinary is one binary package a blocked package waits for, the
// source package that produces it and that package's state.
type BlockingBinary struct {
	Binary string `json:"binary"`
	Unit   string `json:"unit"`
	State  string `json:"state"`
	// CycleOnly is set when the producer is not ready only because its
	// dependency cycle is not finished; CycleInFlight are the members of the
	// cycle that are scheduled, building or blocked.
	CycleOnly     bool                      `json:"cycle_only"`
	CycleInFlight []storage.BlockedProducer `json:"cycle_in_flight,omitempty"`
}

// MissingDependency is a requirement nothing provides.
type MissingDependency struct {
	Requirement string `json:"requirement"`
	NeededBy    string `json:"needed_by,omitempty"`
}

// PackageWhyEntry explains the state of a package in one repository and
// architecture.
type PackageWhyEntry struct {
	Repo      string `json:"repo"`
	Arch      string `json:"arch"`
	State     string `json:"state"`
	Details   string `json:"details,omitempty"`
	UpdatedAt string `json:"updated_at"`
	// BlockedBy lists every blocking binary; Blocked is the structured reason
	// the checker stored.
	BlockedBy []BlockingBinary       `json:"blocked_by,omitempty"`
	Blocked   *storage.BlockedReason `json:"blocked,omitempty"`
	// Missing lists the unresolvable dependencies; Problems the unresolvable
	// details that are not a missing dependency.
	Missing  []MissingDependency `json:"missing,omitempty"`
	Problems []string            `json:"problems,omitempty"`
	// ScheduleReason is why a scheduled package is built ("new build",
	// "source change", "meta change", "forced rebuild").
	ScheduleReason string `json:"schedule_reason,omitempty"`
	SourceCommit   string `json:"source_commit,omitempty"`
	BuiltCommit    string `json:"built_commit,omitempty"`
	Meta           string `json:"meta,omitempty"`
	BuiltMeta      string `json:"built_meta,omitempty"`
}

// PackageWhyResult is the machine contract of `bsgo package why --json`.
type PackageWhyResult struct {
	Project string            `json:"project"`
	Package string            `json:"package"`
	Flavor  string            `json:"flavor,omitempty"`
	Entries []PackageWhyEntry `json:"entries"`
}

func newPackageWhyCmd(store storage.Store, opts *OutputOptions) *cobra.Command {
	var filter storage.BuildStateFilter
	cmd := &cobra.Command{
		Use:   "why <project>/<package>[:<flavor>]",
		Short: "Explain why a package is in its build state",
		Long: `Explain the persisted build state of one package, for every repository and
architecture (narrow it with --repo and --arch).

  blocked       every blocking binary package, the source package that
                produces it and that package's state. A producer that is built
                but not ready only because its dependency cycle is not
                finished says so and names the cycle members in flight.
  unresolvable  the dependencies nothing provides.
  scheduled     why the build was scheduled (new build, source change, meta
                change, forced rebuild).
  other states  the stored details.

With --json the command prints one deterministic JSON document on stdout:
{project, package, flavor, entries: [{repo, arch, state, details, blocked_by,
blocked, missing, problems, schedule_reason, ...}]}. Errors go to stderr.`,
		Example: `  bsgo package why SUSE:SLFO:1.2/7zip
  bsgo package why SUSE:SLFO:1.2/python-base:python313 --arch x86_64 --repo standard
  bsgo package why SUSE:SLFO:1.2/7zip --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := parsePackageRef(args[0])
			if err != nil {
				return err
			}
			res, err := loadPackageWhy(cmd.Context(), store, ref, filter)
			if err != nil {
				return err
			}
			var quiet []string
			for _, e := range res.Entries {
				quiet = append(quiet, fmt.Sprintf("%s/%s %s", e.Repo, e.Arch, e.State))
			}
			return Render(cmd.OutOrStdout(), formatPackageWhy(res), res, strings.Join(quiet, "\n"), *opts)
		},
	}
	cmd.Flags().StringVar(&filter.Repo, "repo", "", "Only explain this repository (build target)")
	cmd.Flags().StringVar(&filter.Arch, "arch", "", "Only explain this architecture")
	return cmd
}

func loadPackageWhy(ctx context.Context, store storage.Store, ref packageRef, filter storage.BuildStateFilter) (*PackageWhyResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := store.GetPackage(ctx, ref.Project, ref.Package); err != nil {
		return nil, fmt.Errorf("package '%s/%s' not found: %w", ref.Project, ref.Package, err)
	}
	states, err := store.ListBuildStates(ctx, ref.Project, filter)
	if err != nil {
		return nil, fmt.Errorf("failed listing build states: %w", err)
	}
	res := &PackageWhyResult{Project: ref.Project, Package: ref.Package, Flavor: ref.Flavor, Entries: []PackageWhyEntry{}}
	for _, st := range states {
		if st.Package == ref.Package && st.Flavor == ref.Flavor {
			res.Entries = append(res.Entries, whyEntry(st))
		}
	}
	if len(res.Entries) == 0 {
		return nil, fmt.Errorf("no build state for %s/%s yet (matching the --repo and --arch filters)", ref.Project, ref.unit())
	}
	return res, nil
}

func whyEntry(st *storage.PackageBuildState) PackageWhyEntry {
	e := PackageWhyEntry{
		Repo: st.Repo, Arch: st.Arch, State: st.State, Details: st.Details,
		UpdatedAt:    st.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		SourceCommit: st.SourceCommit, BuiltCommit: st.BuiltCommit, Meta: st.Meta, BuiltMeta: st.BuiltMeta,
	}
	switch st.State {
	case storage.StateBlocked:
		e.Blocked = st.Blocked
		e.BlockedBy = blockingBinaries(st.Blocked)
	case storage.StateUnresolvable:
		e.Missing, e.Problems = parseUnresolvable(st.Details)
	case storage.StateScheduled:
		e.ScheduleReason = st.Details
	}
	return e
}

// blockingBinaries flattens a reason into one entry per blocking binary,
// sorted by binary name.
func blockingBinaries(r *storage.BlockedReason) []BlockingBinary {
	if r == nil {
		return nil
	}
	var out []BlockingBinary
	for _, p := range r.Producers {
		for _, b := range p.Binaries {
			out = append(out, BlockingBinary{Binary: b, Unit: p.Unit, State: p.State})
		}
	}
	for _, c := range r.Cycles {
		for _, p := range c.Held {
			for _, b := range p.Binaries {
				out = append(out, BlockingBinary{Binary: b, Unit: p.Unit, State: p.State, CycleOnly: true, CycleInFlight: c.InFlight})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Binary < out[j].Binary })
	return out
}

const nothingProvides = "nothing provides "

// parseUnresolvable splits the details of an unresolvable package, the
// messages of the dependency solver joined by ", ", into missing dependencies
// and other problems.
func parseUnresolvable(details string) (missing []MissingDependency, problems []string) {
	var items []string
	for _, part := range strings.Split(details, ", ") {
		if len(items) > 0 && !strings.HasPrefix(part, nothingProvides) && !strings.HasPrefix(items[len(items)-1], nothingProvides) {
			items[len(items)-1] += ", " + part
			continue
		}
		items = append(items, part)
	}
	for _, it := range items {
		req, ok := strings.CutPrefix(it, nothingProvides)
		if !ok {
			if it != "" {
				problems = append(problems, it)
			}
			continue
		}
		r, by, _ := strings.Cut(req, " needed by ")
		missing = append(missing, MissingDependency{Requirement: r, NeededBy: by})
	}
	return missing, problems
}

func formatPackageWhy(res *PackageWhyResult) string {
	var sb strings.Builder
	name := res.Package
	if res.Flavor != "" {
		name += ":" + res.Flavor
	}
	sb.WriteString(titleStyle.Render(fmt.Sprintf("Why: %s/%s", res.Project, name)) + "\n")
	for _, e := range res.Entries {
		fmt.Fprintf(&sb, "\n%s\n", lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("%s/%s: %s", e.Repo, e.Arch, e.State)))
		writeWhyEntry(&sb, e)
	}
	return sb.String()
}

func writeWhyEntry(sb *strings.Builder, e PackageWhyEntry) {
	switch e.State {
	case storage.StateBlocked:
		writeWhyBlocked(sb, e)
	case storage.StateUnresolvable:
		for _, m := range e.Missing {
			line := "nothing provides " + m.Requirement
			if m.NeededBy != "" {
				line += " (needed by " + m.NeededBy + ")"
			}
			fmt.Fprintf(sb, "  - %s\n", line)
		}
		for _, p := range e.Problems {
			fmt.Fprintf(sb, "  - %s\n", p)
		}
	case storage.StateScheduled:
		fmt.Fprintf(sb, "  scheduled because: %s\n", e.ScheduleReason)
	default:
		if e.Details != "" {
			fmt.Fprintf(sb, "  %s\n", e.Details)
		}
	}
}

func writeWhyBlocked(sb *strings.Builder, e PackageWhyEntry) {
	if e.Blocked == nil {
		fmt.Fprintf(sb, "  %s\n  (no structured reason stored; the next checker pass records it)\n", e.Details)
		return
	}
	sb.WriteString("  waiting for these binary packages:\n")
	for _, b := range e.BlockedBy {
		line := fmt.Sprintf("  - %s <- %s (%s)", b.Binary, b.Unit, b.State)
		if b.CycleOnly {
			line += ": not ready only because its dependency cycle is not finished; in flight: " + membersText(b.CycleInFlight)
		}
		sb.WriteString(line + "\n")
	}
}
