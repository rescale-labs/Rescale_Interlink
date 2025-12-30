//go:build windows

package mesa

import (
	"debug/pe"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Doctor runs comprehensive Mesa diagnostics and prints results.
// This is designed to be run standalone (--mesa-doctor) to debug DLL loading issues.
//
// The diagnostic flow is carefully ordered to reveal the root cause:
// 1. Build info & environment - what are we working with?
// 2. System info - Windows version, architecture, DLL settings
// 3. DLL search order explanation
// 4. Static imports - is opengl32.dll baked into the EXE?
// 5. Manifest/DotLocal check - is redirection configured?
// 6. KnownDLLs registry - does Windows bypass DLL search order?
// 7. Full module enumeration BEFORE preload - what's already loaded?
// 8. DLL resources - are Mesa DLLs available?
// 9. DLL load test - can we load Mesa DLLs?
// 10. Full module enumeration AFTER preload - did it work?
// 11. Final diagnosis and recommendations
func Doctor() {
	fmt.Println("=" + strings.Repeat("=", 79))
	fmt.Println("                 MESA SOFTWARE RENDERING DIAGNOSTICS v4.0.0")
	fmt.Println("=" + strings.Repeat("=", 79))
	fmt.Println()
	fmt.Println("This comprehensive diagnostic reveals WHY Mesa may not be working on Windows.")
	fmt.Println("It analyzes DLL loading behavior, system configuration, and provides")
	fmt.Println("actionable recommendations based on the findings.")
	fmt.Println()
	fmt.Println("Run with RESCALE_MESA_DEBUG=1 for additional early-init diagnostics.")
	fmt.Println()
	fmt.Println("-" + strings.Repeat("-", 79))
	fmt.Println()

	// Section 1: Build and Environment
	fmt.Println("=== SECTION 1: BUILD AND ENVIRONMENT ===")
	fmt.Println()
	printBuildInfo()
	printEnvironment()

	// Section 2: System Configuration
	fmt.Println("=== SECTION 2: SYSTEM CONFIGURATION ===")
	fmt.Println()
	printSystemInfo()
	printDllSearchOrder()

	// Section 3: PE Analysis (CRITICAL)
	fmt.Println("=== SECTION 3: PE ANALYSIS (CRITICAL) ===")
	fmt.Println()
	printExecutableImports()
	printManifestInfo()

	// Section 4: Windows DLL Registry
	fmt.Println("=== SECTION 4: WINDOWS DLL REGISTRY ===")
	fmt.Println()
	printKnownDLLs()

	// Section 5: Current Module State (BEFORE)
	fmt.Println("=== SECTION 5: CURRENT MODULE STATE (BEFORE PRELOAD) ===")
	fmt.Println()
	printAllLoadedModules("BEFORE MESA PRELOAD")
	printLoadedModules("BEFORE MESA PRELOAD (OpenGL summary)")

	// Section 6: Mesa Resources
	fmt.Println("=== SECTION 6: MESA RESOURCES ===")
	fmt.Println()
	printEmbeddedDLLs()
	printExtractedDLLs()
	printVCRuntimeStatus()

	// Section 7: DLL Loading Attempt
	fmt.Println("=== SECTION 7: DLL LOADING ATTEMPT ===")
	fmt.Println()
	printDLLLoadTest()

	// Section 8: Module State (AFTER)
	fmt.Println("=== SECTION 8: MODULE STATE (AFTER PRELOAD) ===")
	fmt.Println()
	printAllLoadedModules("AFTER MESA PRELOAD")
	printLoadedModules("AFTER MESA PRELOAD (OpenGL summary)")

	// Section 9: Final Analysis
	fmt.Println("=== SECTION 9: FINAL ANALYSIS AND RECOMMENDATIONS ===")
	fmt.Println()
	printDiagnosisSummary()
	printRecommendations()

	fmt.Println()
	fmt.Println("=" + strings.Repeat("=", 79))
	fmt.Println("                         END OF DIAGNOSTICS")
	fmt.Println("=" + strings.Repeat("=", 79))
}

// printRecommendations provides specific actionable recommendations.
func printRecommendations() {
	fmt.Println("[RECOMMENDATIONS]")
	fmt.Println()

	// Check current state
	namePtr, _ := syscall.UTF16PtrFromString("opengl32.dll")
	ret, _, _ := procGetModuleHandleW.Call(uintptr(unsafe.Pointer(namePtr)))

	if ret != 0 {
		handle := windows.Handle(ret)
		var path [260]uint16
		n, _ := windows.GetModuleFileName(handle, &path[0], 260)
		pathStr := syscall.UTF16ToString(path[:n])
		lowerPath := strings.ToLower(pathStr)

		if strings.Contains(lowerPath, "system32") {
			fmt.Println("  Based on findings: System32's opengl32.dll is loaded.")
			fmt.Println()
			fmt.Println("  IMMEDIATE ACTIONS TO TRY:")
			fmt.Println("  1. Ensure .local file exists next to EXE")
			fmt.Println("     - File: rescale-int.exe.local (can be empty)")
			fmt.Println("     - This enables DotLocal DLL redirection")
			fmt.Println()
			fmt.Println("  2. Ensure manifest file exists")
			fmt.Println("     - File: rescale-int.exe.manifest")
			fmt.Println("     - Should declare DLL redirection")
			fmt.Println()
			fmt.Println("  3. Place Mesa DLLs in EXE directory")
			fmt.Println("     - Copy opengl32.dll, libgallium_wgl.dll, libglapi.dll")
			fmt.Println("     - From: %LOCALAPPDATA%\\rescale-int\\mesa\\")
			fmt.Println("     - To: Same directory as rescale-int.exe")
			fmt.Println()
			fmt.Println("  4. Test with explicit environment")
			fmt.Println("     - Set GALLIUM_DRIVER=llvmpipe")
			fmt.Println("     - Set LIBGL_ALWAYS_SOFTWARE=1")
			fmt.Println()
			fmt.Println("  IF NONE OF THE ABOVE WORK:")
			fmt.Println("  The static PE import of opengl32.dll is the root cause.")
			fmt.Println("  This requires one of:")
			fmt.Println("    a. Patching GLFW to use LoadLibrary instead of static import")
			fmt.Println("    b. Using ANGLE backend (OpenGL ES over DirectX)")
			fmt.Println("    c. A launcher EXE that pre-loads Mesa before starting main EXE")
		} else {
			fmt.Println("  Mesa's opengl32.dll appears to be loaded correctly!")
			fmt.Println()
			fmt.Println("  If the GUI still doesn't work, check:")
			fmt.Println("  1. Are all Mesa DLLs present?")
			fmt.Println("     - opengl32.dll (Mesa frontend)")
			fmt.Println("     - libgallium_wgl.dll (Gallium driver)")
			fmt.Println("     - libglapi.dll (GL API dispatch)")
			fmt.Println()
			fmt.Println("  2. Are VC++ redistributables installed?")
			fmt.Println("     - vcruntime140.dll")
			fmt.Println("     - vcruntime140_1.dll")
			fmt.Println("     - msvcp140.dll")
			fmt.Println()
			fmt.Println("  3. Check Fyne/GLFW compatibility with Mesa")
		}
	} else {
		fmt.Println("  No opengl32.dll is currently loaded.")
		fmt.Println("  This is unusual at this point in diagnostics.")
		fmt.Println()
		fmt.Println("  Check:")
		fmt.Println("  1. Mesa DLLs are properly extracted")
		fmt.Println("  2. No file permission issues")
		fmt.Println("  3. DLLs aren't corrupted")
	}

	fmt.Println()
	fmt.Println("  For detailed debugging:")
	fmt.Println("  - Run with RESCALE_MESA_DEBUG=1 to see early init diagnostics")
	fmt.Println("  - Use Windows Process Monitor to trace DLL loading")
	fmt.Println("  - Use Dependency Walker to analyze DLL dependencies")
	fmt.Println()
}

// printDiagnosisSummary provides a final summary of what we found.
func printDiagnosisSummary() {
	fmt.Println("[DIAGNOSIS SUMMARY]")
	fmt.Println()

	// Check if opengl32 is loaded and from where
	namePtr, _ := syscall.UTF16PtrFromString("opengl32.dll")
	ret, _, _ := procGetModuleHandleW.Call(uintptr(unsafe.Pointer(namePtr)))

	if ret == 0 {
		fmt.Println("  opengl32.dll is NOT loaded.")
		fmt.Println("  This is unusual - OpenGL should have been loaded by now.")
		fmt.Println("  The GUI may fail to start.")
		fmt.Println()
		return
	}

	handle := windows.Handle(ret)
	var path [260]uint16
	n, _ := windows.GetModuleFileName(handle, &path[0], 260)
	pathStr := syscall.UTF16ToString(path[:n])
	lowerPath := strings.ToLower(pathStr)

	if strings.Contains(lowerPath, "system32") || strings.Contains(lowerPath, "syswow64") {
		fmt.Println("  RESULT: System32's opengl32.dll is loaded (Mesa NOT active)")
		fmt.Println()
		fmt.Println("  Loaded from:", pathStr)
		fmt.Println()
		fmt.Println("  EXPLANATION:")
		fmt.Println("  Windows loaded System32\\opengl32.dll before Mesa could intercept.")
		fmt.Println("  This happens due to:")
		fmt.Println("    1. Static PE imports (opengl32.dll in EXE import table)")
		fmt.Println("    2. opengl32.dll being a Windows KnownDLL")
		fmt.Println("    3. Package init() functions loading OpenGL before main()")
		fmt.Println()
		fmt.Println("  REQUIRED FIXES:")
		fmt.Println("    - Import mesainit package BEFORE any Fyne packages")
		fmt.Println("    - Add application manifest with DLL redirection")
		fmt.Println("    - Use DotLocal redirection (.local file)")
	} else if strings.Contains(lowerPath, "rescale") || strings.Contains(lowerPath, "mesa") || strings.Contains(lowerPath, "appdata") {
		fmt.Println("  SUCCESS: Mesa's opengl32.dll appears to be loaded!")
		fmt.Println()
		fmt.Println("  Loaded from:", pathStr)
		fmt.Println()
		fmt.Println("  Software rendering should be active.")
		fmt.Println("  If the GUI still fails, check for missing dependent DLLs.")
	} else {
		fmt.Println("  UNKNOWN: opengl32.dll loaded from unexpected location")
		fmt.Println()
		fmt.Println("  Loaded from:", pathStr)
		fmt.Println()
		fmt.Println("  This could be a third-party OpenGL implementation.")
	}
	fmt.Println()
}

func printBuildInfo() {
	fmt.Println("[BUILD INFO]")
	fmt.Printf("  Mesa embedded: %v\n", mesaEmbedded)
	if mesaEmbedded {
		fmt.Println("  Build variant: -tags mesa (Mesa DLLs embedded)")
	} else {
		fmt.Println("  Build variant: no mesa tag (requires hardware GPU)")
	}

	// Show executable path and process info
	if exePath, err := os.Executable(); err == nil {
		fmt.Printf("  Executable: %s\n", exePath)
	}

	// Show command line (helps debug what mode we're in)
	fmt.Printf("  Command line: %v\n", os.Args)
	fmt.Println()
}

func printEnvironment() {
	fmt.Println("[ENVIRONMENT VARIABLES]")

	// Mesa-related variables
	fmt.Println("  Mesa/OpenGL related:")
	mesaVars := []string{
		"RESCALE_HARDWARE_RENDER",
		"RESCALE_MESA_DEBUG",
		"GALLIUM_DRIVER",
		"LIBGL_ALWAYS_SOFTWARE",
		"MESA_GL_VERSION_OVERRIDE",
		"MESA_GLSL_VERSION_OVERRIDE",
		"MESA_DEBUG",
	}
	for _, v := range mesaVars {
		val := os.Getenv(v)
		if val == "" {
			fmt.Printf("    %s: (not set)\n", v)
		} else {
			fmt.Printf("    %s: %s\n", v, val)
		}
	}

	// System paths
	fmt.Println("  System paths:")
	pathVars := []string{
		"LOCALAPPDATA",
		"APPDATA",
		"SystemRoot",
		"windir",
		"ProgramFiles",
	}
	for _, v := range pathVars {
		val := os.Getenv(v)
		if val == "" {
			fmt.Printf("    %s: (not set)\n", v)
		} else {
			fmt.Printf("    %s: %s\n", v, val)
		}
	}

	// Current directory
	if cwd, err := os.Getwd(); err == nil {
		fmt.Printf("    Current directory: %s\n", cwd)
	}

	fmt.Println()
}

// printSystemInfo shows Windows system information critical for DLL loading.
func printSystemInfo() {
	fmt.Println("[SYSTEM INFORMATION]")

	// Windows version from registry
	key, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows NT\CurrentVersion`,
		registry.QUERY_VALUE,
	)
	if err == nil {
		defer key.Close()
		if productName, _, err := key.GetStringValue("ProductName"); err == nil {
			fmt.Printf("  Windows version: %s\n", productName)
		}
		if build, _, err := key.GetStringValue("CurrentBuild"); err == nil {
			fmt.Printf("  Build number: %s\n", build)
		}
	}

	// Architecture
	fmt.Printf("  Process architecture: amd64 (64-bit)\n")

	// Check SafeDllSearchMode (affects DLL search order)
	safeKey, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\Session Manager`,
		registry.QUERY_VALUE,
	)
	if err == nil {
		defer safeKey.Close()
		if val, _, err := safeKey.GetIntegerValue("SafeDllSearchMode"); err == nil {
			if val == 1 {
				fmt.Println("  SafeDllSearchMode: ENABLED (System32 searched before current directory)")
			} else {
				fmt.Println("  SafeDllSearchMode: DISABLED (current directory searched early)")
			}
		} else {
			fmt.Println("  SafeDllSearchMode: (default - enabled on modern Windows)")
		}
	}

	// Check CWDIllegalInDllSearch
	if val, _, err := safeKey.GetIntegerValue("CWDIllegalInDllSearch"); err == nil {
		fmt.Printf("  CWDIllegalInDllSearch: %d\n", val)
	}

	fmt.Println()
}

// printDllSearchOrder shows the DLL search order Windows will use.
func printDllSearchOrder() {
	fmt.Println("[DLL SEARCH ORDER]")
	fmt.Println("  Windows uses this order for LoadLibrary (without LOAD_LIBRARY_SEARCH_* flags):")
	fmt.Println()

	order := []string{
		"1. Known DLLs (from registry, ALWAYS loads from System32)",
		"2. Application directory (directory containing the EXE)",
		"3. System32 directory",
		"4. 16-bit system directory (legacy, rarely used)",
		"5. Windows directory",
		"6. Current directory (if SafeDllSearchMode disabled)",
		"7. Directories in PATH environment variable",
	}

	for _, step := range order {
		fmt.Printf("    %s\n", step)
	}

	fmt.Println()
	fmt.Println("  IMPORTANT: For opengl32.dll, step 1 (Known DLLs) typically wins!")
	fmt.Println("  Mesa must pre-load by absolute path or use manifest redirection.")
	fmt.Println()

	// Show actual paths
	if exePath, err := os.Executable(); err == nil {
		fmt.Printf("  Your EXE directory: %s\n", filepath.Dir(exePath))
	}

	sys32 := os.Getenv("SystemRoot") + "\\System32"
	fmt.Printf("  System32: %s\n", sys32)

	windir := os.Getenv("windir")
	fmt.Printf("  Windows directory: %s\n", windir)

	mesaDir := MesaDir()
	fmt.Printf("  Mesa DLL directory: %s\n", mesaDir)

	fmt.Println()
}

// printExecutableImports analyzes the running executable's PE imports
// This is CRITICAL for diagnosing the Mesa loading issue:
// If opengl32.dll appears in static imports, Windows loads System32's version
// BEFORE our code runs, and our preloading won't help.
func printExecutableImports() {
	fmt.Println("[EXECUTABLE STATIC IMPORTS - DETAILED]")

	exePath, err := os.Executable()
	if err != nil {
		fmt.Printf("  ERROR: Could not get executable path: %v\n", err)
		fmt.Println()
		return
	}
	fmt.Printf("  Executable: %s\n", exePath)

	// Get file info
	if info, err := os.Stat(exePath); err == nil {
		fmt.Printf("  Size: %d bytes\n", info.Size())
		fmt.Printf("  Modified: %v\n", info.ModTime())
	}

	f, err := pe.Open(exePath)
	if err != nil {
		fmt.Printf("  ERROR: Could not open PE file: %v\n", err)
		fmt.Println()
		return
	}
	defer f.Close()

	// Show PE characteristics
	if f.OptionalHeader != nil {
		switch h := f.OptionalHeader.(type) {
		case *pe.OptionalHeader64:
			fmt.Printf("  PE Type: PE32+ (64-bit)\n")
			fmt.Printf("  Image Base: 0x%x\n", h.ImageBase)
			fmt.Printf("  Subsystem: %d (2=GUI, 3=Console)\n", h.Subsystem)
		case *pe.OptionalHeader32:
			fmt.Printf("  PE Type: PE32 (32-bit)\n")
		}
	}

	imports, err := f.ImportedLibraries()
	if err != nil {
		fmt.Printf("  ERROR: Could not read imports: %v\n", err)
		fmt.Println()
		return
	}

	fmt.Printf("\n  Total imported DLLs: %d\n", len(imports))
	fmt.Println("  Imported DLLs:")

	hasOpenGL := false
	var graphicsRelated []string

	for _, imp := range imports {
		marker := ""
		lower := strings.ToLower(imp)

		if lower == "opengl32.dll" {
			hasOpenGL = true
			marker = " <<<< CRITICAL: STATIC IMPORT!"
		} else if strings.Contains(lower, "gdi") || strings.Contains(lower, "user32") {
			graphicsRelated = append(graphicsRelated, imp)
		}
		fmt.Printf("    - %s%s\n", imp, marker)
	}

	fmt.Println()

	// If OpenGL is statically imported, this is THE problem
	if hasOpenGL {
		fmt.Println("  " + strings.Repeat("!", 60))
		fmt.Println("  CRITICAL FINDING: opengl32.dll is STATICALLY IMPORTED")
		fmt.Println("  " + strings.Repeat("!", 60))
		fmt.Println()
		fmt.Println("  What this means:")
		fmt.Println("    - Windows PE loader sees opengl32.dll in import table")
		fmt.Println("    - PE loader loads opengl32.dll BEFORE any Go code runs")
		fmt.Println("    - Since opengl32.dll is a Known DLL, it loads from System32")
		fmt.Println("    - By the time main() starts, System32\\opengl32.dll is already loaded")
		fmt.Println("    - Our Mesa preloading code CANNOT override this")
		fmt.Println()
		fmt.Println("  Why this happens:")
		fmt.Println("    - CGO builds with '-lopengl32' linker flag")
		fmt.Println("    - GLFW uses static WGL imports")
		fmt.Println("    - Fyne depends on go-gl/glfw which links opengl32")
		fmt.Println()
		fmt.Println("  Possible solutions (in order of feasibility):")
		fmt.Println("    1. Application manifest with .local file (may help)")
		fmt.Println("    2. Patch go-gl/glfw to use LoadLibrary instead of static import")
		fmt.Println("    3. Use ANGLE backend (OpenGL ES over DirectX)")
		fmt.Println("    4. Extract Mesa + re-exec with modified process (complex)")
	} else {
		fmt.Println("  GOOD: opengl32.dll is NOT statically imported!")
		fmt.Println("  Mesa preloading via LoadLibrary should work.")
	}

	if len(graphicsRelated) > 0 {
		fmt.Printf("\n  Graphics-related DLLs (normal): %v\n", graphicsRelated)
	}

	fmt.Println()
}

// printManifestInfo checks for application manifest.
func printManifestInfo() {
	fmt.Println("[APPLICATION MANIFEST CHECK]")

	exePath, err := os.Executable()
	if err != nil {
		fmt.Printf("  ERROR: Could not get executable path: %v\n", err)
		fmt.Println()
		return
	}

	exeDir := filepath.Dir(exePath)
	exeName := filepath.Base(exePath)

	// Check for external manifest files
	manifestPaths := []string{
		filepath.Join(exeDir, exeName+".manifest"),
		filepath.Join(exeDir, strings.TrimSuffix(exeName, ".exe")+".manifest"),
	}

	foundExternal := false
	for _, path := range manifestPaths {
		if info, err := os.Stat(path); err == nil {
			fmt.Printf("  External manifest found: %s (%d bytes)\n", path, info.Size())
			foundExternal = true

			// Try to read first few lines
			if data, err := os.ReadFile(path); err == nil {
				lines := strings.Split(string(data), "\n")
				fmt.Println("  Manifest content (first 10 lines):")
				for i, line := range lines {
					if i >= 10 {
						fmt.Println("    ...")
						break
					}
					fmt.Printf("    %s\n", strings.TrimSpace(line))
				}
			}
		}
	}

	if !foundExternal {
		fmt.Println("  No external manifest file found")
	}

	// Check for .local file
	localPath := filepath.Join(exeDir, exeName+".local")
	if info, err := os.Stat(localPath); err == nil {
		fmt.Printf("  .local file found: %s (%d bytes)\n", localPath, info.Size())
		fmt.Println("  DotLocal redirection IS enabled!")
	} else {
		fmt.Println("  No .local file found (DotLocal redirection NOT enabled)")
	}

	fmt.Println()
	fmt.Println("  Manifest/DotLocal explanation:")
	fmt.Println("    - External manifest can declare private assemblies")
	fmt.Println("    - .local file enables DotLocal DLL redirection")
	fmt.Println("    - Combined, these can override Known DLL loading")
	fmt.Println("    - Mesa includes both in the Windows build")
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

// printKnownDLLs checks the Windows KnownDLLs registry key.
// KnownDLLs are always loaded from System32, BYPASSING the normal DLL search order.
// If opengl32.dll is in this list, drop-in replacement in EXE directory won't work.
func printKnownDLLs() {
	fmt.Println("[WINDOWS KNOWN DLLs REGISTRY CHECK]")
	fmt.Println("  KnownDLLs bypass normal search order - they ALWAYS load from System32.")
	fmt.Println()

	key, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\Session Manager\KnownDLLs`,
		registry.QUERY_VALUE,
	)
	if err != nil {
		fmt.Printf("  ERROR: Cannot read KnownDLLs registry: %v\n", err)
		fmt.Println("  (This is unusual - the key should always exist on Windows)")
		fmt.Println()
		return
	}
	defer key.Close()

	// Get all value names
	names, err := key.ReadValueNames(-1)
	if err != nil {
		fmt.Printf("  ERROR: Cannot enumerate KnownDLLs values: %v\n", err)
		fmt.Println()
		return
	}

	// Check for OpenGL-related DLLs
	openglFound := false
	relevantDLLs := make([]string, 0)

	for _, name := range names {
		val, _, err := key.GetStringValue(name)
		if err != nil {
			continue
		}
		lowerVal := strings.ToLower(val)

		// Flag OpenGL-related entries
		if lowerVal == "opengl32.dll" {
			openglFound = true
			relevantDLLs = append(relevantDLLs, fmt.Sprintf("  %s = %s  <-- CRITICAL!", name, val))
		} else if strings.Contains(lowerVal, "gl") || strings.Contains(lowerVal, "gdi") {
			relevantDLLs = append(relevantDLLs, fmt.Sprintf("  %s = %s  (graphics-related)", name, val))
		}
	}

	// Print findings
	fmt.Printf("  Total KnownDLLs entries: %d\n", len(names))
	fmt.Println()

	if len(relevantDLLs) > 0 {
		fmt.Println("  Graphics-related KnownDLLs:")
		for _, line := range relevantDLLs {
			fmt.Println(line)
		}
		fmt.Println()
	}

	if openglFound {
		fmt.Println("  DIAGNOSIS: opengl32.dll IS a Known DLL on this system")
		fmt.Println("  Impact: Windows ALWAYS loads System32\\opengl32.dll regardless of:")
		fmt.Println("    - EXE directory placement")
		fmt.Println("    - SetDllDirectory calls")
		fmt.Println("    - PATH environment variable")
		fmt.Println()
		fmt.Println("  Mesa solution requires either:")
		fmt.Println("    1. Pre-load by absolute path BEFORE any name-based LoadLibrary")
		fmt.Println("    2. Application manifest with privateAssembly declaration")
		fmt.Println("    3. DotLocal redirection (.local file next to EXE)")
	} else {
		fmt.Println("  OK: opengl32.dll is NOT in KnownDLLs on this system (rare)")
		fmt.Println("  EXE directory or SetDllDirectory approaches may work.")
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

// Windows API procedures (kernel32 is declared in mesa_windows.go)
var (
	procGetModuleHandleW       = kernel32.NewProc("GetModuleHandleW")
	procCreateToolhelp32Snapshot = kernel32.NewProc("CreateToolhelp32Snapshot")
	procModule32FirstW         = kernel32.NewProc("Module32FirstW")
	procModule32NextW          = kernel32.NewProc("Module32NextW")
)

// Constants for CreateToolhelp32Snapshot
const (
	TH32CS_SNAPMODULE   = 0x00000008
	TH32CS_SNAPMODULE32 = 0x00000010
)

// MODULEENTRY32W structure
type moduleEntry32W struct {
	Size         uint32
	ModuleID     uint32
	ProcessID    uint32
	GlblcntUsage uint32
	ProccntUsage uint32
	ModBaseAddr  uintptr
	ModBaseSize  uint32
	HModule      windows.Handle
	Module       [256]uint16 // MAX_MODULE_NAME32 + 1
	ExePath      [260]uint16 // MAX_PATH
}

// printLoadedModules shows which OpenGL-related DLLs are currently loaded in the process
// This is critical for diagnosing whether Mesa's opengl32.dll or System32's is being used
func printLoadedModules(when string) {
	fmt.Printf("[LOADED MODULES - %s]\n", when)

	dllsToCheck := []string{"opengl32.dll", "libgallium_wgl.dll", "libglapi.dll"}

	for _, dllName := range dllsToCheck {
		namePtr, err := syscall.UTF16PtrFromString(dllName)
		if err != nil {
			fmt.Printf("  %s: ERROR encoding name\n", dllName)
			continue
		}

		// Call GetModuleHandleW directly
		ret, _, _ := procGetModuleHandleW.Call(uintptr(unsafe.Pointer(namePtr)))
		handle := windows.Handle(ret)
		if handle == 0 {
			fmt.Printf("  %s: NOT LOADED in process\n", dllName)
			continue
		}

		// Get the full path of the loaded DLL
		var path [260]uint16
		n, err := windows.GetModuleFileName(handle, &path[0], 260)
		if err != nil || n == 0 {
			fmt.Printf("  %s: LOADED (handle: 0x%x) but path unknown\n", dllName, handle)
			continue
		}

		pathStr := syscall.UTF16ToString(path[:n])
		fmt.Printf("  %s: LOADED from %s\n", dllName, pathStr)

		// Flag if System32's opengl32.dll is loaded (this would be the problem)
		if dllName == "opengl32.dll" {
			lower := strings.ToLower(pathStr)
			if strings.Contains(lower, "system32") || strings.Contains(lower, "syswow64") {
				fmt.Printf("    WARNING: This is Windows' built-in OpenGL - Mesa not active!\n")
			} else if strings.Contains(lower, "rescale-int") {
				fmt.Printf("    OK: This appears to be Mesa's OpenGL\n")
			}
		}
	}
	fmt.Println()
}

// printAllLoadedModules uses CreateToolhelp32Snapshot to enumerate EVERY module
// loaded in the current process. This is more comprehensive than just checking
// specific DLLs - it shows the complete picture.
func printAllLoadedModules(when string) {
	fmt.Printf("[FULL MODULE ENUMERATION - %s]\n", when)
	fmt.Println("  Listing ALL modules loaded in this process:")
	fmt.Println()

	// Get current process ID
	pid := windows.GetCurrentProcessId()

	// Create snapshot of all modules
	ret, _, err := procCreateToolhelp32Snapshot.Call(
		uintptr(TH32CS_SNAPMODULE|TH32CS_SNAPMODULE32),
		uintptr(pid),
	)
	if ret == uintptr(windows.InvalidHandle) {
		fmt.Printf("  ERROR: CreateToolhelp32Snapshot failed: %v\n", err)
		fmt.Println()
		return
	}
	snapshot := windows.Handle(ret)
	defer windows.CloseHandle(snapshot)

	// Prepare module entry structure
	var entry moduleEntry32W
	entry.Size = uint32(unsafe.Sizeof(entry))

	// Get first module
	ret, _, err = procModule32FirstW.Call(
		uintptr(snapshot),
		uintptr(unsafe.Pointer(&entry)),
	)
	if ret == 0 {
		fmt.Printf("  ERROR: Module32FirstW failed: %v\n", err)
		fmt.Println()
		return
	}

	// Categorize modules
	var openglModules []string
	var graphicsModules []string
	var systemModules []string
	var otherModules []string

	for {
		moduleName := syscall.UTF16ToString(entry.Module[:])
		exePath := syscall.UTF16ToString(entry.ExePath[:])
		lowerPath := strings.ToLower(exePath)
		lowerName := strings.ToLower(moduleName)

		// Categorize
		isOpenGL := lowerName == "opengl32.dll" ||
			strings.Contains(lowerName, "mesa") ||
			strings.Contains(lowerName, "gallium") ||
			strings.Contains(lowerName, "libglapi")

		isGraphics := strings.Contains(lowerName, "gl") ||
			strings.Contains(lowerName, "gdi") ||
			strings.Contains(lowerName, "d3d") ||
			strings.Contains(lowerName, "dxgi") ||
			strings.Contains(lowerName, "vulkan")

		isSystem := strings.Contains(lowerPath, "windows\\system32") ||
			strings.Contains(lowerPath, "windows\\syswow64")

		line := fmt.Sprintf("    %s -> %s", moduleName, exePath)

		if isOpenGL {
			if strings.Contains(lowerPath, "system32") {
				line += "  <-- SYSTEM OPENGL (problem!)"
			} else {
				line += "  <-- MESA"
			}
			openglModules = append(openglModules, line)
		} else if isGraphics {
			graphicsModules = append(graphicsModules, line)
		} else if isSystem {
			systemModules = append(systemModules, line)
		} else {
			otherModules = append(otherModules, line)
		}

		// Get next module
		ret, _, _ = procModule32NextW.Call(
			uintptr(snapshot),
			uintptr(unsafe.Pointer(&entry)),
		)
		if ret == 0 {
			break
		}
	}

	// Print OpenGL modules first (most important)
	if len(openglModules) > 0 {
		fmt.Println("  OpenGL/Mesa modules:")
		for _, line := range openglModules {
			fmt.Println(line)
		}
		fmt.Println()
	} else {
		fmt.Println("  OpenGL/Mesa modules: (none loaded)")
		fmt.Println()
	}

	// Print graphics modules
	if len(graphicsModules) > 0 {
		fmt.Println("  Other graphics modules:")
		for _, line := range graphicsModules {
			fmt.Println(line)
		}
		fmt.Println()
	}

	// Summary
	total := len(openglModules) + len(graphicsModules) + len(systemModules) + len(otherModules)
	fmt.Printf("  Total modules loaded: %d (OpenGL: %d, Graphics: %d, System: %d, Other: %d)\n",
		total, len(openglModules), len(graphicsModules), len(systemModules), len(otherModules))
	fmt.Println()
}
