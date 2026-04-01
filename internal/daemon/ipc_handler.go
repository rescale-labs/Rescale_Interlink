//go:build !windows

// Package daemon provides background service functionality for auto-downloading completed jobs.
package daemon

import (
	"os/user"
	"time"

	"github.com/rescale/rescale-int/internal/ipc"
	"github.com/rescale/rescale-int/internal/version"
)

// IPCHandler implements ipc.ServiceHandler for the Unix daemon.
// It provides the bridge between IPC requests and daemon operations.
type IPCHandler struct {
	daemon    *Daemon
	startTime time.Time

	// Shutdown callback
	shutdownFunc func()

	logBuffer *LogBuffer
}

// NewIPCHandler creates a new IPC handler for the daemon.
func NewIPCHandler(daemon *Daemon, shutdownFunc func()) *IPCHandler {
	return &IPCHandler{
		daemon:       daemon,
		startTime:    time.Now(),
		shutdownFunc: shutdownFunc,
	}
}

// SetLogBuffer sets the log buffer for IPC log streaming.
func (h *IPCHandler) SetLogBuffer(buf *LogBuffer) {
	h.logBuffer = buf
}

// GetStatus returns the current daemon status.
func (h *IPCHandler) GetStatus() *ipc.StatusData {
	state := "running"
	if h.daemon.IsPaused() {
		state = "paused"
	}

	lastPoll := h.daemon.GetLastPollTime()
	var lastPollPtr *time.Time
	if !lastPoll.IsZero() {
		lastPollPtr = &lastPoll
	}

	uptime := time.Since(h.startTime).Round(time.Second).String()

	return &ipc.StatusData{
		ServiceState:    state,
		Version:         version.Version,
		LastScanTime:    lastPollPtr,
		ActiveDownloads: h.daemon.GetActiveDownloads(),
		ActiveUsers:     1, // Single-user mode on Unix
		Uptime:          uptime,
		ServiceMode:     false,
	}
}

// GetUserList returns the list of user daemon statuses.
// On Unix single-user mode, returns a single user (the current user).
func (h *IPCHandler) GetUserList() []ipc.UserStatus {
	state := "running"
	if h.daemon.IsPaused() {
		state = "paused"
	}

	// Get current user
	username := "unknown"
	if u, err := user.Current(); err == nil {
		username = u.Username
	}

	lastPoll := h.daemon.GetLastPollTime()
	var lastPollPtr *time.Time
	if !lastPoll.IsZero() {
		lastPollPtr = &lastPoll
	}

	return []ipc.UserStatus{
		{
			Username:       username,
			State:          state,
			DownloadFolder: h.daemon.cfg.DownloadDir,
			LastScanTime:   lastPollPtr,
			JobsDownloaded: h.daemon.GetDownloadedCount(),
		},
	}
}

// PauseUser pauses auto-download.
// On Unix single-user mode, userID is ignored.
func (h *IPCHandler) PauseUser(userID string) error {
	h.daemon.SetPaused(true)
	return nil
}

// ResumeUser resumes auto-download.
// On Unix single-user mode, userID is ignored.
func (h *IPCHandler) ResumeUser(userID string) error {
	h.daemon.SetPaused(false)
	return nil
}

// TriggerScan triggers an immediate job scan.
func (h *IPCHandler) TriggerScan(userID string) error {
	if h.daemon.IsPaused() {
		h.daemon.logger.Warn().Msg("Scan requested but daemon is paused")
		return nil
	}

	h.daemon.logger.Info().Msg("Scan triggered via IPC")
	h.daemon.TriggerPoll()
	return nil
}

// OpenLogs opens the log viewer.
// On Unix, this is a no-op (logs go to stdout or log file).
func (h *IPCHandler) OpenLogs(userID string) error {
	h.daemon.logger.Debug().Msg("OpenLogs called (no-op on Unix)")
	return nil
}

// GetRecentLogs returns recent log entries from the buffer.
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
func (h *IPCHandler) GetLogBuffer() *LogBuffer {
	return h.logBuffer
}

// Shutdown gracefully stops the daemon.
func (h *IPCHandler) Shutdown() error {
	h.daemon.logger.Info().Msg("Shutdown requested via IPC")
	if h.shutdownFunc != nil {
		go h.shutdownFunc()
	}
	return nil
}

// ReloadConfig handles config reload for subprocess mode.
// Returns active download count so GUI can decide whether to restart now or defer.
// The actual restart is managed by the GUI (stop + start) -- simpler and avoids in-process mutation.
func (h *IPCHandler) ReloadConfig(userID string) *ipc.ReloadConfigData {
	activeDownloads := h.daemon.GetActiveDownloads()
	if activeDownloads > 0 {
		h.daemon.logger.Info().Int("active_downloads", activeDownloads).
			Msg("Config reload requested but downloads active — deferring")
		return &ipc.ReloadConfigData{
			Deferred:        true,
			ActiveDownloads: activeDownloads,
		}
	}

	h.daemon.logger.Info().Msg("Config reload requested — ready for restart")
	return &ipc.ReloadConfigData{
		Applied: true,
	}
}

// GetTransferStatus returns daemon transfer batch status.
// In subprocess mode, userID is ignored (single-user).
func (h *IPCHandler) GetTransferStatus(userID string) (*ipc.TransferStatusData, error) {
	return h.daemon.GetTransferStatus(), nil
}

// IsPaused returns whether the daemon is currently paused.
func (h *IPCHandler) IsPaused() bool {
	return h.daemon.IsPaused()
}

// ShouldPoll returns whether the daemon should perform polling.
// Returns false if paused.
func (h *IPCHandler) ShouldPoll() bool {
	return !h.IsPaused()
}
