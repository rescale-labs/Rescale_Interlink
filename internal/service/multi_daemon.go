// Package service provides Windows Service Control Manager integration.
package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/daemon"
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
	profile UserProfile
	daemon  *daemon.Daemon
	config  *config.DaemonConfig // v4.2.0: Use DaemonConfig instead of APIConfig
	cancel  context.CancelFunc
	running bool
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
func (m *MultiUserDaemon) scanAndUpdateProfiles() error {
	profiles, err := EnumerateUserProfiles()
	if err != nil {
		return fmt.Errorf("failed to enumerate profiles: %w", err)
	}

	m.logger.Debug().Int("profile_count", len(profiles)).Msg("Enumerated user profiles")

	m.mu.Lock()
	defer m.mu.Unlock()

	// Track which profiles we've seen
	seenProfiles := make(map[string]bool)

	// Start or update daemons for each profile
	for _, profile := range profiles {
		seenProfiles[profile.ProfilePath] = true

		// Check if we already have a daemon for this profile
		if entry, exists := m.daemons[profile.ProfilePath]; exists {
			// Check if config has changed
			if m.configChanged(entry, profile) {
				m.logger.Info().
					Str("user", profile.Username).
					Msg("Config changed, restarting user daemon")
				m.stopUserDaemon(entry)
				if err := m.startUserDaemon(profile); err != nil {
					m.logger.Error().Err(err).Str("user", profile.Username).
						Msg("Failed to restart user daemon")
				}
			}
			continue
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

	m.logger.Info().
		Str("user", profile.Username).
		Str("download_folder", daemonConf.Daemon.DownloadFolder).
		Int("poll_interval_min", daemonConf.Daemon.PollIntervalMinutes).
		Msg("Starting auto-download daemon for user")

	// Create user-specific logger
	userLogger := m.logger.WithStr("user", profile.Username)

	// v4.0.8: Use unified API key resolution with fallback chain
	// Priority: token file -> environment variable
	apiKey, apiKeySource := config.ResolveAPIKeySource("", profile.ProfilePath)
	if apiKey == "" {
		m.logger.Debug().Str("user", profile.Username).
			Msg("No API key found (checked token file, env var), skipping")
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

	// Create cancellable context for this user's daemon
	ctx, cancel := context.WithCancel(m.ctx)

	// Start the daemon
	if err := d.Start(ctx); err != nil {
		cancel()
		return fmt.Errorf("failed to start daemon for %s: %w", profile.Username, err)
	}

	// Track the daemon
	m.daemons[profile.ProfilePath] = &userDaemonEntry{
		profile: profile,
		daemon:  d,
		config:  daemonConf,
		cancel:  cancel,
		running: true,
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
func (m *MultiUserDaemon) PauseUser(identifier string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, entry := range m.daemons {
		if entry.profile.SID == identifier || entry.profile.Username == identifier {
			if entry.running {
				m.logger.Info().Str("user", entry.profile.Username).Msg("Pausing user daemon")
				m.stopUserDaemon(entry)
				return nil
			}
			return fmt.Errorf("daemon for %s is not running", identifier)
		}
	}

	return fmt.Errorf("no daemon found for identifier: %s", identifier)
}

// ResumeUser resumes auto-download for a specific user (by SID or username).
func (m *MultiUserDaemon) ResumeUser(identifier string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for path, entry := range m.daemons {
		if entry.profile.SID == identifier || entry.profile.Username == identifier {
			if !entry.running {
				m.logger.Info().Str("user", entry.profile.Username).Msg("Resuming user daemon")
				// Remove old entry and restart
				delete(m.daemons, path)
				return m.startUserDaemon(entry.profile)
			}
			return fmt.Errorf("daemon for %s is already running", identifier)
		}
	}

	return fmt.Errorf("no daemon found for identifier: %s", identifier)
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
func (m *MultiUserDaemon) TriggerUserScan(identifier string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, entry := range m.daemons {
		if entry.profile.SID == identifier || entry.profile.Username == identifier {
			if entry.running && entry.daemon != nil {
				m.logger.Info().Str("user", entry.profile.Username).Msg("Triggering scan for user")
				entry.daemon.TriggerPoll()
				return nil
			}
			return fmt.Errorf("daemon for %s is not running", identifier)
		}
	}

	return fmt.Errorf("no daemon found for identifier: %s", identifier)
}
