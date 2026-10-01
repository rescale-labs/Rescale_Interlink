// Package daemon provides background service functionality for auto-downloading completed jobs.
package daemon

import (
	"fmt"
	"os/user"
	"time"

	"github.com/rescale/rescale-int/internal/ipc"
	"github.com/rescale/rescale-int/internal/version"
)

// IPCHandler implements ipc.ServiceHandler for the daemon, which serves one
// user, so it ignores every userID.
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
	lastPoll := h.daemon.GetLastPollTime()
	var lastPollPtr *time.Time
	if !lastPoll.IsZero() {
		lastPollPtr = &lastPoll
	}

	uptime := time.Since(h.startTime).Round(time.Second).String()

	status := &ipc.StatusData{
		Version:         version.Version,
		LastScanTime:    lastPollPtr,
		ActiveDownloads: h.daemon.GetActiveDownloads(),
		Uptime:          uptime,
	}

	// A daemon whose scans keep failing is running, not healthy. Report the
	// failure so "alive but broken" is visible instead of showing a last-scan
	// time that has silently stopped advancing.
	if scanErr, at := h.daemon.LastScanError(); scanErr != "" {
		status.LastErrorCode = ipc.CodeScanFailed
		status.LastError = ipc.CanonicalText[ipc.CodeScanFailed] + ": " + scanErr
		if !at.IsZero() {
			status.LastErrorTime = &at
		}
	}

	return status
}

// GetUserList returns the daemon's one user entry, with the user's SID on
// Windows, which the app and the tray match their own against.
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

	sid, _ := ipc.CurrentUserSID()
	folders, outsideLookback, unchecked := h.daemon.state.GetLeftOut()
	return []ipc.UserStatus{
		{
			Username:                username,
			SID:                     sid,
			State:                   state,
			DownloadFolder:          h.daemon.cfg.DownloadDir,
			LastScanTime:            lastPollPtr,
			JobsDownloaded:          h.daemon.GetDownloadedCount(),
			JobsHeldElsewhere:       h.daemon.state.GetHeldElsewhere(),
			WorkspaceFoldersSkipped: folders,
			JobsOutsideLookback:     outsideLookback,
			JobsUnchecked:           unchecked,
		},
	}
}

// PauseUser pauses auto-download.
func (h *IPCHandler) PauseUser(userID string) error {
	h.daemon.SetPaused(true)
	return nil
}

// ResumeUser resumes auto-download.
func (h *IPCHandler) ResumeUser(userID string) error {
	h.daemon.SetPaused(false)
	return nil
}

// TriggerScan triggers an immediate job scan. Returns an error when no scan
// was started (paused, stopped, or a poll already running) so the caller is
// not told a scan happened when it did not.
func (h *IPCHandler) TriggerScan(userID string) error {
	if h.daemon.IsPaused() {
		h.daemon.logger.Warn().Msg("Scan requested but daemon is paused")
		return fmt.Errorf("daemon is paused")
	}

	if err := h.daemon.TriggerPoll(); err != nil {
		h.daemon.logger.Warn().Err(err).Msg("Scan requested but not started")
		return err
	}
	h.daemon.logger.Info().Msg("Scan triggered via IPC")
	return nil
}

// GetRecentLogs returns recent log entries from the buffer.
func (h *IPCHandler) GetRecentLogs(userID string, count int) []ipc.LogEntryData {
	if h.logBuffer == nil {
		return nil
	}
	if count <= 0 {
		count = 100 // Default to 100 entries
	}
	return h.logBuffer.GetRecent(count)
}

// Shutdown gracefully stops the daemon.
func (h *IPCHandler) Shutdown() error {
	h.daemon.logger.Info().Msg("Shutdown requested via IPC")
	if h.shutdownFunc != nil {
		go h.shutdownFunc()
	}
	return nil
}

// ReloadConfig returns the active download count so the GUI can decide
// whether to restart the daemon now or defer.
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

// GetTransferStatus returns a snapshot of the daemon's transfer queue
// filtered to SourceLabel=Daemon.
func (h *IPCHandler) GetTransferStatus(userID string) (*ipc.DaemonTransferSnapshot, error) {
	return h.daemon.DaemonTransferSnapshot(), nil
}

// CancelDaemonBatch cancels all non-terminal tasks in a specific daemon
// batch.
func (h *IPCHandler) CancelDaemonBatch(userID, batchID string) error {
	return h.daemon.TransferService().CancelBatch(batchID)
}

// CancelDaemonTransfer cancels a single in-flight daemon task.
func (h *IPCHandler) CancelDaemonTransfer(userID, taskID string) error {
	return h.daemon.TransferService().CancelTransfer(taskID)
}

// RetryFailedInDaemonBatch retries all failed tasks in a daemon batch.
func (h *IPCHandler) RetryFailedInDaemonBatch(userID, batchID string) error {
	return h.daemon.TransferService().RetryFailedInBatch(batchID)
}
