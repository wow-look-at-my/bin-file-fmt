package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is the release version, overridable at build time via -ldflags "-X main.version=v1.2.3".
var version = "dev"

// versionString returns version, appending the VCS revision stamped into the
// binary (if any) when no explicit version was injected.
func versionString() string {
	if version == "dev" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				if s.Key == "vcs.revision" && len(s.Value) >= 8 {
					return version + " (" + s.Value[:8] + ")"
				}
			}
		}
	}
	return version
}

var rootCmd = &cobra.Command{
	Use:           "binpazer",
	Short:         "Inspect, validate, and author binpazer files",
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() { rootCmd.Version = versionString() }

// Execute runs the CLI, exiting non-zero on error.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
