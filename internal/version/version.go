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
// v4.6.2: Fix Windows auto-download daemon failures (config parsing, IPC user matching,
// scan error visibility) and fix build scripts (WiX extension pinning, ldflags path)
// v4.6.3: Fix S3 upload "stream not seekable" failure during PUR — uploadProgressReader
// with io.ReadSeeker support, reader creation moved inside retry closure
// v4.6.4: PUR feature parity + bug fixes — pattern regex fix for number-followed-by-text
// filenames (Run_335_Fluid_Meas.avg.snc), template crash fix (normalization + panic recovery
// + atomic writes), Azure proxy timeout fix (context-aware retry + proxy bypass logging),
// --extra-input-files shared across all jobs, --iterate-command-patterns CLI + GUI,
// missing CLI flags (include/exclude patterns, flatten-tar, tar-compression, worker counts,
// rm-tar-on-success), GUI enhancements for extra input files and command pattern iteration
var Version = "v4.6.4"

// BuildTime is the build timestamp, set by ldflags during build.
var BuildTime = "unknown"
