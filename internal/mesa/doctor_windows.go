//go:build windows

package mesa

import (
	"debug/pe"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// Doctor runs comprehensive Mesa diagnostics and prints results.
// This is designed to be run standalone (--mesa-doctor) to debug DLL loading issues.
func Doctor() {
	fmt.Println("=" + strings.Repeat("=", 69))
	fmt.Println("MESA SOFTWARE RENDERING DIAGNOSTICS")
	fmt.Println("=" + strings.Repeat("=", 69))
	fmt.Println()

	// 1. Build information
	printBuildInfo()

	// 2. Environment variables
	printEnvironment()

	// 3. Embedded DLLs
	printEmbeddedDLLs()

	// 4. Extracted DLLs on disk
	printExtractedDLLs()

	// 5. VC++ Runtime check
	printVCRuntimeStatus()

	// 6. DLL load test (in dependency order)
	printDLLLoadTest()

	fmt.Println()
	fmt.Println("=" + strings.Repeat("=", 69))
	fmt.Println("END OF DIAGNOSTICS")
	fmt.Println("=" + strings.Repeat("=", 69))
}

func printBuildInfo() {
	fmt.Println("[BUILD INFO]")
	fmt.Printf("  Mesa embedded: %v\n", mesaEmbedded)
	if mesaEmbedded {
		fmt.Println("  Build variant: -tags mesa (Mesa DLLs embedded)")
	} else {
		fmt.Println("  Build variant: no mesa tag (requires hardware GPU)")
	}
	fmt.Println()
}

func printEnvironment() {
	fmt.Println("[ENVIRONMENT VARIABLES]")
	vars := []string{
		"RESCALE_HARDWARE_RENDER",
		"GALLIUM_DRIVER",
		"LIBGL_ALWAYS_SOFTWARE",
		"LOCALAPPDATA",
	}
	for _, v := range vars {
		val := os.Getenv(v)
		if val == "" {
			fmt.Printf("  %s: (not set)\n", v)
		} else {
			fmt.Printf("  %s: %s\n", v, val)
		}
	}
	fmt.Println()
}

func printEmbeddedDLLs() {
	fmt.Println("[EMBEDDED DLLs]")
	if !mesaEmbedded {
		fmt.Println("  (none - this build does not embed Mesa DLLs)")
		fmt.Println()
		return
	}

	if len(embeddedDLLs) == 0 {
		fmt.Println("  (none - embeddedDLLs map is empty)")
		fmt.Println()
		return
	}

	// List in dependency order
	order := []string{"libglapi.dll", "libgallium_wgl.dll", "opengl32.dll"}
	listed := make(map[string]bool)

	for _, name := range order {
		if data, ok := embeddedDLLs[name]; ok {
			fmt.Printf("  %s: %d bytes\n", name, len(data))
			listed[name] = true
		}
	}

	// List any others not in our expected order
	for name, data := range embeddedDLLs {
		if !listed[name] {
			fmt.Printf("  %s: %d bytes (unexpected)\n", name, len(data))
		}
	}

	// Check for missing expected DLLs
	expected := []string{"libglapi.dll", "libgallium_wgl.dll", "opengl32.dll"}
	for _, name := range expected {
		if _, ok := embeddedDLLs[name]; !ok {
			fmt.Printf("  %s: MISSING! (expected but not embedded)\n", name)
		}
	}
	fmt.Println()
}

func printExtractedDLLs() {
	fmt.Println("[EXTRACTED DLLs ON DISK]")
	targetDir := MesaDir()
	if targetDir == "" {
		fmt.Println("  ERROR: Could not determine Mesa directory (LOCALAPPDATA not set?)")
		fmt.Println()
		return
	}

	fmt.Printf("  Directory: %s\n", targetDir)

	if _, err := os.Stat(targetDir); os.IsNotExist(err) {
		fmt.Println("  Status: Directory does not exist (DLLs not yet extracted)")
		fmt.Println()
		return
	}

	entries, err := os.ReadDir(targetDir)
	if err != nil {
		fmt.Printf("  ERROR: Cannot read directory: %v\n", err)
		fmt.Println()
		return
	}

	if len(entries) == 0 {
		fmt.Println("  Status: Directory exists but is empty")
		fmt.Println()
		return
	}

	fmt.Println("  Contents:")
	for _, entry := range entries {
		info, _ := entry.Info()
		if info != nil {
			fmt.Printf("    %s: %d bytes\n", entry.Name(), info.Size())
		} else {
			fmt.Printf("    %s: (size unknown)\n", entry.Name())
		}
	}

	// Check for expected DLLs
	expected := []string{"libglapi.dll", "libgallium_wgl.dll", "opengl32.dll"}
	fmt.Println("  Expected DLLs:")
	for _, name := range expected {
		path := filepath.Join(targetDir, name)
		if info, err := os.Stat(path); err == nil {
			fmt.Printf("    %s: OK (%d bytes)\n", name, info.Size())
		} else {
			fmt.Printf("    %s: MISSING\n", name)
		}
	}
	fmt.Println()
}

func printVCRuntimeStatus() {
	fmt.Println("[VC++ RUNTIME STATUS]")
	sys32 := os.Getenv("SystemRoot") + "\\System32"

	runtimeDLLs := []string{
		"vcruntime140.dll",
		"vcruntime140_1.dll",
		"msvcp140.dll",
	}

	for _, dll := range runtimeDLLs {
		path := filepath.Join(sys32, dll)
		if info, err := os.Stat(path); err == nil {
			fmt.Printf("  %s: OK (%d bytes)\n", dll, info.Size())
		} else {
			fmt.Printf("  %s: NOT FOUND\n", dll)
		}
	}
	fmt.Println()
}

func printDLLLoadTest() {
	fmt.Println("[DLL LOAD TEST]")
	targetDir := MesaDir()
	if targetDir == "" {
		fmt.Println("  ERROR: Could not determine Mesa directory")
		fmt.Println()
		return
	}

	if _, err := os.Stat(targetDir); os.IsNotExist(err) {
		fmt.Println("  SKIPPED: Mesa directory does not exist")
		fmt.Println()
		return
	}

	// Set DLL search directory first
	fmt.Printf("  Setting DLL search directory: %s\n", targetDir)
	if err := setDllDirectory(targetDir); err != nil {
		fmt.Printf("  WARNING: SetDllDirectory failed: %v\n", err)
	} else {
		fmt.Println("  SetDllDirectory: OK")
	}
	fmt.Println()

	// Test loading in dependency order
	order := []string{"libglapi.dll", "libgallium_wgl.dll", "opengl32.dll"}

	for _, dll := range order {
		dllPath := filepath.Join(targetDir, dll)
		fmt.Printf("  Loading: %s\n", dllPath)

		// Check if file exists first
		if _, err := os.Stat(dllPath); os.IsNotExist(err) {
			fmt.Printf("    Result: FILE NOT FOUND\n")
			fmt.Println()
			continue
		}

		// Analyze PE imports before loading
		printPEImports(dllPath)

		// Try to load
		handle, err := windows.LoadLibraryEx(dllPath, 0, windows.LOAD_WITH_ALTERED_SEARCH_PATH)
		if err != nil {
			fmt.Printf("    Result: LOAD FAILED\n")
			fmt.Printf("    Error: %v\n", err)
			if errno, ok := err.(syscall.Errno); ok {
				fmt.Printf("    Windows error code: %d (0x%x)\n", uint32(errno), uint32(errno))
				switch uint32(errno) {
				case 126:
					fmt.Println("    Meaning: ERROR_MOD_NOT_FOUND - A dependent DLL is missing")
				case 193:
					fmt.Println("    Meaning: ERROR_BAD_EXE_FORMAT - Architecture mismatch (32/64 bit)")
				case 127:
					fmt.Println("    Meaning: ERROR_PROC_NOT_FOUND - Required function missing")
				case 5:
					fmt.Println("    Meaning: ERROR_ACCESS_DENIED - Permission denied (policy?)")
				}
			}
		} else {
			fmt.Printf("    Result: SUCCESS (handle: 0x%x)\n", handle)
			// Don't free the handle - keep it loaded
		}
		fmt.Println()
	}
}

func printPEImports(dllPath string) {
	f, err := pe.Open(dllPath)
	if err != nil {
		fmt.Printf("    PE Analysis: ERROR - %v\n", err)
		return
	}
	defer f.Close()

	imports, err := f.ImportedLibraries()
	if err != nil {
		fmt.Printf("    PE Analysis: ERROR reading imports - %v\n", err)
		return
	}

	if len(imports) == 0 {
		fmt.Println("    PE Imports: (none)")
		return
	}

	fmt.Println("    PE Imports:")
	for _, imp := range imports {
		fmt.Printf("      - %s\n", imp)
	}
}
