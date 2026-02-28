//go:build windows

// Package daemon provides background service functionality for auto-downloading completed jobs.
package daemon

import (
	"os/user"
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/ipc"
	"github.com/rescale/rescale-int/internal/version"
)

// IPCHandler implements ipc.ServiceHandler for the Windows daemon subprocess.
// v4.4.0: Fully implemented (was previously a stub).
// This provides the bridge between IPC requests and daemon operations,
// matching the Unix implementation for feature parity.
type IPCHandler struct {
	daemon    *Daemon
	startTime time.Time

	// Pause/resume state
	mu     sync.RWMutex
	paused bool

	// Shutdown callback
	shutdownFunc func()

	// v4.4.0: Log buffer for IPC streaming
	logBuffer *LogBuffer
}

// NewIPCHandler creates a new IPC handler for the daemon.
// v4.4.0: Now stores daemon reference for full functionality.
func NewIPCHandler(daemon *Daemon, shutdownFunc func()) *IPCHandler {
	return &IPCHandler{
		daemon:       daemon,
		startTime:    time.Now(),
		shutdownFunc: shutdownFunc,
	}
}

// SetLogBuffer sets the log buffer for IPC log streaming.
// v4.4.0: Now functional (was previously a no-op).
func (h *IPCHandler) SetLogBuffer(buf *LogBuffer) {
	h.logBuffer = buf
}

// GetStatus returns the current daemon status.
// v4.4.0: Now returns real data (was previously hardcoded stub).
func (h *IPCHandler) GetStatus() *ipc.StatusData {
	h.mu.RLock()
	state := "running"
	if h.paused {
		state = "paused"
	}
	h.mu.RUnlock()

	var lastPollPtr *time.Time
	if h.daemon != nil {
		lastPoll := h.daemon.GetLastPollTime()
		if !lastPoll.IsZero() {
			lastPollPtr = &lastPoll
		}
	}

	uptime := time.Since(h.startTime).Round(time.Second).String()

	activeDownloads := 0
	if h.daemon != nil {
		activeDownloads = h.daemon.GetActiveDownloads()
	}

	return &ipc.StatusData{
		ServiceState:    state,
		Version:         version.Version,
		LastScanTime:    lastPollPtr,
		ActiveDownloads: activeDownloads,
		ActiveUsers:     1, // Single-user subprocess mode
		Uptime:          uptime,
		ServiceMode:     false, // v4.5.2: Running as subprocess (single-user mode)
	}
}

// GetUserList returns the list of user daemon statuses.
// On Windows subprocess mode, returns a single user (the current user).
// v4.4.0: Now returns real data (was previously nil).
func (h *IPCHandler) GetUserList() []ipc.UserStatus {
	h.mu.RLock()
	state := "running"
	if h.paused {
		state = "paused"
	}
	h.mu.RUnlock()

	// Get current user
	username := "unknown"
	if u, err := user.Current(); err == nil {
		username = u.Username
	}

	var lastPollPtr *time.Time
	downloadFolder := ""
	jobsDownloaded := 0

	if h.daemon != nil {
		lastPoll := h.daemon.GetLastPollTime()
		if !lastPoll.IsZero() {
			lastPollPtr = &lastPoll
		}
		if h.daemon.cfg != nil {
			downloadFolder = h.daemon.cfg.DownloadDir
		}
		jobsDownloaded = h.daemon.GetDownloadedCount()
	}

	// Get current user's SID for Windows IPC matching
	// (Matches service mode which sets SID from UserProfile — see service/ipc_handler.go)
	sid := ""
	if currentUser, err := user.Current(); err == nil {
		sid = currentUser.Uid // On Windows, Uid is the SID string (e.g., "S-1-5-21-...")
	}

	return []ipc.UserStatus{
		{
			Username:       username,
			SID:            sid,
			State:          state,
			DownloadFolder: downloadFolder,
			LastScanTime:   lastPollPtr,
			JobsDownloaded: jobsDownloaded,
		},
	}
}

// PauseUser pauses auto-download.
// On Windows subprocess mode, userID is ignored.
// v4.4.0: Now functional (was previously a no-op).
func (h *IPCHandler) PauseUser(userID string) error {
	h.mu.Lock()
	h.paused = true
	h.mu.Unlock()
	if h.daemon != nil && h.daemon.logger != nil {
		h.daemon.logger.Info().Msg("Daemon paused via IPC")
	}
	return nil
}

// ResumeUser resumes auto-download.
// On Windows subprocess mode, userID is ignored.
// v4.4.0: Now functional (was previously a no-op).
func (h *IPCHandler) ResumeUser(userID string) error {
	h.mu.Lock()
	h.paused = false
	h.mu.Unlock()
	if h.daemon != nil && h.daemon.logger != nil {
		h.daemon.logger.Info().Msg("Daemon resumed via IPC")
	}
	return nil
}

// TriggerScan triggers an immediate job scan.
// v4.4.0: Now functional (was previously a no-op).
func (h *IPCHandler) TriggerScan(userID string) error {
	h.mu.RLock()
	paused := h.paused
	h.mu.RUnlock()

	if paused {
		if h.daemon != nil && h.daemon.logger != nil {
			h.daemon.logger.Warn().Msg("Scan requested but daemon is paused")
		}
		return nil
	}

	if h.daemon != nil {
		if h.daemon.logger != nil {
			h.daemon.logger.Info().Msg("Scan triggered via IPC")
		}
		h.daemon.TriggerPoll()
	}
	return nil
}

// OpenLogs opens the log viewer.
// On Windows subprocess mode, this is a no-op (logs go to log file).
func (h *IPCHandler) OpenLogs(userID string) error {
	if h.daemon != nil && h.daemon.logger != nil {
		h.daemon.logger.Debug().Msg("OpenLogs called (no-op in subprocess mode)")
	}
	return nil
}

// GetRecentLogs returns recent log entries from the buffer.
// v4.5.0: Added userID parameter for interface compatibility.
// In subprocess mode, userID is ignored (only one user).
func (h *IPCHandler) GetRecentLogs(userID string, count int) []ipc.LogEntryData {
	// userID ignored in subprocess mode - only one user
	if h.logBuffer == nil {
		return nil
	}
	if count <= 0 {
		count = 100 // Default to 100 entries
	}
	return h.logBuffer.GetRecent(count)
}

// GetLogBuffer returns the log buffer for subscription.
// v4.4.0: Now functional (was previously returning nil).
func (h *IPCHandler) GetLogBuffer() *LogBuffer {
	return h.logBuffer
}

// Shutdown gracefully stops the daemon.
// v4.4.0: Now functional (was previously a no-op).
func (h *IPCHandler) Shutdown() error {
	if h.daemon != nil && h.daemon.logger != nil {
		h.daemon.logger.Info().Msg("Shutdown requested via IPC")
	}
	if h.shutdownFunc != nil {
		go h.shutdownFunc()
	}
	return nil
}

// ReloadConfig handles config reload for subprocess mode.
// v4.7.6: Returns active download count so GUI can decide whether to restart now or defer.
// The actual restart is managed by the GUI (stop + start) — simpler and avoids in-process mutation.
func (h *IPCHandler) ReloadConfig(userID string) *ipc.ReloadConfigData {
	activeDownloads := 0
	if h.daemon != nil {
		activeDownloads = h.daemon.GetActiveDownloads()
	}

	if activeDownloads > 0 {
		if h.daemon != nil && h.daemon.logger != nil {
			h.daemon.logger.Info().Int("active_downloads", activeDownloads).
				Msg("Config reload requested but downloads active — deferring")
		}
		return &ipc.ReloadConfigData{
			Deferred:        true,
			ActiveDownloads: activeDownloads,
		}
	}

	if h.daemon != nil && h.daemon.logger != nil {
		h.daemon.logger.Info().Msg("Config reload requested — ready for restart")
	}
	return &ipc.ReloadConfigData{
		Applied: true,
	}
}

// IsPaused returns whether the daemon is currently paused.
// v4.4.0: Now functional (was previously always returning false).
func (h *IPCHandler) IsPaused() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.paused
}

// ShouldPoll returns whether the daemon should perform polling.
// Returns false if paused.
// v4.4.0: Now functional (was previously always returning true).
func (h *IPCHandler) ShouldPoll() bool {
	return !h.IsPaused()
}
