// Rescale Interlink - Unified CLI and GUI Tool for Rescale platform
// FIPS 140-3 Compliant Build Required
//
// v4.0.3: True server-side pagination with page caching, status message fix.
// - No args + display available → GUI mode
// - No args + no display → CLI help
// - --gui → GUI mode
// - --cli → CLI mode (force)
// - CLI subcommands/flags → CLI mode
//
// Build with: wails build (for all platforms)
package main

import (
	"crypto/fips140"
	"embed"
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"

	// CRITICAL: mesainit MUST be imported FIRST (before any OpenGL packages)
	// Go runs init() functions in import order. By importing mesainit first,
	// its init() runs before any OpenGL usage, allowing us to preload Mesa's
	// opengl32.dll before Windows loads System32's version.
	_ "github.com/rescale/rescale-int/internal/mesainit"

	"github.com/rescale/rescale-int/internal/cli"
	"github.com/rescale/rescale-int/internal/mesa"
	"github.com/rescale/rescale-int/internal/version"
	"github.com/rescale/rescale-int/internal/wailsapp"
)

// Version information
var (
	Version   = "v4.3.8"
	BuildTime = "2026-01-14"
)

// FIPSEnabled indicates whether FIPS 140-3 mode is active
var FIPSEnabled bool

//go:embed all:frontend/dist
var assets embed.FS

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
			fmt.Fprintf(os.Stderr, "Or manually: GOFIPS140=latest wails build\n")
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

	// v4.0.1: Smart CLI vs GUI mode detection
	if isCLIMode() {
		// CLI mode - use the proper CLI root command with all persistent flags
		if err := cli.Execute(); err != nil {
			os.Exit(1)
		}
		return
	}

	// GUI mode - launch Wails GUI
	wailsapp.Assets = assets
	if err := wailsapp.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// isCLIMode determines whether to run in CLI mode based on arguments and environment.
// v4.0.1: Smart detection for intuitive user experience.
//
// CLI mode when:
// - --cli flag is present (force CLI mode)
// - CLI subcommands are present (jobs, files, folders, etc.)
// - CLI flags are present (--help, --version, -h, -v)
// - No display available (DISPLAY/WAYLAND_DISPLAY not set on Linux)
//
// GUI mode when:
// - --gui flag is present (force GUI mode)
// - No arguments and display is available
func isCLIMode() bool {
	// Explicit flags
	if contains(os.Args, "--cli") {
		return true
	}
	if contains(os.Args, "--gui") {
		return false
	}

	// CLI subcommands and flags that indicate CLI mode
	cliPatterns := []string{
		// Subcommands
		"jobs", "files", "folders", "upload", "download",
		"hardware", "software", "config", "pur", "completion",
		// Flags
		"--help", "-h", "--version", "-v",
	}

	for _, arg := range os.Args[1:] {
		for _, pattern := range cliPatterns {
			if arg == pattern || strings.HasPrefix(arg, pattern+" ") {
				return true
			}
		}
	}

	// No explicit mode or commands - check for display
	if len(os.Args) == 1 {
		// No arguments: default to GUI if display available, CLI otherwise
		if runtime.GOOS == "linux" {
			// On Linux, check for display
			if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
				return true // No display, default to CLI
			}
		}
		// On macOS/Windows or Linux with display: default to GUI
		return false
	}

	// Unknown arguments - let CLI handle (might be typos or new commands)
	// This ensures ./AppRun somethingUnknown shows CLI help rather than opening GUI
	return true
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
