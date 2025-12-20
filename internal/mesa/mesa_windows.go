//go:build windows

package mesa

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// embeddedDLLs is defined in either:
// - embed_mesa_windows.go (when built with -tags mesa) - contains actual DLL data
// - embed_nomesa_windows.go (default) - empty map for smaller binary
//
// mesaEmbedded is also defined there, indicating which build variant this is.

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

// preloadMesaDLL explicitly loads Mesa's opengl32.dll with full path.
// This registers it in Windows' "loaded-module list" which is checked
// BEFORE the "Known DLLs" list, allowing us to override the system DLL.
//
// Windows "Known DLLs" (like opengl32.dll) are normally loaded from System32
// regardless of SetDllDirectory or app directory placement. By pre-loading
// with a full path, we get our DLL into the loaded-module list first.
//
// CRITICAL: We use LoadLibraryEx with LOAD_WITH_ALTERED_SEARCH_PATH.
// This flag tells Windows to search the DLL's directory (not the EXE's
// directory) when resolving the DLL's dependencies. Without this flag,
// Windows won't find libgallium_wgl.dll even if it's in the same folder
// as opengl32.dll.
//
// See: https://learn.microsoft.com/en-us/windows/win32/dlls/dynamic-link-library-search-order
func preloadMesaDLL(dir string) error {
	openglPath := filepath.Join(dir, "opengl32.dll")

	// Log directory contents for diagnostics
	fmt.Printf("[Mesa] Directory contents of %s:\n", dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Printf("[Mesa]   Error reading directory: %v\n", err)
	} else {
		for _, entry := range entries {
			info, _ := entry.Info()
			if info != nil {
				fmt.Printf("[Mesa]   %s (%d bytes)\n", entry.Name(), info.Size())
			} else {
				fmt.Printf("[Mesa]   %s\n", entry.Name())
			}
		}
	}

	// Verify the file exists
	if _, err := os.Stat(openglPath); os.IsNotExist(err) {
		return fmt.Errorf("opengl32.dll not found at %s", openglPath)
	}

	// Also set DLL directory as belt-and-suspenders
	// (LoadLibraryEx with LOAD_WITH_ALTERED_SEARCH_PATH is the primary fix)
	fmt.Printf("[Mesa] Setting DLL search directory: %s\n", dir)
	if err := setDllDirectory(dir); err != nil {
		fmt.Printf("[Mesa] Warning: SetDllDirectory failed: %v\n", err)
		// Continue anyway - LoadLibraryEx should handle it
	}

	// Load with full absolute path using LOAD_WITH_ALTERED_SEARCH_PATH
	// This flag is CRITICAL: it makes Windows search the DLL's directory
	// (not the application's directory) for the DLL's dependencies.
	// Without this, libgallium_wgl.dll won't be found even if it's
	// in the same folder as opengl32.dll.
	fmt.Printf("[Mesa] Pre-loading %s with LOAD_WITH_ALTERED_SEARCH_PATH...\n", openglPath)

	// LOAD_WITH_ALTERED_SEARCH_PATH = 0x00000008
	// When this flag is set AND lpFileName contains a path, the loader uses
	// the directory of lpFileName as the search path for dependencies.
	// Note: windows.LoadLibraryEx takes a string and handles UTF16 conversion internally
	handle, err := windows.LoadLibraryEx(openglPath, 0, windows.LOAD_WITH_ALTERED_SEARCH_PATH)
	if err != nil {
		// Extract Windows error code for better diagnostics
		fmt.Printf("[Mesa] LoadLibraryEx failed: %v\n", err)
		if errno, ok := err.(syscall.Errno); ok {
			fmt.Printf("[Mesa] Windows error code: %d (0x%x)\n", uint32(errno), uint32(errno))
			// Common error codes:
			// 126 (0x7E) = ERROR_MOD_NOT_FOUND - A dependent DLL wasn't found
			// 193 (0xC1) = ERROR_BAD_EXE_FORMAT - Wrong architecture (32/64 bit mismatch)
			// 127 (0x7F) = ERROR_PROC_NOT_FOUND - A required function wasn't found
			switch uint32(errno) {
			case 126:
				fmt.Printf("[Mesa] ERROR_MOD_NOT_FOUND: A dependent DLL is missing.\n")
				fmt.Printf("[Mesa] Tip: Use Dependencies.exe or dumpbin /dependents to find which DLL is missing.\n")
			case 193:
				fmt.Printf("[Mesa] ERROR_BAD_EXE_FORMAT: Architecture mismatch (32-bit vs 64-bit).\n")
			case 127:
				fmt.Printf("[Mesa] ERROR_PROC_NOT_FOUND: A required function is missing from a DLL.\n")
			}
		}
		return fmt.Errorf("failed to load Mesa opengl32.dll: %w", err)
	}

	// Keep the handle - don't call FreeLibrary
	// The DLL must stay loaded for GLFW to find it in loaded-module list
	fmt.Printf("[Mesa] Pre-loaded opengl32.dll successfully (handle: 0x%x)\n", handle)

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
// 1. Extracts Mesa DLLs to LOCALAPPDATA (always local C:\ drive)
// 2. Sets DLL search directory so Windows can find libgallium_wgl.dll
// 3. Pre-loads opengl32.dll with full path to bypass Windows "Known DLLs" mechanism
// 4. Sets GALLIUM_DRIVER=llvmpipe to use software rendering
//
// IMPORTANT: Must be called BEFORE any Fyne/OpenGL initialization.
//
// Why LOCALAPPDATA? Network drives (like Z:\ on Rescale) have DLL loading
// restrictions that prevent Mesa from working. LOCALAPPDATA is always on
// the local C:\ drive, avoiding these issues.
//
// The pre-load trick is critical: opengl32.dll is a Windows "Known DLL" which
// normally loads from System32 regardless of SetDllDirectory or app directory.
// By explicitly loading our Mesa DLL first, it gets registered in the
// "loaded-module list" which is checked BEFORE Known DLLs.
//
// Returns nil on success. On error, returns the error but the caller
// may choose to continue (the app might still work with a real GPU).
//
// Build variants:
// - With "-tags mesa": Embedded DLLs, automatic software rendering
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

	// ALWAYS use LOCALAPPDATA - guaranteed to be on local C:\ drive
	// Network drives (like Z:\ on Rescale) have DLL loading restrictions
	// that cause "module not found" errors even when DLLs are present
	targetDir := MesaDir()
	if targetDir == "" {
		return fmt.Errorf("could not determine Mesa directory (LOCALAPPDATA not set?)")
	}
	fmt.Printf("[Mesa] Using local directory: %s\n", targetDir)

	// Create directory if needed
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", targetDir, err)
	}

	// Extract DLLs
	if err := extractDLLsIfNeeded(targetDir); err != nil {
		return err
	}

	if !dllsExistIn(targetDir) {
		return fmt.Errorf("DLLs were not extracted to %s", targetDir)
	}

	// Pre-load Mesa's opengl32.dll with full path
	// This sets DLL directory (for libgallium_wgl.dll dependency) and loads the DLL
	// to bypass Windows "Known DLLs" mechanism
	if err := preloadMesaDLL(targetDir); err != nil {
		return err
	}

	// Tell Mesa to use software rendering (llvmpipe)
	os.Setenv("GALLIUM_DRIVER", "llvmpipe")
	softwareRenderingEnabled = true

	fmt.Println("[Mesa] Software rendering ready (set RESCALE_HARDWARE_RENDER=1 for GPU)")
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
