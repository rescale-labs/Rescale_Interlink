// Package mesa provides Mesa3D software rendering support for Windows.
//
// On Windows systems without GPU/OpenGL hardware support, Mesa's software
// renderer (llvmpipe) provides OpenGL compatibility. This package embeds
// the necessary Mesa DLLs and extracts them at runtime.
//
// DLL Extraction Strategy:
//   1. PREFERRED: Extract to same directory as executable (Windows finds these first)
//   2. FALLBACK: Extract to %LOCALAPPDATA%\rescale-int\mesa and use SetDllDirectory
//
// Usage:
//
//	// Call before any Fyne/OpenGL initialization
//	if err := mesa.EnsureSoftwareRendering(); err != nil {
//	    log.Printf("Mesa setup warning: %v", err)
//	}
//
// On non-Windows platforms, this package is a no-op.
package mesa

import (
	"os"
	"path/filepath"
)

// MesaDir returns the fallback directory for Mesa DLLs.
// This is used when we can't write to the executable's directory.
// On Windows: %LOCALAPPDATA%\rescale-int\mesa
// Falls back to ~/.rescale-int/mesa if LOCALAPPDATA not set.
func MesaDir() string {
	// Prefer LOCALAPPDATA (standard Windows app data location)
	if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
		return filepath.Join(dir, "rescale-int", "mesa")
	}
	// Fallback to home directory
	if dir := os.Getenv("USERPROFILE"); dir != "" {
		return filepath.Join(dir, ".rescale-int", "mesa")
	}
	if dir := os.Getenv("HOME"); dir != "" {
		return filepath.Join(dir, ".rescale-int", "mesa")
	}
	// Last resort: temp directory (will re-extract each boot)
	return filepath.Join(os.TempDir(), "rescale-int-mesa")
}
