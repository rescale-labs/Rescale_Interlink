//go:build !windows

package wailsapp

// v4.7.6: No-op stub for non-Windows platforms.
// The tray companion app is Windows-only.

func (a *App) launchTrayIfNeeded() {
	// No-op on non-Windows platforms
}
