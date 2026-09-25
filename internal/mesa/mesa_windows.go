//go:build windows

package mesa

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// embeddedDLLs is defined in either:
// - embed_mesa_windows.go (when built with -tags mesa) - contains actual DLL data
// - embed_nomesa_windows.go (default) - empty map for smaller binary
//
// mesaEmbedded is also defined there, indicating which build variant this is.

// Windows API procedures for DLL loading
var (
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	setDllDirectoryW     = kernel32.NewProc("SetDllDirectoryW")
	getModuleHandleWProc = kernel32.NewProc("GetModuleHandleW")
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

// isDLLAlreadyLoaded checks if a DLL is already loaded in the process.
// Returns the handle if loaded, 0 if not loaded.
func isDLLAlreadyLoaded(dllName string) (windows.Handle, string) {
	namePtr, err := syscall.UTF16PtrFromString(dllName)
	if err != nil {
		return 0, ""
	}

	ret, _, _ := getModuleHandleWProc.Call(uintptr(unsafe.Pointer(namePtr)))
	if ret == 0 {
		return 0, ""
	}

	handle := windows.Handle(ret)

	// Get the full path of the loaded DLL
	var path [260]uint16
	n, _ := windows.GetModuleFileName(handle, &path[0], 260)
	pathStr := syscall.UTF16ToString(path[:n])

	return handle, pathStr
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

// EnsureSoftwareRendering sets up Mesa software rendering for Windows.
//
// Deployment model: "app-local" — Mesa DLLs are bundled alongside the EXE.
// Windows loads DLLs from the EXE directory before System32 (when the DLL is
// not in KnownDLLs), so the bundled DLLs are picked up automatically.
//
// This function:
// 1. Sets GALLIUM_DRIVER=llvmpipe and LIBGL_ALWAYS_SOFTWARE=1
// 2. Checks which opengl32.dll was loaded and verifies it's Mesa
// 3. Extracts DLLs to LOCALAPPDATA (for --mesa-doctor diagnostics)
//
// The DLL loading happens at process start (CGO/GLFW init), before any Go code
// runs. By the time this function executes, we can only verify what happened.
//
// Returns nil on success. On error, returns actionable error message.
//
// Build variants:
// - With "-tags mesa": Embedded DLLs for app-local deployment
// - Without "-tags mesa": No embedded DLLs, requires hardware GPU (smaller binary)
func EnsureSoftwareRendering() error {
	// Allow opt-out for users with working GPU
	if os.Getenv("RESCALE_HARDWARE_RENDER") == "1" {
		fmt.Println("[Mesa] Hardware rendering requested (RESCALE_HARDWARE_RENDER=1)")
		return nil
	}

	// Check if this is a no-Mesa build
	if !mesaEmbedded {
		fmt.Println("[Mesa] This build does not include Mesa software rendering")
		fmt.Println("[Mesa] Hardware GPU/OpenGL required. Use the '-mesa' build variant if software rendering is needed.")
		return nil
	}

	fmt.Println("[Mesa] Setting up software rendering...")

	// Tell Mesa to use software rendering (llvmpipe)
	// These env vars must be set for Mesa to use software renderer
	os.Setenv("GALLIUM_DRIVER", "llvmpipe")
	os.Setenv("LIBGL_ALWAYS_SOFTWARE", "1")

	// Check which opengl32.dll was loaded
	// By now, CGO/GLFW initialization has already loaded it
	if err := verifyMesaDLLLoaded(); err != nil {
		return err
	}

	// Extract DLLs to LOCALAPPDATA for --mesa-doctor diagnostics
	// This is a secondary/fallback location - app-local (EXE directory) is preferred
	targetDir := MesaDir()
	if targetDir != "" {
		if err := os.MkdirAll(targetDir, 0755); err == nil {
			// Best effort extraction - don't fail if it doesn't work
			_ = extractDLLsIfNeeded(targetDir)
		}
		fmt.Printf("[Mesa] Using local directory: %s\n", targetDir)
	}

	fmt.Println("[Mesa] Software rendering ready (set RESCALE_HARDWARE_RENDER=1 for GPU)")
	return nil
}

// verifyMesaDLLLoaded checks if opengl32.dll was loaded from the correct location.
// Returns nil if Mesa is loaded, error with actionable message if System32's version was loaded.
func verifyMesaDLLLoaded() error {
	handle, loadedPath := isDLLAlreadyLoaded("opengl32.dll")
	if handle == 0 {
		// Not loaded yet - shouldn't happen in normal flow, but not an error
		return nil
	}

	lowerPath := strings.ToLower(loadedPath)

	// Check if loaded from System32 - this is the failure case
	if strings.Contains(lowerPath, "system32") || strings.Contains(lowerPath, "syswow64") {
		exeDir, _ := getExeDir()
		return fmt.Errorf(`Mesa software rendering is not available.

System32's opengl32.dll was loaded: %s

This happened because Mesa DLLs were not found in the EXE directory at process start.
Windows checked the EXE directory first, found no opengl32.dll, and fell back to System32.

To fix this, ensure these files are in the same directory as rescale-int.exe:
  - opengl32.dll
  - libgallium_wgl.dll
  - libglapi.dll

Your EXE directory: %s

If you're using the pre-built release, download the '-mesa.zip' package which includes these DLLs.
Run 'rescale-int --mesa-doctor' for detailed diagnostics.`, loadedPath, exeDir)
	}

	// Loaded from somewhere else (EXE directory, LOCALAPPDATA, etc.) - this is success
	fmt.Printf("[Mesa] opengl32.dll already loaded from: %s\n", loadedPath)

	// Also verify the supporting DLLs are loaded
	for _, dll := range []string{"libgallium_wgl.dll", "libglapi.dll"} {
		if h, path := isDLLAlreadyLoaded(dll); h != 0 {
			fmt.Printf("[Mesa] %s already loaded from: %s (continuing)\n", dll, path)
		}
	}

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

// GetExeDir returns the directory containing the running executable.
// Exported for use by mesainit.
func GetExeDir() (string, error) {
	return getExeDir()
}

// DLLsExistInDir checks if all required Mesa DLLs exist in the given directory.
// Exported for use by mesainit.
func DLLsExistInDir(dir string) bool {
	return dllsExistIn(dir)
}

// ExtractDLLsToDir extracts embedded Mesa DLLs to the specified directory.
// Only extracts DLLs that are missing or have wrong size.
// Exported for use by mesainit.
func ExtractDLLsToDir(dir string) error {
	if !mesaEmbedded {
		return fmt.Errorf("this build does not include Mesa DLLs")
	}
	return extractDLLsIfNeeded(dir)
}

// HasEmbeddedDLLs returns true if this build includes embedded Mesa DLLs.
// Exported for use by mesainit.
func HasEmbeddedDLLs() bool {
	return mesaEmbedded
}
