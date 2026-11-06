package main

import (
	"fmt"
	"os"

	"github.com/openSUSE/bsgo/pkg/cli"
	"github.com/openSUSE/bsgo/pkg/storage"
)

func main() {
	store := storage.NewMemoryStore()
	rootCmd := cli.NewRootCommand(store)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
