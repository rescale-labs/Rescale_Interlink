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

// softwareRenderingEnabled tracks if we successfully set up Mesa
var softwareRenderingEnabled bool

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

// getExeDir returns the directory containing the running executable
func getExeDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	// Resolve symlinks to get the real path
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

// canWriteToDir tests if we can write files to a directory
func canWriteToDir(dir string) bool {
	testFile := filepath.Join(dir, ".mesa-write-test")
	f, err := os.Create(testFile)
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(testFile)
	return true
}

// EnsureSoftwareRendering sets up Mesa software rendering for Windows.
//
// This function:
// 1. Tries to extract Mesa DLLs next to the executable (PREFERRED - most reliable)
// 2. Falls back to %LOCALAPPDATA%\rescale-int\mesa with SetDllDirectory
// 3. Sets GALLIUM_DRIVER=llvmpipe to use software rendering
//
// IMPORTANT: Must be called BEFORE any Fyne/OpenGL initialization.
//
// Returns nil on success. On error, returns the error but the caller
// may choose to continue (the app might still work with a real GPU).
func EnsureSoftwareRendering() error {
	// Allow opt-out for users with working GPU
	if os.Getenv("RESCALE_HARDWARE_RENDER") == "1" {
		fmt.Println("[Mesa] Hardware rendering requested (RESCALE_HARDWARE_RENDER=1)")
		return nil
	}

	fmt.Println("[Mesa] Setting up software rendering...")

	// Strategy 1: Try to place DLLs next to the executable
	// This is the most reliable method - Windows always checks the app directory first
	exeDir, err := getExeDir()
	if err == nil && canWriteToDir(exeDir) {
		fmt.Printf("[Mesa] Strategy 1: Extracting DLLs to exe directory: %s\n", exeDir)
		if err := extractDLLsIfNeeded(exeDir); err != nil {
			fmt.Printf("[Mesa] Warning: Failed to extract to exe dir: %v\n", err)
		} else {
			// Check if DLLs exist there now
			if dllsExistIn(exeDir) {
				fmt.Println("[Mesa] DLLs in exe directory - this is the most reliable setup")
				os.Setenv("GALLIUM_DRIVER", "llvmpipe")
				softwareRenderingEnabled = true
				fmt.Println("[Mesa] Using Mesa software rendering (set RESCALE_HARDWARE_RENDER=1 for GPU)")
				return nil
			}
		}
	} else {
		if err != nil {
			fmt.Printf("[Mesa] Cannot determine exe directory: %v\n", err)
		} else {
			fmt.Printf("[Mesa] Cannot write to exe directory: %s\n", exeDir)
		}
	}

	// Strategy 2: Extract to LOCALAPPDATA and use SetDllDirectory
	mesaDir := MesaDir()
	if mesaDir == "" {
		return fmt.Errorf("could not determine Mesa directory")
	}

	fmt.Printf("[Mesa] Strategy 2: Using LOCALAPPDATA: %s\n", mesaDir)

	// Create directory if needed
	if err := os.MkdirAll(mesaDir, 0755); err != nil {
		return fmt.Errorf("failed to create Mesa directory %s: %w", mesaDir, err)
	}

	// Extract DLLs if needed
	if err := extractDLLsIfNeeded(mesaDir); err != nil {
		return err
	}

	// Verify DLLs exist
	if !dllsExistIn(mesaDir) {
		return fmt.Errorf("DLLs were not extracted to %s", mesaDir)
	}

	// Add Mesa directory to DLL search path
	// This must happen BEFORE Fyne/GLFW loads opengl32.dll
	fmt.Printf("[Mesa] Calling SetDllDirectory(%s)\n", mesaDir)
	if err := setDllDirectory(mesaDir); err != nil {
		return fmt.Errorf("failed to set DLL directory: %w", err)
	}

	// Tell Mesa to use software rendering (llvmpipe)
	os.Setenv("GALLIUM_DRIVER", "llvmpipe")
	softwareRenderingEnabled = true

	fmt.Println("[Mesa] Using Mesa software rendering (set RESCALE_HARDWARE_RENDER=1 for GPU)")
	return nil
}

// extractDLLsIfNeeded extracts DLLs to dir if they're missing or outdated
func extractDLLsIfNeeded(dir string) error {
	for name, data := range embeddedDLLs {
		targetPath := filepath.Join(dir, name)

		// Check if DLL already exists with correct size
		if info, err := os.Stat(targetPath); err == nil {
			if info.Size() == int64(len(data)) {
				fmt.Printf("[Mesa] %s already exists with correct size\n", name)
				continue
			}
			fmt.Printf("[Mesa] %s exists but size mismatch (have %d, want %d) - updating\n",
				name, info.Size(), len(data))
		}

		fmt.Printf("[Mesa] Extracting %s (%d bytes)...\n", name, len(data))
		if err := extractDLL(dir, name, data); err != nil {
			return err
		}
		fmt.Printf("[Mesa] Extracted %s successfully\n", name)
	}
	return nil
}

// dllsExistIn checks if all required DLLs exist in the directory
func dllsExistIn(dir string) bool {
	for name := range embeddedDLLs {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return false
		}
	}
	return true
}

// IsSoftwareRenderingEnabled returns true if Mesa software rendering is active
func IsSoftwareRenderingEnabled() bool {
	return softwareRenderingEnabled
}
