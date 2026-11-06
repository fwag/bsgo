package cli

import (
	"github.com/openSUSE/bsgo/pkg/storage"
	"github.com/spf13/cobra"
)

func NewRootCommand(store storage.Store) *cobra.Command {
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

	return rootCmd
}
