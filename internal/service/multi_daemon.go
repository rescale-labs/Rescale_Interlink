// Package service provides Windows Service Control Manager integration.
package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/daemon"
	"github.com/rescale/rescale-int/internal/ipc"
	"github.com/rescale/rescale-int/internal/logging"
)

// MultiUserDaemon orchestrates auto-download for multiple user profiles.
// On Windows, this allows a single service to download jobs for all users
// who have configured auto-download.
type MultiUserDaemon struct {
	logger *logging.Logger

	// Per-user daemons (keyed by profile path)
	daemons map[string]*userDaemonEntry
	mu      sync.RWMutex

	// Profile rescan interval (how often to check for new/removed profiles)
	rescanInterval time.Duration

	// Shutdown coordination
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// userDaemonEntry tracks a daemon for a specific user.
type userDaemonEntry struct {
	profile    UserProfile
	daemon     *daemon.Daemon
	config     *config.DaemonConfig // v4.2.0: Use DaemonConfig instead of APIConfig
	cancel     context.CancelFunc
	running    bool
	logBuffer  *daemon.LogBuffer // v4.5.0: Per-user log buffer for IPC GetRecentLogs
	skipReason string            // v4.7.6: Reason user was skipped (e.g., "no_api_key")
}

// NewMultiUserDaemon creates a new multi-user daemon orchestrator.
func NewMultiUserDaemon(logger *logging.Logger) *MultiUserDaemon {
	ctx, cancel := context.WithCancel(context.Background())
	return &MultiUserDaemon{
		logger:         logger,
		daemons:        make(map[string]*userDaemonEntry),
		rescanInterval: 5 * time.Minute, // Rescan for new profiles every 5 minutes
		ctx:            ctx,
		cancel:         cancel,
	}
}

// Start begins the multi-user daemon orchestration.
// It scans for user profiles and starts daemons for each user with valid config.
func (m *MultiUserDaemon) Start() error {
	m.logger.Info().Msg("Starting multi-user auto-download daemon")

	// Initial profile scan
	if err := m.scanAndUpdateProfiles(); err != nil {
		m.logger.Warn().Err(err).Msg("Initial profile scan had errors (will continue)")
	}

	// Start the profile rescan loop
	m.wg.Add(1)
	go m.rescanLoop()

	return nil
}

// Stop stops all user daemons and the orchestrator.
func (m *MultiUserDaemon) Stop() {
	m.logger.Info().Msg("Stopping multi-user auto-download daemon")

	// Signal shutdown
	m.cancel()

	// Stop all user daemons
	m.mu.Lock()
	for path, entry := range m.daemons {
		if entry.running && entry.cancel != nil {
			m.logger.Debug().Str("user", entry.profile.Username).Msg("Stopping user daemon")
			entry.cancel()
			entry.daemon.Stop()
			entry.running = false
		}
		delete(m.daemons, path)
	}
	m.mu.Unlock()

	// Wait for rescan loop to exit
	m.wg.Wait()

	m.logger.Info().Msg("Multi-user daemon stopped")
}

// rescanLoop periodically scans for new or removed user profiles.
func (m *MultiUserDaemon) rescanLoop() {
	defer m.wg.Done()

	ticker := time.NewTicker(m.rescanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			m.logger.Debug().Msg("Profile rescan loop cancelled")
			return
		case <-ticker.C:
			m.logger.Debug().Msg("Rescanning user profiles")
			if err := m.scanAndUpdateProfiles(); err != nil {
				m.logger.Warn().Err(err).Msg("Profile rescan had errors")
			}
		}
	}
}

// scanAndUpdateProfiles enumerates user profiles and updates the daemon list.
// v4.5.3: Enhanced logging for profile discovery debugging.
func (m *MultiUserDaemon) scanAndUpdateProfiles() error {
	profiles, err := EnumerateUserProfiles()
	if err != nil {
		return fmt.Errorf("failed to enumerate profiles: %w", err)
	}

	// v4.5.3: Log enumeration results at Info level for visibility
	m.logger.Info().
		Int("found_profiles", len(profiles)).
		Msg("Profile rescan complete")

	m.mu.Lock()
	defer m.mu.Unlock()

	// Track which profiles we've seen
	seenProfiles := make(map[string]bool)

	// Start or update daemons for each profile
	for _, profile := range profiles {
		seenProfiles[profile.ProfilePath] = true

		// Check if we already have a daemon for this profile
		if entry, exists := m.daemons[profile.ProfilePath]; exists {
			// v4.7.6: Check if previously skipped user now has an API key
			if entry.skipReason == "no_api_key" {
				apiKey, _ := config.ResolveAPIKeySource("", profile.ProfilePath)
				if apiKey != "" {
					m.logger.Info().Str("user", profile.Username).
						Msg("API key now available — starting previously skipped user daemon")
					delete(m.daemons, profile.ProfilePath) // Remove skip entry, fall through to startUserDaemon
				} else {
					continue // Still no key, keep skipped
				}
			} else if m.configChanged(entry, profile) {
				m.logger.Info().
					Str("user", profile.Username).
					Msg("Config changed, restarting user daemon")
				m.stopUserDaemon(entry)
				if err := m.startUserDaemon(profile); err != nil {
					m.logger.Error().Err(err).Str("user", profile.Username).
						Msg("Failed to restart user daemon")
				}
				continue
			} else {
				continue
			}
		}

		// New profile - start a daemon
		if err := m.startUserDaemon(profile); err != nil {
			m.logger.Warn().Err(err).Str("user", profile.Username).
				Msg("Failed to start user daemon (will retry on next rescan)")
		}
	}

	// Stop daemons for removed profiles
	for path, entry := range m.daemons {
		if !seenProfiles[path] {
			m.logger.Info().Str("user", entry.profile.Username).
				Msg("Profile no longer valid, stopping daemon")
			m.stopUserDaemon(entry)
			delete(m.daemons, path)
		}
	}

	return nil
}

// startUserDaemon starts a daemon for a specific user profile.
// Must be called with m.mu held.
// v4.2.0: Updated to use DaemonConfig (daemon.conf) instead of APIConfig.
func (m *MultiUserDaemon) startUserDaemon(profile UserProfile) error {
	// Load user's daemon.conf
	daemonConf, err := config.LoadDaemonConfig(profile.ConfigPath)
	if err != nil {
		return fmt.Errorf("failed to load config for %s: %w", profile.Username, err)
	}

	// Check if daemon is enabled and valid
	if !daemonConf.IsEnabled() {
		// Log different messages based on why it's not enabled
		if !daemonConf.Daemon.Enabled {
			m.logger.Debug().Str("user", profile.Username).
				Msg("Auto-download disabled for user, skipping")
		} else if err := daemonConf.Validate(); err != nil {
			m.logger.Debug().Str("user", profile.Username).Err(err).
				Msg("Auto-download config has validation errors, skipping")
		} else {
			m.logger.Debug().Str("user", profile.Username).
				Msg("Auto-download config invalid, skipping")
		}
		return nil
	}

	// v4.5.3: Enhanced logging with SID and profile path for debugging daemon lookup issues
	m.logger.Info().
		Str("user", profile.Username).
		Str("sid", profile.SID).
		Str("profile_path", profile.ProfilePath).
		Str("download_folder", daemonConf.Daemon.DownloadFolder).
		Int("poll_interval_min", daemonConf.Daemon.PollIntervalMinutes).
		Msg("Starting auto-download daemon for user")

	// v4.5.0: Create per-user log writer with buffer for IPC GetRecentLogs
	// The log writer captures logs for both file output and IPC retrieval
	logDir := config.LogDirectoryForUser(profile.ProfilePath)
	// v4.5.2: Ensure log directory exists before creating log writer
	if err := os.MkdirAll(logDir, 0700); err != nil {
		m.logger.Warn().Err(err).Str("dir", logDir).Msg("Failed to create log directory")
		// Continue anyway - daemon can still run without file logs
	}
	logWriter := daemon.NewDaemonLogWriter(daemon.DaemonLogConfig{
		Console:    false, // Service mode - no console
		LogFile:    filepath.Join(logDir, "daemon.log"),
		BufferSize: 1000,
	})
	userLogger := logging.NewLoggerWithWriter(logWriter)

	// v4.0.8: Use unified API key resolution with fallback chain
	// Priority: token file -> environment variable
	apiKey, apiKeySource := config.ResolveAPIKeySource("", profile.ProfilePath)
	if apiKey == "" {
		// v4.7.6: Promote to Error level and track skip reason for retry + IPC visibility
		m.logger.Error().Str("user", profile.Username).
			Msg("No API key found (checked user-token-file, apiconfig, token-file, env var), skipping")
		m.daemons[profile.ProfilePath] = &userDaemonEntry{
			profile:    profile,
			config:     daemonConf,
			skipReason: "no_api_key",
		}
		return nil
	}

	// Log which source the API key came from (helpful for debugging)
	m.logger.Info().
		Str("user", profile.Username).
		Str("api_key_source", apiKeySource).
		Msg("API key resolved for user daemon")

	// Create app config for API client
	appCfg := &config.Config{
		APIBaseURL: "https://platform.rescale.com", // Default URL, could be configurable
		APIKey:     apiKey,
	}

	// v4.3.0: Simplified - mode is now per-job, only tag and lookback are configurable
	eligibility := &daemon.EligibilityConfig{
		AutoDownloadTag: daemonConf.Eligibility.AutoDownloadTag,
		LookbackDays:    daemonConf.Daemon.LookbackDays,
	}

	// Create job filter from daemon.conf
	var filter *daemon.JobFilter
	if daemonConf.Filters.NamePrefix != "" || daemonConf.Filters.NameContains != "" || daemonConf.Filters.Exclude != "" {
		filter = &daemon.JobFilter{
			NamePrefix:   daemonConf.Filters.NamePrefix,
			NameContains: daemonConf.Filters.NameContains,
			ExcludeNames: daemonConf.GetExcludePatterns(),
		}
	}

	// v4.5.8: Validate download folder accessibility from SYSTEM context.
	// When running as a Windows Service (SYSTEM account), mapped drives (e.g., Z:\)
	// are not accessible because they are user-session-specific. Warn and skip if so.
	downloadDir := daemonConf.Daemon.DownloadFolder
	if downloadDir != "" && len(downloadDir) >= 2 && downloadDir[1] == ':' {
		// Check if the drive letter is accessible from SYSTEM context
		if _, err := os.Stat(downloadDir); err != nil {
			driveLetter := string(downloadDir[0])
			// Check if this might be a mapped drive (not C:, not common local drives)
			if driveLetter != "C" && driveLetter != "c" {
				m.logger.Warn().
					Str("user", profile.Username).
					Str("download_folder", downloadDir).
					Str("error", err.Error()).
					Msg("Download folder may be a mapped drive inaccessible from service context. " +
						"Use a local path (e.g., C:\\Users\\...) or UNC path (\\\\server\\share) instead.")
			}
		}
	}

	// Create daemon config
	daemonCfg := &daemon.Config{
		PollInterval:  time.Duration(daemonConf.Daemon.PollIntervalMinutes) * time.Minute,
		DownloadDir:   daemonConf.Daemon.DownloadFolder,
		UseJobNameDir: daemonConf.Daemon.UseJobNameDir,
		MaxConcurrent: daemonConf.Daemon.MaxConcurrent,
		StateFile:     profile.StateFilePath,
		Eligibility:   eligibility,
		Filter:        filter,
	}

	// Create the daemon
	d, err := daemon.New(appCfg, daemonCfg, userLogger)
	if err != nil {
		return fmt.Errorf("failed to create daemon for %s: %w", profile.Username, err)
	}

	// v4.5.0: Get log buffer from the log writer for IPC GetRecentLogs
	logBuffer := logWriter.GetBuffer()

	// Create cancellable context for this user's daemon
	ctx, cancel := context.WithCancel(m.ctx)

	// Start the daemon
	if err := d.Start(ctx); err != nil {
		cancel()
		return fmt.Errorf("failed to start daemon for %s: %w", profile.Username, err)
	}

	// Track the daemon
	m.daemons[profile.ProfilePath] = &userDaemonEntry{
		profile:   profile,
		daemon:    d,
		config:    daemonConf,
		cancel:    cancel,
		running:   true,
		logBuffer: logBuffer,
	}

	m.logger.Info().Str("user", profile.Username).Msg("User daemon started successfully")
	return nil
}

// stopUserDaemon stops a running user daemon.
// Must be called with m.mu held.
func (m *MultiUserDaemon) stopUserDaemon(entry *userDaemonEntry) {
	if entry == nil || !entry.running {
		return
	}

	m.logger.Debug().Str("user", entry.profile.Username).Msg("Stopping user daemon")

	if entry.cancel != nil {
		entry.cancel()
	}
	if entry.daemon != nil {
		entry.daemon.Stop()
	}
	entry.running = false
}

// configChanged checks if the user's config has changed since we last loaded it.
// v4.2.0: Updated to use DaemonConfig (daemon.conf) instead of APIConfig.
func (m *MultiUserDaemon) configChanged(entry *userDaemonEntry, profile UserProfile) bool {
	// Reload config from disk
	newCfg, err := config.LoadDaemonConfig(profile.ConfigPath)
	if err != nil {
		// Error loading - assume changed to trigger restart
		return true
	}

	// Compare key fields
	if entry.config == nil {
		return true
	}

	oldCfg := entry.config
	if oldCfg.Daemon.Enabled != newCfg.Daemon.Enabled {
		return true
	}
	if oldCfg.Daemon.DownloadFolder != newCfg.Daemon.DownloadFolder {
		return true
	}
	if oldCfg.Daemon.PollIntervalMinutes != newCfg.Daemon.PollIntervalMinutes {
		return true
	}
	if oldCfg.Daemon.UseJobNameDir != newCfg.Daemon.UseJobNameDir {
		return true
	}
	if oldCfg.Daemon.MaxConcurrent != newCfg.Daemon.MaxConcurrent {
		return true
	}
	if oldCfg.Daemon.LookbackDays != newCfg.Daemon.LookbackDays {
		return true
	}
	// v4.3.0: Simplified eligibility - only AutoDownloadTag is configurable
	if oldCfg.Eligibility.AutoDownloadTag != newCfg.Eligibility.AutoDownloadTag {
		return true
	}
	if oldCfg.Filters.NamePrefix != newCfg.Filters.NamePrefix {
		return true
	}
	if oldCfg.Filters.NameContains != newCfg.Filters.NameContains {
		return true
	}
	if oldCfg.Filters.Exclude != newCfg.Filters.Exclude {
		return true
	}

	return false
}

// GetStatus returns the current status of all user daemons.
// v4.2.0: Updated to use DaemonConfig fields.
func (m *MultiUserDaemon) GetStatus() []UserDaemonStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var statuses []UserDaemonStatus
	for _, entry := range m.daemons {
		status := UserDaemonStatus{
			Username:       entry.profile.Username,
			ProfilePath:    entry.profile.ProfilePath,
			DownloadFolder: entry.config.Daemon.DownloadFolder,
			Running:        entry.running,
			Enabled:        entry.config.Daemon.Enabled,
			SID:            entry.profile.SID, // v4.0.8
		}

		// v4.0.8: Get stats from daemon if running
		if entry.running && entry.daemon != nil {
			status.LastScanTime = entry.daemon.GetLastPollTime()
			status.JobsDownloaded = entry.daemon.GetDownloadedCount()
			status.ActiveDownloads = entry.daemon.GetActiveDownloads()
		}

		// v4.7.6: Populate error from skip reason for IPC visibility
		if entry.skipReason == "no_api_key" {
			status.LastError = "No API key configured"
		}

		statuses = append(statuses, status)
	}

	return statuses
}

// UserDaemonStatus represents the status of a single user's daemon.
type UserDaemonStatus struct {
	Username        string
	ProfilePath     string
	DownloadFolder  string
	Running         bool
	Enabled         bool
	SID             string    // v4.0.8: Windows Security Identifier
	LastScanTime    time.Time // v4.0.8: Last poll/scan time
	JobsDownloaded  int       // v4.0.8: Total jobs downloaded
	ActiveDownloads int       // v4.0.8: Currently active downloads
	LastError       string    // v4.7.6: Error reason if daemon was skipped or failed
}

// RunningCount returns the number of currently running user daemons.
func (m *MultiUserDaemon) RunningCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	count := 0
	for _, entry := range m.daemons {
		if entry.running {
			count++
		}
	}
	return count
}

// TriggerRescan forces an immediate rescan of user profiles.
// This can be called via IPC to refresh after config changes.
func (m *MultiUserDaemon) TriggerRescan() {
	go func() {
		if err := m.scanAndUpdateProfiles(); err != nil {
			m.logger.Warn().Err(err).Msg("Triggered rescan had errors")
		}
	}()
}

// pauseUser pauses auto-download for a specific user (by SID or username).
// v4.5.3: Added SID-to-username fallback and diagnostic logging.
func (m *MultiUserDaemon) PauseUser(identifier string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// v4.5.3: Log what we're looking for
	m.logger.Info().
		Str("looking_for", identifier).
		Int("daemon_count", len(m.daemons)).
		Msg("PauseUser: searching for daemon")

	// First pass: try exact SID or username match
	for _, entry := range m.daemons {
		if entry.profile.SID == identifier ||
			strings.EqualFold(entry.profile.Username, identifier) {
			if entry.running {
				m.logger.Info().Str("user", entry.profile.Username).Msg("Pausing user daemon")
				m.stopUserDaemon(entry)
				return nil
			}
			return fmt.Errorf("daemon for %s is not running", identifier)
		}
	}

	// v4.5.3: Second pass - if identifier looks like a SID, resolve to username
	if strings.HasPrefix(identifier, "S-1-5-") {
		resolvedUsername := ResolveSIDToUsername(identifier)
		if resolvedUsername != "" {
			m.logger.Debug().
				Str("sid", identifier).
				Str("resolved_username", resolvedUsername).
				Msg("PauseUser: resolved SID to username, retrying match")

			for _, entry := range m.daemons {
				if strings.EqualFold(entry.profile.Username, resolvedUsername) {
					if entry.running {
						m.logger.Info().Str("user", entry.profile.Username).Msg("Pausing user daemon (matched via SID resolution)")
						m.stopUserDaemon(entry)
						return nil
					}
					return fmt.Errorf("daemon for %s is not running", identifier)
				}
			}
		}
	}

	// v4.5.3: Enhanced error message with diagnostic info
	var registeredUsers []string
	for _, entry := range m.daemons {
		registeredUsers = append(registeredUsers, fmt.Sprintf("%s(sid=%s)", entry.profile.Username, entry.profile.SID))
	}
	return fmt.Errorf("no daemon found for identifier: %s (registered: %v)", identifier, registeredUsers)
}

// ResumeUser resumes auto-download for a specific user (by SID or username).
// v4.5.3: Added SID-to-username fallback and diagnostic logging.
func (m *MultiUserDaemon) ResumeUser(identifier string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// v4.5.3: Log what we're looking for
	m.logger.Info().
		Str("looking_for", identifier).
		Int("daemon_count", len(m.daemons)).
		Msg("ResumeUser: searching for daemon")

	// First pass: try exact SID or username match
	for path, entry := range m.daemons {
		if entry.profile.SID == identifier ||
			strings.EqualFold(entry.profile.Username, identifier) {
			if !entry.running {
				m.logger.Info().Str("user", entry.profile.Username).Msg("Resuming user daemon")
				// Remove old entry and restart
				delete(m.daemons, path)
				return m.startUserDaemon(entry.profile)
			}
			return fmt.Errorf("daemon for %s is already running", identifier)
		}
	}

	// v4.5.3: Second pass - if identifier looks like a SID, resolve to username
	if strings.HasPrefix(identifier, "S-1-5-") {
		resolvedUsername := ResolveSIDToUsername(identifier)
		if resolvedUsername != "" {
			m.logger.Debug().
				Str("sid", identifier).
				Str("resolved_username", resolvedUsername).
				Msg("ResumeUser: resolved SID to username, retrying match")

			for path, entry := range m.daemons {
				if strings.EqualFold(entry.profile.Username, resolvedUsername) {
					if !entry.running {
						m.logger.Info().Str("user", entry.profile.Username).Msg("Resuming user daemon (matched via SID resolution)")
						// Remove old entry and restart
						delete(m.daemons, path)
						return m.startUserDaemon(entry.profile)
					}
					return fmt.Errorf("daemon for %s is already running", identifier)
				}
			}
		}
	}

	// v4.5.3: Enhanced error message with diagnostic info
	var registeredUsers []string
	for _, entry := range m.daemons {
		registeredUsers = append(registeredUsers, fmt.Sprintf("%s(sid=%s)", entry.profile.Username, entry.profile.SID))
	}
	return fmt.Errorf("no daemon found for identifier: %s (registered: %v)", identifier, registeredUsers)
}

// v4.0.8: GetTotalActiveDownloads returns the total number of active downloads across all users.
func (m *MultiUserDaemon) GetTotalActiveDownloads() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	total := 0
	for _, entry := range m.daemons {
		if entry.running && entry.daemon != nil {
			total += entry.daemon.GetActiveDownloads()
		}
	}
	return total
}

// v4.0.8: TriggerUserScan triggers a scan for a specific user (by SID or username).
// v4.5.3: Added SID-to-username fallback and diagnostic logging.
func (m *MultiUserDaemon) TriggerUserScan(identifier string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// v4.5.3: Log what we're looking for and what's available
	m.logger.Info().
		Str("looking_for", identifier).
		Int("daemon_count", len(m.daemons)).
		Msg("TriggerUserScan: searching for daemon")

	// First pass: try exact SID or username match
	for _, entry := range m.daemons {
		// v4.5.3: Log each daemon's identifiers for debugging
		m.logger.Debug().
			Str("entry_sid", entry.profile.SID).
			Str("entry_username", entry.profile.Username).
			Str("entry_path", entry.profile.ProfilePath).
			Bool("running", entry.running).
			Msg("TriggerUserScan: checking daemon entry")

		if entry.profile.SID == identifier ||
			strings.EqualFold(entry.profile.Username, identifier) {
			if entry.running && entry.daemon != nil {
				m.logger.Info().Str("user", entry.profile.Username).Msg("Triggering scan for user")
				entry.daemon.TriggerPoll()
				return nil
			}
			return fmt.Errorf("daemon for %s is not running", identifier)
		}
	}

	// v4.5.3: Second pass - if identifier looks like a SID, resolve to username
	if strings.HasPrefix(identifier, "S-1-5-") {
		resolvedUsername := ResolveSIDToUsername(identifier)
		if resolvedUsername != "" {
			m.logger.Debug().
				Str("sid", identifier).
				Str("resolved_username", resolvedUsername).
				Msg("TriggerUserScan: resolved SID to username, retrying match")

			for _, entry := range m.daemons {
				if strings.EqualFold(entry.profile.Username, resolvedUsername) {
					if entry.running && entry.daemon != nil {
						m.logger.Info().Str("user", entry.profile.Username).Msg("Triggering scan for user (matched via SID resolution)")
						entry.daemon.TriggerPoll()
						return nil
					}
					return fmt.Errorf("daemon for %s is not running", identifier)
				}
			}
		}
	}

	// v4.5.3: Enhanced error message with diagnostic info
	var registeredUsers []string
	for _, entry := range m.daemons {
		registeredUsers = append(registeredUsers, fmt.Sprintf("%s(sid=%s)", entry.profile.Username, entry.profile.SID))
	}
	return fmt.Errorf("no daemon found for identifier: %s (registered: %v)", identifier, registeredUsers)
}

// GetUserLogs returns recent log entries for a specific user (by SID or username).
// v4.5.0: Added to support per-user log retrieval via IPC.
func (m *MultiUserDaemon) GetUserLogs(identifier string, count int) []ipc.LogEntryData {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, entry := range m.daemons {
		// Match by SID (preferred) or username
		if entry.profile.SID == identifier ||
			strings.EqualFold(entry.profile.Username, identifier) {
			if entry.logBuffer != nil {
				return entry.logBuffer.GetRecent(count)
			}
			return nil
		}
	}
	return nil
}
