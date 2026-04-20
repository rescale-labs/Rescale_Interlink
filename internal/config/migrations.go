package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/rescale/rescale-int/internal/logging"
)

// MigrationScope describes which profiles a migration run should touch.
type MigrationScope int

const (
	// ScopeCurrentUser migrates only the current process's files. Used by
	// the GUI, CLI, and subprocess daemon entry points.
	ScopeCurrentUser MigrationScope = iota

	// ScopeAllProfiles migrates every enumerable user profile on the
	// machine. Used by the Windows Service main (running as SYSTEM). The
	// caller supplies the profile list — this package does not import
	// service/ to avoid a cycle.
	ScopeAllProfiles
)

// ProfileMigrationTarget is a single Windows profile directory that the
// ScopeAllProfiles migrations should touch. The service enumerator fills in
// Username/SID/ProfilePath; the migration iterates over these.
//
// SID is required for the Windows token ACL tightening under spec §11.2
// (applyTokenFileACL). When the migration runs under SYSTEM (service
// scope), the current process SID is NOT the user's — the SID field here
// is the authoritative source. An empty SID causes the ACL step to be
// skipped with a WARN (the copied file keeps its inherited permissions,
// matching pre-Plan-4 behavior).
type ProfileMigrationTarget struct {
	Username    string
	SID         string
	ProfilePath string
}

// RunStartupMigrations executes all one-time file migrations Plan 2 introduces.
//
// Migrations included:
//  1. Windows: token + config.csv from AppData\Roaming to AppData\Local.
//  2. Unix: state file from ~/.config/rescale-int/ to ~/.config/rescale/.
//     (The state file is also migrated lazily by State.Load(); this is
//     the eager path.)
//  3. macOS: logs from ~/Library/Application Support/rescale/logs to
//     ~/.config/rescale/logs.
//  4. All platforms: rename daemon-startup.log to startup.log.
//
// All migrations are idempotent and safe to call on every startup. Each
// migration logs at WARN on failure and continues — a migration failure
// never blocks startup. The copy-then-verify-then-delete ordering ensures
// a rollback to the previous binary still finds the source file until the
// delete completes, and the read-side path-const fallbacks tolerate files
// in either old or new locations during the transition release.
//
// For ScopeAllProfiles, callers (the Windows Service main) must pass the
// enumerated profile list; an empty slice is treated as "no profiles"
// rather than "all profiles."
func RunStartupMigrations(logger *logging.Logger, scope MigrationScope, profiles []ProfileMigrationTarget) {
	migrateStartupLogFilename(logger)

	if runtime.GOOS == "darwin" {
		migrateMacOSLogs(logger)
	}

	if runtime.GOOS == "windows" {
		// Current-user migration always runs; the service extends to all
		// profiles so the service-read paths (per-profile config.csv and
		// token) land in Local regardless of who enumerated them.
		migrateCurrentUserWindowsCredentials(logger)
		if scope == ScopeAllProfiles {
			for _, p := range profiles {
				migratePerProfileWindowsCredentials(logger, p)
			}
		}
	}
}

// migrateStartupLogFilename renames <logdir>/daemon-startup.log to
// <logdir>/startup.log on every OS. The new name is authoritative going
// forward.
func migrateStartupLogFilename(logger *logging.Logger) {
	dir := LogDirectory()
	if dir == "" {
		return
	}
	oldPath := filepath.Join(dir, LegacyStartupLogName)
	newPath := filepath.Join(dir, StartupLogName)
	if err := copyThenRemove(oldPath, newPath); err != nil {
		if logger != nil {
			logger.Warn().Err(err).
				Str("from", oldPath).
				Str("to", newPath).
				Msg("Startup-log rename migration failed")
		}
	}
}

// migrateMacOSLogs copies *.log from the macOS legacy Application-Support
// location to ~/.config/rescale/logs, then removes the legacy directory if
// it is empty.
func migrateMacOSLogs(logger *logging.Logger) {
	src := MacOSLegacyLogDirectory()
	dst := LogDirectory()
	if src == "" || dst == "" || src == dst {
		return
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return // Source does not exist — nothing to do.
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		from := filepath.Join(src, e.Name())
		to := filepath.Join(dst, e.Name())
		if err := copyThenRemove(from, to); err != nil && logger != nil {
			logger.Warn().Err(err).
				Str("from", from).
				Str("to", to).
				Msg("macOS log migration failed")
		}
	}
	// Best-effort cleanup of the source directory if empty.
	_ = os.Remove(src)
}

// migrateCurrentUserWindowsCredentials moves the current user's token and
// config.csv from Roaming to Local (per spec §3.2). Token files re-apply
// the explicit ACL after copy using the current process SID.
func migrateCurrentUserWindowsCredentials(logger *logging.Logger) {
	oldDir := firstOldWindowsDir()
	newDir := getConfigDir()
	if oldDir == "" || newDir == "" || oldDir == newDir {
		return
	}
	// Capture the current user's SID once; reuse for the token entry.
	// currentUserSID() is the Windows-only helper defined alongside
	// applyTokenFileACL; returns "" on non-Windows.
	sid, sidErr := currentUserSID()
	if sidErr != nil && logger != nil {
		logger.Warn().Err(sidErr).Msg("Could not capture current user SID for migrated token ACL")
	}
	for _, name := range []string{"token", "config.csv"} {
		from := filepath.Join(oldDir, name)
		to := filepath.Join(newDir, name)
		if err := copyThenRemove(from, to); err != nil {
			if logger != nil {
				logger.Warn().Err(err).
					Str("from", from).
					Str("to", to).
					Msg("Windows credential migration (current user) failed")
			}
			continue
		}
		if name == "token" && sid != "" {
			if aclErr := applyTokenFileACL(to, sid); aclErr != nil && logger != nil {
				logger.Warn().Err(aclErr).
					Str("to", to).
					Msg("Could not apply explicit ACL to migrated token (current user)")
			}
		}
	}
}

// migratePerProfileWindowsCredentials does the same under an explicit
// user-profile root. Service-mode scope: this runs under SYSTEM, so the
// target user's SID must come from the ProfileMigrationTarget, NOT the
// current process SID (which would be SYSTEM's).
func migratePerProfileWindowsCredentials(logger *logging.Logger, p ProfileMigrationTarget) {
	if p.ProfilePath == "" {
		return
	}
	oldBase := filepath.Join(p.ProfilePath, "AppData", "Roaming", "Rescale", "Interlink")
	newBase := filepath.Join(p.ProfilePath, "AppData", "Local", "Rescale", "Interlink")
	for _, name := range []string{"token", "config.csv"} {
		from := filepath.Join(oldBase, name)
		to := filepath.Join(newBase, name)
		if err := copyThenRemove(from, to); err != nil {
			if logger != nil {
				logger.Warn().Err(err).
					Str("user", p.Username).
					Str("from", from).
					Str("to", to).
					Msg("Windows credential migration (per profile) failed")
			}
			continue
		}
		if name == "token" {
			if p.SID == "" {
				if logger != nil {
					logger.Warn().
						Str("user", p.Username).
						Str("to", to).
						Msg("No SID available for migrated token; skipping explicit ACL (file retains inherited permissions)")
				}
				continue
			}
			if aclErr := applyTokenFileACL(to, p.SID); aclErr != nil && logger != nil {
				logger.Warn().Err(aclErr).
					Str("user", p.Username).
					Str("to", to).
					Msg("Could not apply explicit ACL to migrated token (per profile)")
			}
		}
	}
}

// firstOldWindowsDir returns the single legacy Windows config dir, or empty
// if not applicable (non-Windows, or env vars missing).
func firstOldWindowsDir() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	if dirs := getOldConfigDirs(); len(dirs) > 0 {
		return dirs[0]
	}
	return ""
}

// copyThenRemove copies src to dst and, on verified success, removes src.
// Idempotent:
//   - src missing → no-op, no error.
//   - dst already present → no-op, source left alone (avoid overwrite).
//   - partial failure → source preserved; dst cleaned up if created.
//
// Cross-volume moves fall through to copy since os.Rename may fail.
func copyThenRemove(src, dst string) error {
	if src == "" || dst == "" || src == dst {
		return nil
	}
	srcInfo, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat source: %w", err)
	}
	if srcInfo.IsDir() {
		return fmt.Errorf("source is a directory, not a file: %s", src)
	}
	if _, err := os.Stat(dst); err == nil {
		return nil // Don't overwrite an existing target.
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return fmt.Errorf("create target dir: %w", err)
	}

	if err := copyFile(src, dst, srcInfo.Mode().Perm()); err != nil {
		_ = os.Remove(dst)
		return err
	}

	// Verify by stat — the copy must have produced a readable file.
	if _, err := os.Stat(dst); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("verify target: %w", err)
	}

	if err := os.Remove(src); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove source: %w", err)
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create target: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy contents: %w", err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return fmt.Errorf("sync target: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close target: %w", err)
	}
	return nil
}
