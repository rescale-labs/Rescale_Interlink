package ipc

import "net"

const maxIPCMessageSize = 1 << 20 // 1MB - bounds IPC message reads to prevent OOM

// ServiceHandler defines the interface for daemon operations.
// The daemon implements this to handle IPC requests. It serves one user, so
// every userID is ignored.
type ServiceHandler interface {
	// GetStatus returns the current daemon status.
	GetStatus() *StatusData

	// GetUserList returns the daemon's one user entry.
	GetUserList() []UserStatus

	// PauseUser pauses auto-download.
	PauseUser(userID string) error

	// ResumeUser resumes auto-download.
	ResumeUser(userID string) error

	// TriggerScan triggers an immediate job scan.
	TriggerScan(userID string) error

	// Shutdown gracefully stops the daemon.
	Shutdown() error

	// GetRecentLogs returns recent log entries from the daemon.
	GetRecentLogs(userID string, count int) []LogEntryData

	// ReloadConfig requests daemon config reload. It returns the active
	// download count for the GUI to decide when to restart the daemon.
	ReloadConfig(userID string) *ReloadConfigData

	// GetTransferStatus returns a snapshot of the daemon's transfer queue
	// filtered to SourceLabel=Daemon.
	GetTransferStatus(userID string) (*DaemonTransferSnapshot, error)

	// CancelDaemonBatch cancels non-terminal tasks in a daemon batch.
	CancelDaemonBatch(userID, batchID string) error

	// CancelDaemonTransfer cancels one daemon task.
	CancelDaemonTransfer(userID, taskID string) error

	// RetryFailedInDaemonBatch retries failed tasks in a daemon batch.
	RetryFailedInDaemonBatch(userID, batchID string) error
}

// acceptLoop accepts incoming connections.
func (s *Server) acceptLoop() {
	defer s.wg.Done()

	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		// Accept with timeout to allow checking context
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.ctx.Done():
				return
			default:
				s.logger.Warn().Err(err).Msg("Failed to accept IPC connection")
				continue
			}
		}

		// Handle connection in goroutine
		s.wg.Add(1)
		go s.handleConnection(conn)
	}
}

// sendResponse sends a response to the client.
func (s *Server) sendResponse(conn net.Conn, resp *Response) {
	data, err := resp.Encode()
	if err != nil {
		s.logger.Error().Err(err).Msg("Failed to encode IPC response")
		return
	}

	// Append newline delimiter
	data = append(data, '\n')

	_, err = conn.Write(data)
	if err != nil {
		s.logger.Warn().Err(err).Msg("Failed to send IPC response")
	}
}
