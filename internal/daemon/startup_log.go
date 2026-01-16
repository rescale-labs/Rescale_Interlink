//go:build !windows

// Package daemon provides the auto-download daemon functionality.
package daemon

// StartupLogPath returns the path to the daemon startup log.
// v4.3.8: On non-Windows platforms, this is a no-op stub.
// The startup log is primarily for debugging Windows subprocess launch issues.
func StartupLogPath() string {
	return ""
}

// WriteStartupLog is a no-op on non-Windows platforms.
// v4.3.8: The startup log is primarily for debugging Windows subprocess launch issues.
func WriteStartupLog(format string, args ...interface{}) {
	// No-op on non-Windows
}

// ClearStartupLog is a no-op on non-Windows platforms.
// v4.3.8: The startup log is primarily for debugging Windows subprocess launch issues.
func ClearStartupLog() {
	// No-op on non-Windows
}
