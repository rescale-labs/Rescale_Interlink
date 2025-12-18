//go:build windows

package mesa

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// Embedded Mesa DLLs - these are included in the Windows binary
//
//go:embed dlls/opengl32.dll
var opengl32DLL []byte

//go:embed dlls/libgallium_wgl.dll
var libgalliumDLL []byte

// embeddedDLLs maps filenames to their embedded content
var embeddedDLLs = map[string][]byte{
	"opengl32.dll":       opengl32DLL,
	"libgallium_wgl.dll": libgalliumDLL,
}

// Windows API for SetDllDirectory
var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	setDllDirectoryW = kernel32.NewProc("SetDllDirectoryW")
)

// setDllDirectory adds a directory to the DLL search path.
// This must be called BEFORE any DLLs are loaded (i.e., before Fyne init).
func setDllDirectory(dir string) error {
	// Convert to UTF-16 for Windows API
	dirPtr, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return fmt.Errorf("invalid directory path: %w", err)
	}

	ret, _, err := setDllDirectoryW.Call(uintptr(unsafe.Pointer(dirPtr)))
	if ret == 0 {
		return fmt.Errorf("SetDllDirectory failed: %w", err)
	}
	return nil
}

// extractDLL writes an embedded DLL to the target directory.
// Uses atomic write (temp file + rename) to prevent partial extraction.
func extractDLL(dir, name string, data []byte) error {
	targetPath := filepath.Join(dir, name)
	tempPath := targetPath + ".tmp"

	// Write to temp file first
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", name, err)
	}

	// Atomic rename
	if err := os.Rename(tempPath, targetPath); err != nil {
		os.Remove(tempPath) // Clean up temp file on failure
		return fmt.Errorf("failed to install %s: %w", name, err)
	}

	return nil
}

// EnsureSoftwareRendering sets up Mesa software rendering for Windows.
//
// This function:
// 1. Checks if Mesa DLLs are already extracted and current
// 2. Extracts embedded DLLs if needed (to %LOCALAPPDATA%\rescale-int\mesa)
// 3. Calls SetDllDirectory so Windows finds Mesa's opengl32.dll
// 4. Sets GALLIUM_DRIVER=llvmpipe to use software rendering
//
// IMPORTANT: Must be called BEFORE any Fyne/OpenGL initialization.
//
// Returns nil on success. On error, returns the error but the caller
// may choose to continue (the app might still work with a real GPU).
func EnsureSoftwareRendering() error {
	// Allow opt-out for users with working GPU
	if os.Getenv("RESCALE_HARDWARE_RENDER") == "1" {
		return nil
	}

	mesaDir := MesaDir()
	if mesaDir == "" {
		return fmt.Errorf("could not determine Mesa directory")
	}

	// Create directory if needed
	if err := os.MkdirAll(mesaDir, 0755); err != nil {
		return fmt.Errorf("failed to create Mesa directory %s: %w", mesaDir, err)
	}

	// Check if DLLs need extraction (skip if already current)
	if !AllDLLsCurrent(mesaDir) {
		// Extract each DLL
		for name, data := range embeddedDLLs {
			// Check if this specific DLL needs update
			for _, info := range RequiredDLLs {
				if info.Name == name && dllNeedsUpdate(mesaDir, info) {
					if err := extractDLL(mesaDir, name, data); err != nil {
						return err
					}
					break
				}
			}
		}
	}

	// Add Mesa directory to DLL search path
	// This must happen BEFORE Fyne/GLFW loads opengl32.dll
	if err := setDllDirectory(mesaDir); err != nil {
		return fmt.Errorf("failed to set DLL directory: %w", err)
	}

	// Tell Mesa to use software rendering (llvmpipe)
	os.Setenv("GALLIUM_DRIVER", "llvmpipe")

	return nil
}

// IsSoftwareRenderingEnabled returns true if Mesa software rendering is active
func IsSoftwareRenderingEnabled() bool {
	return os.Getenv("GALLIUM_DRIVER") == "llvmpipe"
}
