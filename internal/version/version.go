// Package version provides build version information for the application.
// This is a separate package to avoid import cycles between cli and service packages.
package version

// Version is the build version string, set by ldflags during build.
// Format: vX.Y.Z or vX.Y.Z-dev for development builds.
// v4.6.0: PUR pipeline fixes — submitMode normalization, shared state manager, terminal-state accounting,
// context-aware cancellation, GUI diagnostics (events, stage stats, log panel, error display),
// TarSubpath field, readable tar filenames with FNV hash
// v4.6.1: Fix PUR jobs failing with "The specified version is not available" — resolve
// analysis version display names to versionCodes in both frontend (TemplateBuilder) and
// backend (pipeline resolveAnalysisVersions), with preflight validation
var Version = "v4.6.1"

// BuildTime is the build timestamp, set by ldflags during build.
var BuildTime = "unknown"
