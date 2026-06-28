package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:           "binpazer",
	Short:         "Inspect, validate, and author binpazer files",
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute runs the CLI, exiting non-zero on error.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
