package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	natsqueue "github.com/openSUSE/bsgo/pkg/queue/nats"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
	"github.com/openSUSE/bsgo/pkg/scheduler/coord"
	"github.com/openSUSE/bsgo/pkg/scheduler/indexer"
	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/openSUSE/bsgo/pkg/storage/s3"
	"github.com/spf13/cobra"
)

// BuildRepoAdmin is what "admin buildrepo" needs besides the pointer store:
// an indexer wired to object storage and writer leases, and the listing of
// the units that have uploaded results.
type BuildRepoAdmin struct {
	Indexer *indexer.Indexer
	// ListKeys returns the object keys below a prefix.
	ListKeys func(ctx context.Context, prefix string) ([]string, error)
	// Close releases connections; it may be nil.
	Close func()
}

// BuildRepoOpener connects the build repository backends.
type BuildRepoOpener func(ctx context.Context, store storage.Store) (*BuildRepoAdmin, error)

// openBuildRepoFromEnv connects to S3 (S3_ENDPOINT, S3_BUCKET, credentials as
// the server) and NATS (NATS_URL) for the writer lease and the repo-changed
// event.
func openBuildRepoFromEnv(ctx context.Context, store storage.Store) (*BuildRepoAdmin, error) {
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
		return nil, fmt.Errorf("writer leases: %w", err)
	}
	blobs := s3.Blobs{Store: st}
	return &BuildRepoAdmin{
		Indexer: &indexer.Indexer{
			Pointers: store, Blobs: blobs, Reader: buildrepo.NewReader(store, blobs),
			Leases: leases, Owner: "bsgo-cli-" + fmt.Sprint(os.Getpid()), Notify: natsNotifier{nc: nc},
			Exports: indexer.ManifestExports{Projects: store},
		},
		ListKeys: blobs.ListKeys,
		Close:    nc.Close,
	}, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type natsNotifier struct {
	nc *nats.Conn
}

// RepoChanged implements indexer.Notifier.
func (n natsNotifier) RepoChanged(_ context.Context, p buildrepo.Prpa) error {
	data, err := indexer.EncodeEvent(p)
	if err != nil {
		return err
	}
	return n.nc.Publish(natsqueue.SubjectEventsRepoChanged, data)
}

func newAdminBuildRepoCmd(store storage.Store, opts *OutputOptions, open BuildRepoOpener) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "buildrepo",
		Short: "Inspect and repair build repository indexes",
		Long: `A build repository is the set of binaries built inside one project, repository
and architecture. Its index is kept up to date incrementally whenever a build
finishes; the commands here repair it.`,
	}
	cmd.AddCommand(newAdminBuildRepoRebuildCmd(store, opts, open))
	return cmd
}

func newAdminBuildRepoRebuildCmd(store storage.Store, opts *OutputOptions, open BuildRepoOpener) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "rebuild <project> <repository> <arch>",
		Short: "Rebuild a build repository index from the stored binaries of all units",
		Long: `Recreate the index of one build repository from scratch: the bininfo.json of every
unit that has uploaded results under artifacts/<project>/<repository>/<arch>/ is
read and a new libsolv blob and entry list are written. Use it to repair an
index that was damaged or that predates the unit-aware format; normal builds
only update the index incrementally.

The rebuild takes the same writer lease as a finishing build and announces the
change to the scheduler. With --dry-run nothing is written and no event is sent;
the result shows the sizes and hashes the rebuild would produce.

Needs S3_ENDPOINT, S3_BUCKET and S3 credentials, and NATS_URL for the lease.

Examples:
  bsgo admin buildrepo rebuild SUSE:SLFO:1.2 standard x86_64 --dry-run
  bsgo admin buildrepo rebuild SUSE:SLFO:1.2 standard x86_64 --json`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := buildrepo.Prpa{Project: args[0], Repo: args[1], Arch: args[2]}
			res, err := runBuildRepoRebuild(cmd.Context(), store, open, p, dryRun)
			if err != nil {
				return err
			}
			return renderRebuild(cmd.OutOrStdout(), res, *opts)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Compute the new index but write nothing and send no event")
	return cmd
}

func runBuildRepoRebuild(ctx context.Context, store storage.Store, open BuildRepoOpener, p buildrepo.Prpa, dryRun bool) (*indexer.RebuildResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !p.Valid() {
		return nil, fmt.Errorf("project, repository and architecture are required")
	}
	adm, err := open(ctx, store)
	if err != nil {
		return nil, fmt.Errorf("connecting build repository backends: %w", err)
	}
	if adm.Close != nil {
		defer adm.Close()
	}
	prefix := buildrepo.ArtifactPrefix(p, "")
	keys, err := adm.ListKeys(ctx, strings.TrimSuffix(prefix, "/"))
	if err != nil {
		return nil, err
	}
	units := indexer.UnitsFromKeys(p, keys)
	if len(units) == 0 {
		return nil, fmt.Errorf("no unit has uploaded results for %s", p)
	}
	return adm.Indexer.Rebuild(ctx, p, units, dryRun)
}

func renderRebuild(w io.Writer, res *indexer.RebuildResult, opts OutputOptions) error {
	if opts.JSON {
		return json.NewEncoder(w).Encode(res)
	}
	mode := "rebuilt"
	if res.DryRun {
		mode = "would rebuild (dry run)"
	}
	_, err := fmt.Fprintf(w, "%s %s: %d units, %d binaries, solv %d bytes, entries %d bytes, changed=%v\n  old %s\n  new %s\n",
		mode, res.Prpa, res.Units, res.Binaries, res.SolvBytes, res.EntriesBytes, res.Changed, res.OldHash, res.NewHash)
	return err
}
