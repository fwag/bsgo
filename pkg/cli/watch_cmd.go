package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nats-io/nats.go"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/spf13/cobra"
)

func watchLong() string {
	return `Watch the build state of one project.

The data comes from the control-plane database (table package_build_state),
polled every --interval, so one row is one package unit (package[:flavor]) in
one repo and arch with its current or last job. NATS is only used for live
build logs, worker names and to refresh immediately on job status messages.

Interactive keys (TUI):
  b  building (default)   s  scheduled   f  failed   u  unresolvable
  k  blocked              a  all states  m  architecture matrix
  /  search by package substring (Enter keeps it, Esc clears it)
  ?  legend of the state letters
  Up/Down  select         Enter or l  logs of the selected job (Esc closes)
  q  quit

State letters (as in osc results): ` + stateLegend() + `.
The header shows the counts per repository and architecture with these letters;
the worker fleet panel lists every active job of each worker.

Non-interactive:
  --once            print the current snapshot of --state (default building)
                    as a plain table and exit; with --json one deterministic
                    JSON document on stdout, no ANSI
  --history <pkg>   earlier jobs of that package, newest first, read from the
                    JOB_STATUS stream (needs NATS; at most --limit jobs)

Valid values for --state: ` + watchAllStates + ", " + strings.Join(storage.ValidBuildStates, ", ") + `.`
}

const watchExample = `  bsgo watch SUSE:SLFO:1.2
  bsgo watch SUSE:SLFO:1.2 --repo standard --arch x86_64 --interval 5s
  bsgo watch SUSE:SLFO:1.2 --once --state failed --json
  bsgo watch SUSE:SLFO:1.2 --history glibc --limit 5 --json`

type watchParams struct {
	Project  string
	Repo     string
	Arch     string
	State    string
	Interval time.Duration
	Once     bool
	History  string
	Limit    int
	NATSURL  string
}

// WatchResult is the machine contract of `bsgo watch --once --json`.
type WatchResult struct {
	Project string         `json:"project"`
	Repo    string         `json:"repo,omitempty"`
	Arch    string         `json:"arch,omitempty"`
	State   string         `json:"state"`
	Counts  map[string]int `json:"counts"`
	// Repositories has one entry per repo/arch with its state counts.
	Repositories []RepoArchSummary   `json:"repositories"`
	Packages     []PackageStateEntry `json:"packages"`
}

// WatchHistoryResult is the machine contract of `bsgo watch --history --json`.
type WatchHistoryResult struct {
	Project string         `json:"project"`
	Package string         `json:"package"`
	Jobs    []HistoryEntry `json:"jobs"`
}

func newWatchCmd(store storage.Store, opts *OutputOptions) *cobra.Command {
	defaultNATSURL := os.Getenv("NATS_URL")
	if defaultNATSURL == "" {
		defaultNATSURL = "nats://localhost:4222"
	}
	p := watchParams{}
	var projectFlag string

	cmd := &cobra.Command{
		Use:     "watch <project>",
		Short:   "Watch the build state of a project (live TUI, --once snapshot, --history)",
		Long:    watchLong(),
		Example: watchExample,
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := resolveWatchProject(args, projectFlag)
			if err != nil {
				return err
			}
			p.Project = project
			return runWatch(cmd, store, opts, p)
		},
	}

	cmd.Flags().StringVarP(&projectFlag, "project", "p", "", "Project to watch (alias of the positional argument)")
	cmd.Flags().StringVar(&p.Repo, "repo", "", "Only show this repository (build target)")
	cmd.Flags().StringVarP(&p.Arch, "arch", "a", "", "Only show this architecture")
	cmd.Flags().StringVar(&p.State, "state", storage.StateBuilding, "Initial state filter ("+watchAllStates+" for every state)")
	cmd.Flags().DurationVar(&p.Interval, "interval", 2*time.Second, "How often the TUI polls the build state table")
	cmd.Flags().BoolVar(&p.Once, "once", false, "Print the current snapshot and exit (no TUI)")
	cmd.Flags().StringVar(&p.History, "history", "", "List earlier jobs of this package from the JOB_STATUS stream and exit")
	cmd.Flags().IntVar(&p.Limit, "limit", 20, "Maximum number of jobs listed by --history")
	cmd.Flags().StringVar(&p.NATSURL, "nats-url", defaultNATSURL, "NATS JetStream connection URL (logs, workers, --history)")
	return cmd
}

// resolveWatchProject merges the positional project with -p/--project.
func resolveWatchProject(args []string, flag string) (string, error) {
	if len(args) == 0 {
		return flag, nil
	}
	if flag != "" && flag != args[0] {
		return "", fmt.Errorf("conflicting projects %q (argument) and %q (--project)", args[0], flag)
	}
	return args[0], nil
}

func runWatch(cmd *cobra.Command, store storage.Store, opts *OutputOptions, p watchParams) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateWatchProject(ctx, store, p.Project); err != nil {
		return err
	}
	state, err := parseWatchState(p.State)
	if err != nil {
		return err
	}
	if p.Interval <= 0 {
		return fmt.Errorf("--interval must be positive, got %s", p.Interval)
	}
	src := storeStateSource{store: store, now: time.Now}
	switch {
	case p.History != "":
		return runWatchHistory(ctx, cmd.OutOrStdout(), p, *opts)
	case p.Once:
		return runWatchOnce(ctx, cmd.OutOrStdout(), src, p, state, *opts)
	}
	return runWatchTUI(ctx, src, p, state)
}

// runWatchOnce prints the snapshot of one state and returns.
func runWatchOnce(ctx context.Context, w io.Writer, src stateSource, p watchParams, state string, opts OutputOptions) error {
	snap, err := src.Snapshot(ctx, p.Project, storage.BuildStateFilter{Repo: p.Repo, Arch: p.Arch, State: state})
	if err != nil {
		return err
	}
	rows := sortEntries(filterEntries(snap.Rows, state, ""), false)
	label := state
	if label == "" {
		label = watchAllStates
	}
	res := WatchResult{Project: p.Project, Repo: p.Repo, Arch: p.Arch, State: label, Counts: snap.Counts, Packages: rows}
	if res.Counts == nil {
		res.Counts = map[string]int{}
	}
	res.Repositories = snap.Summaries
	if res.Repositories == nil {
		res.Repositories = []RepoArchSummary{}
	}
	human := fmt.Sprintf("%s: %s\n%s%s", p.Project, formatCounts(snap.Counts), formatSummaryLines(snap.Summaries), formatEntriesTable(rows))
	return Render(w, human, res, fmt.Sprintf("%d", len(rows)), opts)
}

func runWatchHistory(ctx context.Context, w io.Writer, p watchParams, opts OutputOptions) error {
	if p.Limit <= 0 {
		return fmt.Errorf("--limit must be positive, got %d", p.Limit)
	}
	nc, js, err := natsqueue.ConnectJetStream(p.NATSURL)
	if err != nil {
		return fmt.Errorf("failed to connect to NATS at %s: %w", p.NATSURL, err)
	}
	defer nc.Close()
	stream, err := js.Stream(ctx, natsqueue.StreamJobStatus)
	if err != nil {
		return fmt.Errorf("failed to open stream %s: %w", natsqueue.StreamJobStatus, err)
	}
	return writeHistory(ctx, w, jetStreamStatusLog{stream: stream}, p, opts)
}

func writeHistory(ctx context.Context, w io.Writer, log statusLog, p watchParams, opts OutputOptions) error {
	jobs, err := loadHistory(ctx, log, historyQuery{Project: p.Project, Package: p.History, Repo: p.Repo, Arch: p.Arch, Limit: p.Limit})
	if err != nil {
		return err
	}
	if jobs == nil {
		jobs = []HistoryEntry{}
	}
	res := WatchHistoryResult{Project: p.Project, Package: p.History, Jobs: jobs}
	return Render(w, formatHistoryTable(jobs), res, fmt.Sprintf("%d", len(jobs)), opts)
}

// runWatchTUI runs the interactive dashboard. NATS is optional: without it
// the dashboard still shows the state table but no logs or worker names.
func runWatchTUI(ctx context.Context, src stateSource, p watchParams, state string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var logs logSource
	var nc *nats.Conn
	note := ""
	conn, _, err := natsqueue.ConnectJetStream(p.NATSURL)
	if err != nil {
		note = "NATS unavailable: logs and worker names are disabled"
	} else {
		nc = conn
		defer nc.Close()
		logs = natsLogSource{nc: nc}
	}

	m := newWatchModel(ctx, src, logs, p, state)
	m.note = note
	prog := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	m.send = prog.Send
	if nc != nil {
		if err := subscribeWatchFeeds(nc, prog.Send); err != nil {
			return err
		}
	}
	if _, err := prog.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}
	return nil
}
