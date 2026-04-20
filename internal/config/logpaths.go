package config

// Canonical filenames for every log Interlink writes. The directory is returned
// by LogDirectory / LogDirectoryForUser; these constants are the filenames
// expected within that directory.
const (
	// DaemonLogName is the structured per-daemon log.
	DaemonLogName = "daemon.log"

	// DaemonStderrLogName captures raw stderr from subprocess daemon launches,
	// used for post-mortem when the daemon fails to come up.
	DaemonStderrLogName = "daemon-stderr.log"

	// StartupLogName is the very-early boot log, written before the main
	// logger is initialized. Renamed from daemon-startup.log to startup.log
	// in Plan 2; the one-time migration in RunStartupMigrations renames any
	// existing daemon-startup.log on first run of the new version.
	StartupLogName = "startup.log"

	// LegacyStartupLogName is the pre-Plan-2 filename, referenced only by
	// the migration path.
	LegacyStartupLogName = "daemon-startup.log"

	// InterlinkLogName is the GUI + CLI unified log, written when the user
	// enables file logging.
	InterlinkLogName = "interlink.log"
)
