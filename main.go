// Rescale Interlink - Unified CLI and GUI Tool for Rescale platform
// FIPS 140-3 Compliant Build Required
//
// v4.7.1: Disk space error UX (banner + short labels) and settings reorganization (workers/tar from Setup to PUR/SingleJob).
// v4.6.8: Fix automation JSON format, single job all input modes, GTK warnings, terminology.
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
	"embed"
	"fmt"
	"log"
	"os"
	"runtime"
	"slices"
	"strings"

	// CRITICAL: mesainit MUST be imported FIRST (before any OpenGL packages)
	// Go runs init() functions in import order. By importing mesainit first,
	// its init() runs before any OpenGL usage, allowing us to preload Mesa's
	// opengl32.dll before Windows loads System32's version.
	_ "github.com/rescale/rescale-int/internal/mesainit"

	"github.com/rescale/rescale-int/internal/cli"
	"github.com/rescale/rescale-int/internal/config"
	intfips "github.com/rescale/rescale-int/internal/fips"
	"github.com/rescale/rescale-int/internal/mesa"
	"github.com/rescale/rescale-int/internal/version"
	"github.com/rescale/rescale-int/internal/wailsapp"
)

//go:embed all:frontend/dist
var assets embed.FS

func init() {
	// Shared FIPS 140-3 compliance check (common to GUI and CLI binaries)
	intfips.Init("wails")

	// v4.5.1: Warn if NTLM proxy is configured in FIPS mode
	// NTLM uses non-FIPS algorithms (MD4/MD5) which may violate compliance for FRM platforms
	if intfips.Enabled {
		cfg, err := config.LoadConfigCSV(config.GetDefaultConfigPath())
		if err == nil && cfg != nil {
			if warning := cfg.ValidateNTLMForFIPS(); warning != "" {
				log.Printf("[WARN] NTLM proxy mode uses non-FIPS algorithms (MD4/MD5)")
				log.Printf("[WARN] For strict FIPS compliance, use 'basic' proxy mode over TLS")
			}
		}
	}
}

func main() {
	// Propagate version from the single source of truth (internal/version)
	// to CLI package for backwards compatibility
	cli.Version = version.Version
	cli.BuildTime = version.BuildTime

	// Check for diagnostic modes before GUI/CLI
	if slices.Contains(os.Args, "--mesa-doctor") {
		fmt.Printf("Rescale Interlink %s\n\n", version.Version)
		mesa.Doctor()
		return
	}

	// Enable timing output (works with both GUI and CLI)
	if slices.Contains(os.Args, "--timing") {
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
	// v4.6.8: Suppress GTK ibus input method warnings on Linux.
	// Wails uses its own webview input handling; ibus is unnecessary.
	if runtime.GOOS == "linux" && os.Getenv("GTK_IM_MODULE") == "" {
		os.Setenv("GTK_IM_MODULE", "none")
	}
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
	if slices.Contains(os.Args, "--cli") {
		return true
	}
	if slices.Contains(os.Args, "--gui") {
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
