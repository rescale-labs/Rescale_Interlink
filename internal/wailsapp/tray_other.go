//go:build !windows

package wailsapp

// The tray companion app is Windows-only.
func (a *App) launchTrayIfNeeded() {
	// No-op on non-Windows platforms
}
