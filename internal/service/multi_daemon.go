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
	config  *config.APIConfig
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
func (m *MultiUserDaemon) startUserDaemon(profile UserProfile) error {
	// Load user's apiconfig
	apiCfg, err := config.LoadAPIConfig(profile.ConfigPath)
	if err != nil {
		return fmt.Errorf("failed to load config for %s: %w", profile.Username, err)
	}

	// Validate config
	if err := apiCfg.Validate(); err != nil {
		return fmt.Errorf("invalid config for %s: %w", profile.Username, err)
	}

	// Check if auto-download is enabled
	if !apiCfg.AutoDownload.Enabled {
		m.logger.Debug().Str("user", profile.Username).
			Msg("Auto-download disabled for user, skipping")
		return nil
	}

	m.logger.Info().
		Str("user", profile.Username).
		Str("download_folder", apiCfg.AutoDownload.DefaultDownloadFolder).
		Int("scan_interval_min", apiCfg.AutoDownload.ScanIntervalMinutes).
		Msg("Starting auto-download daemon for user")

	// Create user-specific logger
	userLogger := m.logger.WithStr("user", profile.Username)

	// Create app config for API client
	appCfg := &config.Config{
		APIBaseURL: apiCfg.PlatformURL,
		APIKey:     apiCfg.APIKey,
	}

	// Create eligibility config from apiconfig
	eligibility := &daemon.EligibilityConfig{
		CorrectnessTag:        apiCfg.AutoDownload.CorrectnessTag,
		AutoDownloadField:     "Auto Download",
		AutoDownloadValue:     "Enable",
		DownloadedTag:         "autoDownloaded:true",
		AutoDownloadPathField: "Auto Download Path",
		LookbackDays:          apiCfg.AutoDownload.LookbackDays,
	}

	// Create daemon config
	daemonCfg := &daemon.Config{
		PollInterval:  time.Duration(apiCfg.AutoDownload.ScanIntervalMinutes) * time.Minute,
		DownloadDir:   apiCfg.AutoDownload.DefaultDownloadFolder,
		UseJobNameDir: true,
		MaxConcurrent: 5,
		StateFile:     profile.StateFilePath,
		Eligibility:   eligibility,
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
		config:  apiCfg,
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
func (m *MultiUserDaemon) configChanged(entry *userDaemonEntry, profile UserProfile) bool {
	// Reload config from disk
	newCfg, err := config.LoadAPIConfig(profile.ConfigPath)
	if err != nil {
		// Error loading - assume changed to trigger restart
		return true
	}

	// Compare key fields
	if entry.config == nil {
		return true
	}

	oldCfg := entry.config
	if oldCfg.PlatformURL != newCfg.PlatformURL {
		return true
	}
	if oldCfg.APIKey != newCfg.APIKey {
		return true
	}
	if oldCfg.AutoDownload.Enabled != newCfg.AutoDownload.Enabled {
		return true
	}
	if oldCfg.AutoDownload.DefaultDownloadFolder != newCfg.AutoDownload.DefaultDownloadFolder {
		return true
	}
	if oldCfg.AutoDownload.ScanIntervalMinutes != newCfg.AutoDownload.ScanIntervalMinutes {
		return true
	}
	if oldCfg.AutoDownload.CorrectnessTag != newCfg.AutoDownload.CorrectnessTag {
		return true
	}
	if oldCfg.AutoDownload.LookbackDays != newCfg.AutoDownload.LookbackDays {
		return true
	}

	return false
}

// GetStatus returns the current status of all user daemons.
func (m *MultiUserDaemon) GetStatus() []UserDaemonStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var statuses []UserDaemonStatus
	for _, entry := range m.daemons {
		statuses = append(statuses, UserDaemonStatus{
			Username:       entry.profile.Username,
			ProfilePath:    entry.profile.ProfilePath,
			DownloadFolder: entry.config.AutoDownload.DefaultDownloadFolder,
			Running:        entry.running,
			Enabled:        entry.config.AutoDownload.Enabled,
		})
	}

	return statuses
}

// UserDaemonStatus represents the status of a single user's daemon.
type UserDaemonStatus struct {
	Username       string
	ProfilePath    string
	DownloadFolder string
	Running        bool
	Enabled        bool
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
