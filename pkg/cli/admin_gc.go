package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/google/uuid"
	"github.com/openSUSE/bsgo/pkg/gc"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/scheduler/coord"
	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/openSUSE/bsgo/pkg/storage/s3"
	"github.com/spf13/cobra"
)

// GCAdmin is what "admin gc" needs besides the flags: a collector wired to
// object storage, the metadata store and the run lease.
type GCAdmin struct {
	Collector *gc.Collector
	// Close releases connections; it may be nil.
	Close func()
}

// GCOpener connects the garbage collection backends.
type GCOpener func(ctx context.Context, store storage.Store) (*GCAdmin, error)

// openGCFromEnv connects to S3 (S3_ENDPOINT, S3_BUCKET, credentials as the
// server) and NATS (NATS_URL) for the run lease shared with the server's own
// GC loop. The pointer table is the store the CLI was started with.
func openGCFromEnv(ctx context.Context, store storage.Store) (*GCAdmin, error) {
	st, err := s3.NewS3Storage(ctx, envOr("S3_ENDPOINT", "http://localhost:3900"), "garage", envOr("S3_BUCKET", "bsgo-artifacts"), "", "")
	if err != nil {
		return nil, err
	}
	nc, js, err := natsqueue.ConnectJetStream(envOr("NATS_URL", "nats://localhost:4222"))
	if err != nil {
		return nil, err
	}
	leases, err := coord.NewNATS(ctx, js, 30*time.Second)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("gc lease: %w", err)
	}
	return &GCAdmin{
		Collector: &gc.Collector{
			Objects: s3.GCObjects{Store: st}, Store: store, Leases: leases,
			Owner: "bsgo-cli-gc-" + uuid.NewString(),
		},
		Close: nc.Close,
	}, nil
}

type adminGCFlags struct {
	prefix       string
	project      string
	grace        time.Duration
	keepVersions int
	dryRun       bool
	yes          bool
}

func (f adminGCFlags) prefixes() ([]string, error) {
	switch f.prefix {
	case "all":
		return []string{gc.PrefixBuildRepo, gc.PrefixArtifacts}, nil
	case gc.PrefixBuildRepo, gc.PrefixArtifacts:
		return []string{f.prefix}, nil
	}
	return nil, fmt.Errorf("invalid --prefix %q: want buildrepo, artifacts or all", f.prefix)
}

func (f adminGCFlags) options() gc.Options {
	return gc.Options{Grace: f.grace, KeepVersions: f.keepVersions, Project: f.project}
}

func newAdminGCCmd(store storage.Store, opts *OutputOptions, open GCOpener) *cobra.Command {
	var flags adminGCFlags
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Delete unreachable build repository indexes and superseded build artifacts",
		Long: `Garbage-collect the artifact bucket. Nothing in it is deleted by builds, so
superseded objects accumulate:

  buildrepo  every version of every build repository index (<hash>.solv and
             <hash>.entries.json); only the current one per prpa is needed.
  artifacts  the binaries of old builds of every unit
             (artifacts/<project>/<repo>/<arch>/<unit>/).

An object is deleted only when no current pointer can reach it and it is older
than --grace:
  - buildrepo: the pair referenced by the build_repo_index row of a prpa is kept,
    plus the --keep-index-versions most recent superseded pairs of that prpa. All
    objects of a prpa without a row are garbage.
  - artifacts: an RPM is kept when the current entries.json of ANY prpa lists it
    (that covers binaries exported to other architectures), when it is part of the
    unit's current build (listed in its current bininfo.json, including debuginfo,
    debugsource and source RPMs), or when it is one of the unit's metadata files
    (bininfo.json, _slsa_provenance.json, _statistics, build.log). Old files of
    units that no longer exist are garbage.
repos/, backup/, recipes/ and dod/ are never touched.

Only one GC run happens at a time: the run takes the "gc" lease in NATS KV, which
the server's background loop (--gc-interval) uses as well. Pointers are read again
right before objects are deleted.

Without --yes the command asks for confirmation on an interactive terminal and
fails otherwise; --dry-run never deletes and needs no confirmation. With --json
stdout carries the per-prpa plan or result (kept, delete, free_bytes, deleted) and
nothing else; diagnostics go to stderr.

Needs S3_ENDPOINT, S3_BUCKET and S3 credentials, NATS_URL for the lease and the
database of the other admin commands. Access tokens need the scope admin:gc.

Examples:
  # Review what would be deleted, machine readable:
  bsgo admin gc --prefix all --dry-run --json > gc-plan.json

  # Clean the build repository indexes of one project without asking:
  bsgo admin gc --prefix buildrepo --project SUSE:SLFO:1.2 --yes

  # Delete superseded artifacts, keeping everything younger than two days:
  bsgo admin gc --prefix artifacts --grace 48h --yes --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAdminGC(cmd, store, opts, open, flags)
		},
	}
	cmd.Flags().StringVar(&flags.prefix, "prefix", "all", "What to collect: buildrepo, artifacts or all")
	cmd.Flags().StringVar(&flags.project, "project", "", "Restrict the run to one project (default: all projects)")
	cmd.Flags().DurationVar(&flags.grace, "grace", gc.DefaultGrace, "Never delete objects younger than this")
	cmd.Flags().IntVar(&flags.keepVersions, "keep-index-versions", gc.DefaultKeepVersions, "Superseded index versions kept per prpa besides the current one")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "Compute and report the plan but delete nothing")
	cmd.Flags().BoolVarP(&flags.yes, "yes", "y", false, "Delete without asking for confirmation")
	return cmd
}

func runAdminGC(cmd *cobra.Command, store storage.Store, opts *OutputOptions, open GCOpener, flags adminGCFlags) error {
	prefixes, err := flags.prefixes()
	if err != nil {
		return err
	}
	interactive := isTerminal(cmd.InOrStdin()) && !opts.JSON
	if !flags.dryRun && !flags.yes && !interactive {
		return errors.New("refusing to delete without confirmation: pass --yes to delete or --dry-run to only report")
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	adm, err := open(ctx, store)
	if err != nil {
		return fmt.Errorf("connecting garbage collection backends: %w", err)
	}
	if adm.Close != nil {
		defer adm.Close()
	}
	adm.Collector.Logf = func(format string, args ...any) { fmt.Fprintf(cmd.ErrOrStderr(), format+"\n", args...) }

	if !flags.dryRun && !flags.yes {
		ok, err := confirmGC(ctx, cmd, adm.Collector, prefixes, flags.options())
		if err != nil || !ok {
			return err
		}
	}
	var results []gc.Result
	if flags.dryRun {
		results, err = adm.Collector.Plan(ctx, prefixes, flags.options())
	} else {
		results, err = adm.Collector.Run(ctx, prefixes, flags.options())
	}
	if renderErr := renderGC(cmd.OutOrStdout(), results, flags.dryRun, *opts); renderErr != nil {
		return renderErr
	}
	if err != nil {
		return err
	}
	for _, r := range results {
		if r.Errors > 0 {
			return fmt.Errorf("%s: %d prpa(s) failed, see diagnostics", r.Prefix, r.Errors)
		}
	}
	return nil
}

// confirmGC shows what a run would delete and asks whether to go on.
func confirmGC(ctx context.Context, cmd *cobra.Command, c *gc.Collector, prefixes []string, o gc.Options) (bool, error) {
	plan, err := c.Plan(ctx, prefixes, o)
	if err != nil {
		return false, err
	}
	if err := renderGC(cmd.ErrOrStderr(), plan, true, OutputOptions{}); err != nil {
		return false, err
	}
	return confirmDeletion(cmd.InOrStdin(), cmd.ErrOrStderr())
}

func confirmDeletion(in io.Reader, w io.Writer) (bool, error) {
	fmt.Fprint(w, "Delete these objects? [y/N] ")
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	fmt.Fprintln(w, "aborted")
	return false, nil
}

func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(f.Fd())
}

// gcReport is the deterministic JSON document of a run.
type gcReport struct {
	DryRun  bool        `json:"dry_run"`
	Results []gc.Result `json:"results"`
}

func renderGC(w io.Writer, results []gc.Result, dryRun bool, opts OutputOptions) error {
	if opts.JSON {
		if results == nil {
			results = []gc.Result{}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(gcReport{DryRun: dryRun, Results: results})
	}
	verb := "deleted"
	if dryRun {
		verb = "would delete"
	}
	for _, r := range results {
		fmt.Fprintf(w, "%s: kept %d, %s %d objects, free %s\n", r.Prefix, r.Kept, verb, r.Deleted, gc.FormatBytes(r.FreedBytes))
		for _, p := range r.Plans {
			if len(p.Delete) == 0 && p.Error == "" {
				continue
			}
			fmt.Fprintf(w, "  %s: kept %d, %s %d, %s\n", p.Prpa, p.Kept, verb, p.Deleted, gc.FormatBytes(p.FreeBytes))
		}
	}
	return nil
}
