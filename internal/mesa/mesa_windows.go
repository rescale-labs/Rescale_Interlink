//go:build windows

package mesa

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// embeddedDLLs is defined in either:
// - embed_mesa_windows.go (when built with -tags mesa) - contains actual DLL data
// - embed_nomesa_windows.go (default) - empty map for smaller binary
//
// mesaEmbedded is also defined there, indicating which build variant this is.

// softwareRenderingEnabled tracks if we successfully set up Mesa
var softwareRenderingEnabled bool

// Windows drive type constants
const (
	DRIVE_REMOTE = 4 // Network drive
)

// Windows API for SetDllDirectory and GetDriveType
var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	setDllDirectoryW = kernel32.NewProc("SetDllDirectoryW")
	getDriveTypeW    = kernel32.NewProc("GetDriveTypeW")
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

// isNetworkPath checks if a path is on a network/remote drive.
// This uses Windows GetDriveTypeW API to detect mapped network drives.
func isNetworkPath(path string) bool {
	// Check for UNC paths (\\server\share)
	if len(path) >= 2 && path[0] == '\\' && path[1] == '\\' {
		return true
	}

	// Check drive letter paths (e.g., Z:\)
	if len(path) >= 2 && path[1] == ':' {
		driveLetter := strings.ToUpper(string(path[0])) + ":\\"
		drivePtr, err := syscall.UTF16PtrFromString(driveLetter)
		if err != nil {
			return false
		}
		driveType, _, _ := getDriveTypeW.Call(uintptr(unsafe.Pointer(drivePtr)))
		return driveType == DRIVE_REMOTE
	}
	return false
}

// preloadMesaDLL explicitly loads Mesa's opengl32.dll with full path.
// This registers it in Windows' "loaded-module list" which is checked
// BEFORE the "Known DLLs" list, allowing us to override the system DLL.
//
// Windows "Known DLLs" (like opengl32.dll) are normally loaded from System32
// regardless of SetDllDirectory or app directory placement. By pre-loading
// with a full path, we get our DLL into the loaded-module list first.
func preloadMesaDLL(dir string) error {
	openglPath := filepath.Join(dir, "opengl32.dll")

	// Verify the file exists
	if _, err := os.Stat(openglPath); os.IsNotExist(err) {
		return fmt.Errorf("opengl32.dll not found at %s", openglPath)
	}

	// Load with full absolute path - this bypasses Known DLLs
	fmt.Printf("[Mesa] Pre-loading %s...\n", openglPath)
	handle, err := syscall.LoadLibrary(openglPath)
	if err != nil {
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
// 1. Detects if running from a network drive (which has DLL loading restrictions)
// 2. Extracts Mesa DLLs to a LOCAL directory (exe dir if local, else LOCALAPPDATA)
// 3. Pre-loads opengl32.dll with full path to bypass Windows "Known DLLs" mechanism
// 4. Sets GALLIUM_DRIVER=llvmpipe to use software rendering
//
// IMPORTANT: Must be called BEFORE any Fyne/OpenGL initialization.
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

	// Determine target directory for DLLs
	// CRITICAL: Must be a LOCAL path - network drives have DLL loading restrictions
	var targetDir string
	exeDir, exeErr := getExeDir()

	// Strategy selection: prefer exe directory ONLY if local and writable
	// Network drives (like Z:\ on Rescale) have DLL loading restrictions
	if exeErr == nil && canWriteToDir(exeDir) && !isNetworkPath(exeDir) {
		targetDir = exeDir
		fmt.Printf("[Mesa] Using exe directory (local): %s\n", targetDir)
	} else {
		// Use LOCALAPPDATA - guaranteed to be on local disk
		targetDir = MesaDir()
		if targetDir == "" {
			return fmt.Errorf("could not determine Mesa directory")
		}

		if exeErr != nil {
			fmt.Printf("[Mesa] Cannot determine exe directory, using LOCALAPPDATA\n")
		} else if isNetworkPath(exeDir) {
			fmt.Printf("[Mesa] Exe directory is on network drive (%s), using LOCALAPPDATA\n", exeDir)
		} else {
			fmt.Printf("[Mesa] Exe directory not writable, using LOCALAPPDATA\n")
		}
		fmt.Printf("[Mesa] Using local directory: %s\n", targetDir)
	}

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

	// KEY FIX: Pre-load Mesa's opengl32.dll with full path
	// This bypasses Windows "Known DLLs" mechanism by registering in loaded-module list
	// The loaded-module list is checked BEFORE Known DLLs in Windows DLL search order
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
