//go:build !windows

package mesa

// EnsureSoftwareRendering is a no-op on non-Windows platforms.
// Mesa software rendering is only needed on Windows where GPU/OpenGL
// support may be unavailable (VMs, RDP sessions, etc.).
func EnsureSoftwareRendering() error {
	return nil
}

// IsSoftwareRenderingEnabled always returns false on non-Windows platforms.
func IsSoftwareRenderingEnabled() bool {
	return false
}

// GetExeDir returns the directory containing the running executable.
// Stub for non-Windows platforms.
func GetExeDir() (string, error) {
	return "", nil
}

// DLLsExistInDir always returns true on non-Windows platforms (no DLLs needed).
func DLLsExistInDir(dir string) bool {
	return true
}

// ExtractDLLsToDir is a no-op on non-Windows platforms.
func ExtractDLLsToDir(dir string) error {
	return nil
}

// HasEmbeddedDLLs always returns false on non-Windows platforms.
func HasEmbeddedDLLs() bool {
	return false
}
