// Rescale Interlink - Unified CLI and GUI Tool
// Version 2.4.8 - Optimized Job Downloads (v2 API + No GetFileInfo)
package main

import (
	"fmt"
	"os"

	"github.com/rescale/rescale-int/internal/cli"
	"github.com/rescale/rescale-int/internal/gui"
)

// Version information
var (
	Version   = "2.4.8"
	BuildTime = "2025-11-20"
)

func main() {
	// Set version in CLI package
	cli.Version = Version
	cli.BuildTime = BuildTime

	// Check for GUI mode before creating command
	guiMode := contains(os.Args, "--gui")

	if guiMode {
		// Launch GUI mode directly
		if err := gui.Run(os.Args); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// CLI mode - use the proper CLI root command with all persistent flags
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}

// contains checks if a string slice contains a specific value.
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
