// Rescale Interlink - Unified CLI and GUI Tool for Rescale platform
// FIPS 140-3 Compliant Build Required
package main

import (
	"crypto/fips140"
	"fmt"
	"log"
	"os"

	"github.com/rescale/rescale-int/internal/cli"
	"github.com/rescale/rescale-int/internal/gui"
	"github.com/rescale/rescale-int/internal/mesa"
)

// Version information
var (
	Version   = "v3.4.9"
	BuildTime = "2025-12-21"
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
	// Set version in CLI package
	cli.Version = Version
	cli.BuildTime = BuildTime

	// Check for diagnostic modes before GUI/CLI
	if contains(os.Args, "--mesa-doctor") {
		fmt.Printf("Rescale Interlink %s\n\n", Version)
		mesa.Doctor()
		return
	}

	// Enable timing output (works with both GUI and CLI)
	if contains(os.Args, "--timing") {
		os.Setenv("RESCALE_TIMING", "1")
	}

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
