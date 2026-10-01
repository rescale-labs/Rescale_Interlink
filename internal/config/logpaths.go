package config

// Canonical filenames for every log Interlink writes. The directory is returned
// by LogDirectory; these constants are the filenames
// expected within that directory.
const (
	// DaemonLogName is the structured per-daemon log.
	DaemonLogName = "daemon.log"

	// DaemonStderrLogName captures raw stderr from subprocess daemon launches,
	// used for post-mortem when the daemon fails to come up.
	DaemonStderrLogName = "daemon-stderr.log"

	// StartupLogName is the very-early boot log, written before the main
	// logger is initialized. RunStartupMigrations renames an earlier
	// version's daemon-startup.log to it.
	StartupLogName = "startup.log"

	// LegacyStartupLogName is the name earlier versions used, referenced only
	// by the migration.
	LegacyStartupLogName = "daemon-startup.log"

	// InterlinkLogName is the GUI + CLI unified log, written when the user
	// enables file logging.
	InterlinkLogName = "interlink.log"
)
