package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "bsgo",
	Short: "bsgo - Cloud-Native Build Service CLI",
	Long:  "A modern, cloud-native CLI for the Open Build Service rewrite in Go.\nSupports interactive Charm TUI dashboards, developer workflows, and deterministic machine-readable JSON for CI/agents.",
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output machine-readable JSON (ideal for CI, scripts, and AI agents)")
	rootCmd.PersistentFlags().BoolVarP(&quietOutput, "quiet", "q", false, "Output quiet IDs/status only")
	rootCmd.PersistentFlags().StringVar(&serverAddr, "server", "https://bsgo-api.infra.local", "bsgo API control plane server address")

	rootCmd.AddCommand(projectCmd)
	rootCmd.AddCommand(packageCmd)
	rootCmd.AddCommand(buildCmd)
	rootCmd.AddCommand(watchCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
