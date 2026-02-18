// Package fips provides shared FIPS 140-3 compliance initialization
// for both the Wails GUI and standalone CLI entry points.
//
// v4.6.6: Extracted from duplicated init() logic in main.go and cmd/rescale-int/main.go
package fips

import (
	"crypto/fips140"
	"fmt"
	"log"
	"os"
)

// Enabled reports whether FIPS 140-3 mode is active after Init has been called.
// It is set once by Init and should be treated as read-only thereafter.
var Enabled bool

// Init checks FIPS 140-3 compliance status at startup.
// FIPS 140-3 is REQUIRED for Rescale Interlink (FedRAMP compliance).
//
// buildType should be "wails" for the GUI binary or "cli" for the standalone
// CLI binary; it controls the rebuild hint shown when FIPS is not active.
//
// If FIPS is not enabled and RESCALE_ALLOW_NON_FIPS is not set to "true",
// Init prints an error to stderr and calls os.Exit(2).
func Init(buildType string) {
	Enabled = fips140.Enabled()
	if Enabled {
		return
	}

	// FIPS is NOT active - this is a compliance issue
	log.Printf("[CRITICAL] FIPS 140-3 mode is NOT active")
	log.Printf("[CRITICAL] This binary was NOT built with GOFIPS140=latest")
	log.Printf("[CRITICAL] FedRAMP compliance REQUIRES FIPS 140-3 mode")

	// Check if non-FIPS mode is explicitly allowed (for development only)
	if os.Getenv("RESCALE_ALLOW_NON_FIPS") == "true" {
		log.Printf("[WARN] Running without FIPS due to RESCALE_ALLOW_NON_FIPS=true (DEVELOPMENT ONLY)")
		return
	}

	rebuildHint := "GOFIPS140=latest go build ./cmd/rescale-int"
	if buildType == "wails" {
		rebuildHint = "GOFIPS140=latest wails build"
	}

	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "ERROR: FIPS 140-3 compliance is REQUIRED.\n")
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "This binary was NOT built with FIPS support enabled.\n")
	fmt.Fprintf(os.Stderr, "Please rebuild using: make build\n")
	fmt.Fprintf(os.Stderr, "Or manually: %s\n", rebuildHint)
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "For development ONLY, set RESCALE_ALLOW_NON_FIPS=true to bypass.\n")
	fmt.Fprintf(os.Stderr, "\n")
	os.Exit(2) // Exit with code 2 to indicate compliance failure
}
