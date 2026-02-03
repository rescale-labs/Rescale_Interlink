// Package version provides build version information for the application.
// This is a separate package to avoid import cycles between cli and service packages.
package version

// Version is the build version string, set by ldflags during build.
// Format: vX.Y.Z or vX.Y.Z-dev for development builds.
// v4.5.7: Auto-download settings auto-save fix (all fields debounce-save, fields editable before checkbox)
var Version = "v4.5.7"

// BuildTime is the build timestamp, set by ldflags during build.
var BuildTime = "unknown"
