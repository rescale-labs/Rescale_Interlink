//go:build windows

package mesa

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// EarlyDiagnostics checks if opengl32.dll is already loaded BEFORE Mesa initialization.
// This is called from mesainit.init() BEFORE any Fyne packages are imported.
//
// If opengl32.dll is already loaded from System32, it means:
// - CGO static imports (from GLFW/Fyne) loaded it at process start, OR
// - Some other init() function loaded it before us
//
// This is critical diagnostic info - if opengl32.dll is already loaded, our
// preloading cannot override it.
func EarlyDiagnostics() {
	if os.Getenv("RESCALE_MESA_DEBUG") == "" {
		return
	}

	fmt.Println("=" + repeatStr("=", 69))
	fmt.Println("MESA EARLY DIAGNOSTICS (before any OpenGL init)")
	fmt.Println("=" + repeatStr("=", 69))
	fmt.Println()

	// Check if opengl32.dll is already loaded
	checkDLLAlreadyLoaded("opengl32.dll")
	checkDLLAlreadyLoaded("libgallium_wgl.dll")
	checkDLLAlreadyLoaded("libglapi.dll")

	fmt.Println()
}

// checkDLLAlreadyLoaded checks if a specific DLL is already in the process.
func checkDLLAlreadyLoaded(dllName string) {
	namePtr, err := syscall.UTF16PtrFromString(dllName)
	if err != nil {
		fmt.Printf("[MESA-EARLY] %s: ERROR encoding name\n", dllName)
		return
	}

	ret, _, _ := procGetModuleHandleW.Call(uintptr(unsafe.Pointer(namePtr)))
	if ret == 0 {
		fmt.Printf("[MESA-EARLY] %s: NOT LOADED (good - we can preload Mesa's version)\n", dllName)
		return
	}

	// DLL is loaded - get its path
	handle := windows.Handle(ret)
	var path [260]uint16
	n, err := windows.GetModuleFileName(handle, &path[0], 260)
	if err != nil || n == 0 {
		fmt.Printf("[MESA-EARLY] CRITICAL: %s IS ALREADY LOADED (handle: 0x%x) - path unknown\n", dllName, handle)
		return
	}

	pathStr := syscall.UTF16ToString(path[:n])
	fmt.Printf("[MESA-EARLY] CRITICAL: %s ALREADY LOADED from: %s\n", dllName, pathStr)

	// Diagnose the source
	lowerPath := lowerStr(pathStr)
	if containsStr(lowerPath, "system32") || containsStr(lowerPath, "syswow64") {
		fmt.Printf("[MESA-EARLY]   This is Windows' built-in version - Mesa CANNOT override it!\n")
		fmt.Printf("[MESA-EARLY]   Cause: Static PE import or early init() loaded System32 DLL\n")
		fmt.Printf("[MESA-EARLY]   Fix needed: Application manifest or import-order fix\n")
	} else if containsStr(lowerPath, "rescale") || containsStr(lowerPath, "mesa") {
		fmt.Printf("[MESA-EARLY]   OK: This appears to be Mesa's version\n")
	}
}

// DiagnoseLoadTiming runs comprehensive timing diagnostics.
// Call this from --mesa-doctor to understand DLL loading sequence.
func DiagnoseLoadTiming() {
	fmt.Println("[TIMING ANALYSIS]")
	fmt.Println("  This section helps determine WHEN opengl32.dll gets loaded.")
	fmt.Println()

	// Check what's loaded NOW
	dllsToCheck := []string{"opengl32.dll", "libgallium_wgl.dll", "libglapi.dll", "glfw.dll", "gdi32.dll"}

	fmt.Println("  Current DLL load state:")
	for _, dll := range dllsToCheck {
		namePtr, _ := syscall.UTF16PtrFromString(dll)
		ret, _, _ := procGetModuleHandleW.Call(uintptr(unsafe.Pointer(namePtr)))
		if ret == 0 {
			fmt.Printf("    %s: not loaded\n", dll)
		} else {
			handle := windows.Handle(ret)
			var path [260]uint16
			n, _ := windows.GetModuleFileName(handle, &path[0], 260)
			pathStr := syscall.UTF16ToString(path[:n])
			fmt.Printf("    %s: LOADED from %s\n", dll, pathStr)
		}
	}
	fmt.Println()
}

// Helper functions to avoid importing strings package (keeps init() fast)
func repeatStr(s string, n int) string {
	result := ""
	for i := 0; i < n; i++ {
		result += s
	}
	return result
}

func lowerStr(s string) string {
	result := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		result[i] = c
	}
	return string(result)
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
