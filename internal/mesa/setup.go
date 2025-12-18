// Package mesa provides Mesa3D software rendering support for Windows.
//
// On Windows systems without GPU/OpenGL hardware support, Mesa's software
// renderer (llvmpipe) provides OpenGL compatibility. This package embeds
// the necessary Mesa DLLs and extracts them at runtime, then configures
// Windows to use them via SetDllDirectory.
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

// MesaDir returns the directory where Mesa DLLs are extracted.
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

// DLLInfo describes an embedded DLL file
type DLLInfo struct {
	Name string // Filename (e.g., "opengl32.dll")
	Size int64  // Expected size in bytes (for version checking)
}

// RequiredDLLs lists the Mesa DLLs needed for software rendering
var RequiredDLLs = []DLLInfo{
	{Name: "opengl32.dll", Size: 137216},        // Mesa OpenGL frontend
	{Name: "libgallium_wgl.dll", Size: 53364736}, // Gallium llvmpipe backend
}

// dllNeedsUpdate checks if a DLL needs to be extracted/updated.
// Returns true if the file doesn't exist or has wrong size.
func dllNeedsUpdate(dir string, info DLLInfo) bool {
	path := filepath.Join(dir, info.Name)
	stat, err := os.Stat(path)
	if os.IsNotExist(err) {
		return true
	}
	if err != nil {
		return true // Can't stat, assume needs update
	}
	// Check size matches expected (simple version check)
	return stat.Size() != info.Size
}

// AllDLLsCurrent checks if all required DLLs exist with correct sizes
func AllDLLsCurrent(dir string) bool {
	for _, info := range RequiredDLLs {
		if dllNeedsUpdate(dir, info) {
			return false
		}
	}
	return true
}
