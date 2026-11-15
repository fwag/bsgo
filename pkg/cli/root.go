package cli

import (
	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/spf13/cobra"
)

// RootOption customizes NewRootCommand.
type RootOption func(*rootConfig)

type rootConfig struct{ buildRepo BuildRepoOpener }

// WithBuildRepoOpener replaces the environment-driven S3/NATS connection of
// "admin buildrepo" (used by tests).
func WithBuildRepoOpener(o BuildRepoOpener) RootOption {
	return func(c *rootConfig) { c.buildRepo = o }
}

func NewRootCommand(store storage.Store, options ...RootOption) *cobra.Command {
	cfg := rootConfig{buildRepo: openBuildRepoFromEnv}
	for _, o := range options {
		o(&cfg)
	}
	var opts OutputOptions

	rootCmd := &cobra.Command{
		Use:          "bsgo",
		Short:        "bsgo - Cloud-Native Build Service CLI",
		Long:         "A cloud-native CLI for the Open Build Service rewrite in Go.\nSupports interactive human views and deterministic machine-readable JSON for agents.",
		SilenceUsage: true,
	}

	rootCmd.PersistentFlags().BoolVar(&opts.JSON, "json", false, "Output machine-readable JSON (ideal for CI and AI agents)")
	rootCmd.PersistentFlags().BoolVarP(&opts.Quiet, "quiet", "q", false, "Output quiet IDs/status only")

	rootCmd.AddCommand(newProjectCmd(store, &opts))
	rootCmd.AddCommand(newPackageCmd(store, &opts))
	rootCmd.AddCommand(newAdminCmd(store, &opts, cfg.buildRepo))
	rootCmd.AddCommand(newAuthCmd(&opts))
	rootCmd.AddCommand(newWatchCmd(store, &opts))
	rootCmd.AddCommand(newJobCmd(&opts))

	return rootCmd
}
