// Rescale Interlink - Unified CLI and GUI Tool for Rescale platform
// FIPS 140-3 Compliant Build Required
package main

import (
	"fmt"
	"os"
	"slices"

	intfips "github.com/rescale/rescale-int/internal/fips"

	"github.com/rescale/rescale-int/internal/cli"
	"github.com/rescale/rescale-int/internal/version"
)

func init() {
	// Shared FIPS 140-3 compliance check (common to GUI and CLI binaries)
	intfips.Init("cli")
}

func main() {
	// Propagate version from the single source of truth (internal/version)
	// to CLI package for backwards compatibility
	cli.Version = version.Version
	cli.BuildTime = version.BuildTime

	// Enable timing output
	if slices.Contains(os.Args, "--timing") {
		os.Setenv("RESCALE_TIMING", "1")
	}

	// v4.0.2: This is the standalone CLI binary.
	// For GUI, use rescale-int-gui (or rescale-int-gui.AppImage on Linux).
	if slices.Contains(os.Args, "--gui") {
		fmt.Fprintf(os.Stderr, "Error: --gui is not available in the CLI-only binary.\n")
		fmt.Fprintf(os.Stderr, "Use rescale-int-gui for the graphical interface.\n")
		os.Exit(1)
	}

	// CLI mode - use the proper CLI root command with all persistent flags
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
