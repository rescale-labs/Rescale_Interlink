// Rescale Interlink - Unified CLI and GUI Tool for Rescale platform
// FIPS 140-3 Compliant Build Required
package main

import (
	"crypto/fips140"
	"fmt"
	"log"
	"os"

	"github.com/rescale/rescale-int/internal/cli"
	"github.com/rescale/rescale-int/internal/version"
)

// Version information
var (
	Version   = "v4.0.2"
	BuildTime = "2026-01-01"
)

// FIPSEnabled indicates whether FIPS 140-3 mode is active
var FIPSEnabled bool

func init() {
	// Check FIPS 140-3 compliance status at startup
	// FIPS 140-3 is REQUIRED for Rescale Interlink (FedRAMP compliance)
	FIPSEnabled = fips140.Enabled()
	if !FIPSEnabled {
		// FIPS is NOT active - this is a compliance issue
		log.Printf("[CRITICAL] FIPS 140-3 mode is NOT active")
		log.Printf("[CRITICAL] This binary was NOT built with GOFIPS140=latest")
		log.Printf("[CRITICAL] FedRAMP compliance REQUIRES FIPS 140-3 mode")

		// Check if non-FIPS mode is explicitly allowed (for development only)
		if os.Getenv("RESCALE_ALLOW_NON_FIPS") == "true" {
			log.Printf("[WARN] Running without FIPS due to RESCALE_ALLOW_NON_FIPS=true (DEVELOPMENT ONLY)")
		} else {
			fmt.Fprintf(os.Stderr, "\n")
			fmt.Fprintf(os.Stderr, "ERROR: FIPS 140-3 compliance is REQUIRED.\n")
			fmt.Fprintf(os.Stderr, "\n")
			fmt.Fprintf(os.Stderr, "This binary was NOT built with FIPS support enabled.\n")
			fmt.Fprintf(os.Stderr, "Please rebuild using: make build\n")
			fmt.Fprintf(os.Stderr, "Or manually: GOFIPS140=latest go build ./cmd/rescale-int\n")
			fmt.Fprintf(os.Stderr, "\n")
			fmt.Fprintf(os.Stderr, "For development ONLY, set RESCALE_ALLOW_NON_FIPS=true to bypass.\n")
			fmt.Fprintf(os.Stderr, "\n")
			os.Exit(2) // Exit with code 2 to indicate compliance failure
		}
	}
}

func main() {
	// Set version in version package (canonical source for all packages)
	// and CLI package (for backwards compatibility)
	version.Version = Version
	version.BuildTime = BuildTime
	cli.Version = Version
	cli.BuildTime = BuildTime

	// Enable timing output
	if contains(os.Args, "--timing") {
		os.Setenv("RESCALE_TIMING", "1")
	}

	// v4.0.2: This is the standalone CLI binary.
	// For GUI, use rescale-int-gui (or rescale-int-gui.AppImage on Linux).
	if contains(os.Args, "--gui") {
		fmt.Fprintf(os.Stderr, "Error: --gui is not available in the CLI-only binary.\n")
		fmt.Fprintf(os.Stderr, "Use rescale-int-gui for the graphical interface.\n")
		os.Exit(1)
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
