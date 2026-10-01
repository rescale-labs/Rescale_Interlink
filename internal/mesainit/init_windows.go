//go:build windows

// Package mesainit provides early Mesa initialization for Windows.
//
// CRITICAL: This package must be imported BEFORE any Fyne or OpenGL packages
// in main.go. Go runs init() functions in import order, so by importing
// mesainit first, we can set up Mesa before Fyne's init() runs.
//
// This package implements a "self-extract and re-exec" boot sequence:
//  1. Check if Mesa DLLs exist alongside the EXE
//  2. If missing, extract from embedded resources
//  3. Re-exec the process so the new DLLs are loaded on startup
//
// This allows the EXE to be distributed standalone - it will automatically
// extract the required DLLs on first run.
package mesainit

import (
	"fmt"
	"os"
	"os/exec"
	"slices"

	"github.com/rescale/rescale-int/internal/mesa"
)

// Environment variable to prevent infinite re-exec loops
const reexecEnvVar = "_RESCALE_MESA_REEXEC_DONE"

func init() {
	// The CLI needs no Mesa; the GUI and the Mesa doctor do.
	if !GUIMode() && !slices.Contains(os.Args, "--mesa-doctor") {
		return
	}

	// Allow opting out of software rendering
	if os.Getenv("RESCALE_HARDWARE_RENDER") == "1" {
		return
	}

	// Check if this build has embedded Mesa DLLs
	if !mesa.HasEmbeddedDLLs() {
		return
	}

	// Self-extract and re-exec if needed
	// This happens before EnsureSoftwareRendering() because by the time
	// that function runs, CGO has already loaded opengl32.dll.
	if os.Getenv(reexecEnvVar) != "1" {
		// Exits this process when it re-execs; otherwise carry on below.
		tryExtractAndReexec()
	}

	// Run early diagnostics if debug mode is enabled
	if os.Getenv("RESCALE_MESA_DEBUG") != "" {
		mesa.EarlyDiagnostics()
	}

	// Set up Mesa environment and verify correct DLL was loaded
	_ = mesa.EnsureSoftwareRendering()
}

// tryExtractAndReexec checks if Mesa DLLs are missing from the EXE directory,
// extracts them if possible, and re-execs the process.
//
// Returns true if re-exec was attempted (even if it failed).
// Returns false if no extraction/re-exec was needed.
func tryExtractAndReexec() bool {
	exeDir, err := mesa.GetExeDir()
	if err != nil {
		debugLog("Failed to get EXE directory: %v", err)
		return false
	}

	// Check if DLLs already exist
	if mesa.DLLsExistInDir(exeDir) {
		debugLog("Mesa DLLs already exist in EXE directory")
		return false
	}

	debugLog("Mesa DLLs not found in EXE directory, attempting extraction...")

	// Try to extract DLLs to EXE directory
	if err := mesa.ExtractDLLsToDir(exeDir); err != nil {
		// Extraction failed - likely read-only directory (Program Files, etc.)
		debugLog("Failed to extract Mesa DLLs to %s: %v", exeDir, err)
		debugLog("This is expected if running from a read-only location.")
		debugLog("The '-mesa.zip' package includes pre-extracted DLLs.")
		return false
	}

	debugLog("Mesa DLLs extracted successfully, re-executing...")

	// Set marker to prevent infinite loops
	os.Setenv(reexecEnvVar, "1")

	// Re-exec ourselves
	// Using exec.Command instead of syscall.Exec for better cross-platform compatibility
	exePath, err := os.Executable()
	if err != nil {
		debugLog("Failed to get executable path: %v", err)
		return true // Extraction done, but re-exec failed
	}

	cmd := exec.Command(exePath, os.Args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	if err := cmd.Run(); err != nil {
		debugLog("Re-exec failed: %v", err)
		// Don't return error - the new process may have already run and exited
	}

	// Exit this process - the re-exec'd process is handling things now.
	// If the process never started, ProcessState is nil; exit non-zero rather than panic.
	if cmd.ProcessState == nil {
		os.Exit(1)
	}
	os.Exit(cmd.ProcessState.ExitCode())
	return true // Never reached
}

// debugLog prints debug messages when RESCALE_MESA_DEBUG is set
func debugLog(format string, args ...interface{}) {
	if os.Getenv("RESCALE_MESA_DEBUG") != "" {
		fmt.Printf("[MESA-INIT] "+format+"\n", args...)
	}
}
