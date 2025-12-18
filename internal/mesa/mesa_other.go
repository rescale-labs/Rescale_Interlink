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
