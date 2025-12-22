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

// softwareRenderingEnabled tracks if we successfully set up Mesa
var softwareRenderingEnabled bool

// Windows API procedures for DLL loading
var (
	kernel32              = syscall.NewLazyDLL("kernel32.dll")
	setDllDirectoryW      = kernel32.NewProc("SetDllDirectoryW")
	addDllDirectoryProc   = kernel32.NewProc("AddDllDirectory")
	setDefaultDllDirsProc = kernel32.NewProc("SetDefaultDllDirectories")
	getModuleHandleWProc  = kernel32.NewProc("GetModuleHandleW")
)

// Constants for SetDefaultDllDirectories
const (
	LOAD_LIBRARY_SEARCH_USER_DIRS   = 0x00000400
	LOAD_LIBRARY_SEARCH_SYSTEM32    = 0x00000800
	LOAD_LIBRARY_SEARCH_DEFAULT_DIRS = 0x00001000
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

// addDllDirectory uses the modern AddDllDirectory API (Windows 8+).
// This is more secure than SetDllDirectory because it adds to the search
// path instead of replacing it, and it works with LOAD_LIBRARY_SEARCH_USER_DIRS.
func addDllDirectory(dir string) (uintptr, error) {
	dirPtr, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return 0, fmt.Errorf("invalid directory path: %w", err)
	}

	// AddDllDirectory returns a "DLL directory cookie" on success, or NULL on failure
	cookie, _, err := addDllDirectoryProc.Call(uintptr(unsafe.Pointer(dirPtr)))
	if cookie == 0 {
		return 0, fmt.Errorf("AddDllDirectory failed: %w", err)
	}
	return cookie, nil
}

// setDefaultDllDirectories restricts the DLL search to specific paths.
// We use this to ensure Windows checks USER_DIRS (our Mesa directory) and SYSTEM32.
func setDefaultDllDirectories(flags uintptr) error {
	ret, _, err := setDefaultDllDirsProc.Call(flags)
	if ret == 0 {
		return fmt.Errorf("SetDefaultDllDirectories failed: %w", err)
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

// configureDllSearchPath sets up the DLL search path using modern APIs if available,
// falling back to SetDllDirectory on older Windows versions.
func configureDllSearchPath(dir string) error {
	// Try the modern approach first: SetDefaultDllDirectories + AddDllDirectory
	// This gives us more control over the search order
	err := setDefaultDllDirectories(LOAD_LIBRARY_SEARCH_USER_DIRS | LOAD_LIBRARY_SEARCH_SYSTEM32)
	if err != nil {
		// Fall back to SetDllDirectory on older Windows
		fmt.Printf("[Mesa] SetDefaultDllDirectories unavailable, using SetDllDirectory\n")
		return setDllDirectory(dir)
	}

	// Add our Mesa directory to the user DLL search path
	cookie, err := addDllDirectory(dir)
	if err != nil {
		// Fall back to SetDllDirectory
		fmt.Printf("[Mesa] AddDllDirectory failed, using SetDllDirectory: %v\n", err)
		// Reset to default search order
		setDefaultDllDirectories(LOAD_LIBRARY_SEARCH_DEFAULT_DIRS)
		return setDllDirectory(dir)
	}

	fmt.Printf("[Mesa] Configured DLL search path (cookie: 0x%x)\n", cookie)

	// Also set the legacy directory for maximum compatibility
	_ = setDllDirectory(dir)

	return nil
}

// preloadMesaDLLs loads Mesa DLLs in dependency order for clear diagnostics.
//
// Load order (dependencies must be loaded first):
//   1. libglapi.dll    - GL API dispatch (no Mesa dependencies)
//   2. libgallium_wgl.dll - Gallium WGL driver (depends on libglapi)
//   3. opengl32.dll    - Mesa frontend (depends on libgallium_wgl)
//
// Windows "Known DLLs" (like opengl32.dll) are normally loaded from System32
// regardless of SetDllDirectory or app directory placement. By pre-loading
// with a full path, we get our DLLs into the "loaded-module list" which is
// checked BEFORE Known DLLs.
//
// CRITICAL: We use LoadLibraryEx with LOAD_WITH_ALTERED_SEARCH_PATH.
// This flag tells Windows to search the DLL's directory (not the EXE's
// directory) when resolving the DLL's dependencies.
//
// See: https://learn.microsoft.com/en-us/windows/win32/dlls/dynamic-link-library-search-order
func preloadMesaDLLs(dir string) error {
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

	// CRITICAL: Check if opengl32.dll is ALREADY loaded from System32
	// If it is, our preloading CANNOT override it - the system version wins.
	if handle, path := isDLLAlreadyLoaded("opengl32.dll"); handle != 0 {
		lowerPath := strings.ToLower(path)
		if strings.Contains(lowerPath, "system32") || strings.Contains(lowerPath, "syswow64") {
			fmt.Printf("[Mesa] CRITICAL: opengl32.dll is ALREADY loaded from System32!\n")
			fmt.Printf("[Mesa]   Path: %s\n", path)
			fmt.Printf("[Mesa]   This happened before Mesa preloading could run.\n")
			fmt.Printf("[Mesa]   Possible causes:\n")
			fmt.Printf("[Mesa]     1. Static PE import (opengl32.dll in EXE import table)\n")
			fmt.Printf("[Mesa]     2. A package init() loaded it before mesainit\n")
			fmt.Printf("[Mesa]     3. CGO initialization triggered DLL load\n")
			fmt.Printf("[Mesa]   Mesa software rendering CANNOT work in this state.\n")
			return fmt.Errorf("opengl32.dll already loaded from System32 - Mesa cannot override")
		}
		// Already loaded but NOT from System32 - might be Mesa already
		fmt.Printf("[Mesa] opengl32.dll already loaded from: %s\n", path)
		if strings.Contains(lowerPath, "rescale") || strings.Contains(lowerPath, "mesa") || strings.Contains(lowerPath, "appdata") {
			fmt.Printf("[Mesa] This appears to be Mesa's version - continuing.\n")
		}
	}

	// Configure DLL search path using modern APIs when available
	fmt.Printf("[Mesa] Configuring DLL search path: %s\n", dir)
	if err := configureDllSearchPath(dir); err != nil {
		fmt.Printf("[Mesa] Warning: configureDllSearchPath failed: %v\n", err)
		// Continue anyway - LoadLibraryEx with full path should still work
	}

	// Load DLLs in dependency order - if any fails, we know exactly which one
	// Note: libglapi.dll was removed in Mesa 25.0.0, but we're using 24.2.x
	dllOrder := []string{"libglapi.dll", "libgallium_wgl.dll", "opengl32.dll"}

	for _, dll := range dllOrder {
		dllPath := filepath.Join(dir, dll)

		// Check if this specific DLL is already loaded
		if handle, existingPath := isDLLAlreadyLoaded(dll); handle != 0 {
			lowerPath := strings.ToLower(existingPath)
			if strings.Contains(lowerPath, "system32") || strings.Contains(lowerPath, "syswow64") {
				fmt.Printf("[Mesa] CRITICAL: %s already loaded from System32: %s\n", dll, existingPath)
				return fmt.Errorf("%s already loaded from System32 - cannot override", dll)
			}
			fmt.Printf("[Mesa] %s already loaded from: %s (continuing)\n", dll, existingPath)
			continue // Already loaded, skip loading again
		}

		// Verify the file exists
		if _, err := os.Stat(dllPath); os.IsNotExist(err) {
			return fmt.Errorf("%s not found at %s", dll, dllPath)
		}

		fmt.Printf("[Mesa] Pre-loading %s...\n", dllPath)

		// LOAD_WITH_ALTERED_SEARCH_PATH = 0x00000008
		// When this flag is set AND lpFileName contains a path, the loader uses
		// the directory of lpFileName as the search path for dependencies.
		handle, err := windows.LoadLibraryEx(dllPath, 0, windows.LOAD_WITH_ALTERED_SEARCH_PATH)
		if err != nil {
			fmt.Printf("[Mesa] LoadLibraryEx failed for %s: %v\n", dll, err)
			if errno, ok := err.(syscall.Errno); ok {
				fmt.Printf("[Mesa] Windows error code: %d (0x%x)\n", uint32(errno), uint32(errno))
				// Common error codes:
				// 126 (0x7E) = ERROR_MOD_NOT_FOUND - A dependent DLL wasn't found
				// 193 (0xC1) = ERROR_BAD_EXE_FORMAT - Wrong architecture (32/64 bit mismatch)
				// 127 (0x7F) = ERROR_PROC_NOT_FOUND - A required function wasn't found
				switch uint32(errno) {
				case 126:
					fmt.Printf("[Mesa] ERROR_MOD_NOT_FOUND: A dependent DLL is missing.\n")
				case 193:
					fmt.Printf("[Mesa] ERROR_BAD_EXE_FORMAT: Architecture mismatch (32-bit vs 64-bit).\n")
				case 127:
					fmt.Printf("[Mesa] ERROR_PROC_NOT_FOUND: A required function is missing from a DLL.\n")
				}
			}
			return fmt.Errorf("failed to load %s: %w", dll, err)
		}

		// Keep the handle - don't call FreeLibrary
		// The DLL must stay loaded for GLFW to find it in loaded-module list
		fmt.Printf("[Mesa] Loaded %s successfully (handle: 0x%x)\n", dll, handle)
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
// As of v3.4.13, the recommended deployment is "app-local": bundle Mesa DLLs
// alongside the EXE. Windows loads DLLs from the EXE directory before System32
// (when the DLL is not in KnownDLLs), so this works automatically.
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

	softwareRenderingEnabled = true
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

// IsSoftwareRenderingEnabled returns true if Mesa software rendering is active
func IsSoftwareRenderingEnabled() bool {
	return softwareRenderingEnabled
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
