package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/spf13/cobra"
)

// packageRef is a parsed <project>/<package>[:<flavor>] argument.
type packageRef struct {
	Project string
	Package string
	Flavor  string
}

// unit returns the OBS name of the unit: "pkg" or "pkg:flavor".
func (r packageRef) unit() string {
	if r.Flavor == "" {
		return r.Package
	}
	return r.Package + ":" + r.Flavor
}

// parsePackageRef splits on the first '/' (project names contain ':' but never
// '/'), then the package part on its first ':' into package and flavor.
func parsePackageRef(arg string) (packageRef, error) {
	project, rest, ok := strings.Cut(arg, "/")
	if !ok || project == "" || rest == "" {
		return packageRef{}, errors.New("package must be in <project>/<package>[:<flavor>] format")
	}
	pkg, flavor, hasFlavor := strings.Cut(rest, ":")
	if pkg == "" || (hasFlavor && flavor == "") || strings.ContainsAny(flavor, ":/") {
		return packageRef{}, fmt.Errorf("invalid package or flavor in %q", arg)
	}
	return packageRef{Project: project, Package: pkg, Flavor: flavor}, nil
}

// verifyFlavor rejects a flavor the checker has not seen for the package.
// The flavor list is derived from _multibuild by the checker and recorded as
// build-state rows; with no flavored rows yet the list is unknown and anything
// passes.
func verifyFlavor(ctx context.Context, store storage.Store, ref packageRef) error {
	if ref.Flavor == "" {
		return nil
	}
	states, err := store.ListBuildStates(ctx, ref.Project, storage.BuildStateFilter{})
	if err != nil {
		return fmt.Errorf("list build states: %w", err)
	}
	var known []string
	for _, st := range states {
		if st.Package == ref.Package && st.Flavor != "" && !slices.Contains(known, st.Flavor) {
			known = append(known, st.Flavor)
		}
	}
	if len(known) == 0 || slices.Contains(known, ref.Flavor) {
		return nil
	}
	slices.Sort(known)
	return fmt.Errorf("unknown flavor %q of %s/%s (known: %s); use --skip-flavor-check to force it anyway",
		ref.Flavor, ref.Project, ref.Package, strings.Join(known, ", "))
}

type packageTriggerFlags struct {
	commit    string
	natsURL   string
	repo      string
	arch      string
	priority  string
	dryRun    bool
	skipCheck bool
}

const packageTriggerLong = `Force a rebuild of a package, or of one multibuild flavor of it.

The argument is <project>/<package>[:<flavor>]. Project names may contain ':'
(SUSE:SLFO:1.2); the project ends at the first '/', and the flavor starts at
the first ':' of the package part.

Like OBS 'osc rebuild', the request covers exactly the named unit:
  <project>/gcc         rebuilds only the plain (unflavored) package gcc
  <project>/gcc:build   rebuilds only the flavor 'build' of gcc
OBS expands a package to its multibuild flavors only for result queries, never
for rebuild, so every flavor needs its own trigger. --repo and --arch narrow the
request like the repository and arch parameters of OBS rebuild; unset they match
every repository and architecture.

--priority sets the tier of the request: 'high' and 'med' add a boost to the
dispatch priority of the job the request produces, 'low' (the default) adds
none. The boost is added to the priority the scheduler computed from the
units that wait for the package (high: +20000, above every computed priority;
med: +5000), so the job overtakes queued jobs; a job that is already queued is
re-prioritised. The tier also selects the trigger subject.

A flavor is checked against the flavors the scheduler has already recorded for
the package; --skip-flavor-check bypasses this for a flavor not built yet.`

const packageTriggerExample = `  # Force the plain gcc package
  bsgo package trigger SUSE:SLFO:1.2/gcc

  # Force only the 'build' flavor of gcc, on x86_64 of repository standard
  bsgo package trigger SUSE:SLFO:1.2/gcc:build --repo standard --arch x86_64

  # Build gcc before everything else that is queued
  bsgo package trigger SUSE:SLFO:1.2/gcc --priority high

  # Show the request as JSON without publishing it
  bsgo package trigger SUSE:SLFO:1.2/gcc:build --dry-run --json`

func newPackageTriggerCmd(store storage.Store, opts *OutputOptions) *cobra.Command {
	var f packageTriggerFlags
	f.natsURL = os.Getenv("NATS_URL")
	if f.natsURL == "" {
		f.natsURL = "nats://localhost:4222"
	}

	cmd := &cobra.Command{
		Use:     "trigger <project>/<package>[:<flavor>]",
		Short:   "Force a rebuild of a package or one of its multibuild flavors",
		Long:    packageTriggerLong,
		Example: packageTriggerExample,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPackageTrigger(cmd, store, opts, args[0], &f)
		},
	}

	cmd.Flags().StringVar(&f.commit, "commit", "", "Optional commit SHA override to build")
	cmd.Flags().StringVar(&f.repo, "repo", "", "Only force this repository (default: all)")
	cmd.Flags().StringVar(&f.arch, "arch", "", "Only force this architecture (default: all)")
	cmd.Flags().StringVar(&f.priority, "priority", "low",
		"Dispatch priority tier: 'high' (+20000), 'med' (+5000) or 'low' (no boost)")
	cmd.Flags().BoolVar(&f.skipCheck, "skip-flavor-check", false, "Do not verify the flavor against the recorded flavors")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "Print the request without publishing it to NATS")
	cmd.Flags().StringVar(&f.natsURL, "nats-url", f.natsURL, "NATS JetStream connection URL")
	return cmd
}

func runPackageTrigger(cmd *cobra.Command, store storage.Store, opts *OutputOptions, arg string, f *packageTriggerFlags) error {
	ctx := context.Background()
	ref, err := parsePackageRef(arg)
	if err != nil {
		return err
	}
	p, err := store.GetPackage(ctx, ref.Project, ref.Package)
	if err != nil {
		return fmt.Errorf("package '%s/%s' not found: %w", ref.Project, ref.Package, err)
	}
	if !f.skipCheck {
		if err := verifyFlavor(ctx, store, ref); err != nil {
			return err
		}
	}

	tier, err := parseTier(f.priority)
	if err != nil {
		return err
	}
	commit := f.commit
	if commit == "" {
		commit = p.Source.Commit
	}
	if commit == "" {
		commit = "HEAD"
	}

	trigger := newForceTrigger(ref, p.Source.Git, commit, p.Source.Branch, f.repo, f.arch, tier)
	subject := natsqueue.TriggerSubject(tier, ref.Project, ref.unit())
	status := "TRIGGERED"
	if f.dryRun {
		status = "DRY_RUN"
	} else if err := publishPackageTrigger(ctx, f.natsURL, subject, trigger); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\n", err)
	}

	result := map[string]any{
		"status":     status,
		"job_id":     trigger.TriggerID,
		"trigger_id": trigger.TriggerID,
		"project":    ref.Project,
		"package":    ref.Package,
		"flavor":     ref.Flavor,
		"repo":       f.repo,
		"arch":       f.arch,
		"priority":   tier,
		"commit_sha": commit,
		"subject":    subject,
		"dry_run":    f.dryRun,
	}
	return Render(cmd.OutOrStdout(), triggerSummary(ref, f, commit, subject, trigger.TriggerID), result, trigger.TriggerID, *opts)
}

// parseTier validates a --priority value.
func parseTier(v string) (string, error) {
	switch v {
	case natsqueue.PriorityHigh, natsqueue.PriorityMed, natsqueue.PriorityLow:
		return v, nil
	}
	return "", fmt.Errorf("invalid --priority %q: use high, med or low", v)
}

// newForceTrigger builds the trigger that makes the scheduler force exactly
// the unit named by ref (repo and arch empty means all).
func newForceTrigger(ref packageRef, gitURL, commit, branch, repo, arch, tier string) *v1.BuildTrigger {
	t := v1.NewTrigger(ref.Project, ref.Package, gitURL, commit, branch, "developer")
	t.Flavor = ref.Flavor
	t.ForceRebuild = true // rebuild regardless of the meta; the checker consumes the request
	t.Priority = tier
	t.Target = repo
	t.Architecture = arch
	return t
}

func triggerSummary(ref packageRef, f *packageTriggerFlags, commit, subject, jobID string) string {
	var sb strings.Builder
	title := "✔ Build triggered successfully!"
	if f.dryRun {
		title = "✔ Dry run: nothing published"
	}
	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#04B575")).Render(title) + "\n\n")
	fmt.Fprintf(&sb, "  Job ID:     %s\n", lipgloss.NewStyle().Bold(true).Render(jobID))
	fmt.Fprintf(&sb, "  Project:    %s\n", ref.Project)
	fmt.Fprintf(&sb, "  Package:    %s\n", ref.Package)
	if ref.Flavor != "" {
		fmt.Fprintf(&sb, "  Flavor:     %s\n", ref.Flavor)
	}
	if f.repo != "" {
		fmt.Fprintf(&sb, "  Repository: %s\n", f.repo)
	}
	if f.arch != "" {
		fmt.Fprintf(&sb, "  Arch:       %s\n", f.arch)
	}
	fmt.Fprintf(&sb, "  Commit SHA: %s\n", commit)
	fmt.Fprintf(&sb, "  Subject:    %s\n", subject)
	return sb.String()
}

func publishPackageTrigger(ctx context.Context, natsURL, subject string, trigger *v1.BuildTrigger) error {
	nc, js, err := natsqueue.ConnectJetStream(natsURL)
	if err != nil {
		return fmt.Errorf("connect to NATS: %w", err)
	}
	defer nc.Close()

	if err := natsqueue.SetupStreams(ctx, js); err != nil {
		return fmt.Errorf("set up streams: %w", err)
	}
	data, err := json.Marshal(trigger)
	if err != nil {
		return fmt.Errorf("encode trigger: %w", err)
	}
	if _, err := js.Publish(ctx, subject, data); err != nil {
		return fmt.Errorf("publish trigger: %w", err)
	}
	return nc.Flush()
}
