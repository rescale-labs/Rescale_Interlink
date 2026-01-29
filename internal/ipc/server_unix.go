//go:build !windows

// Package ipc provides inter-process communication between the daemon
// and the GUI application using Unix domain sockets.
package ipc

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/logging"
)

// ServiceHandler defines the interface for daemon operations.
// The daemon implements this to handle IPC requests.
type ServiceHandler interface {
	// GetStatus returns the current daemon status.
	GetStatus() *StatusData

	// GetUserList returns the list of user daemon statuses.
	// On Unix single-user mode, returns a single user.
	GetUserList() []UserStatus

	// PauseUser pauses auto-download.
	PauseUser(userID string) error

	// ResumeUser resumes auto-download.
	ResumeUser(userID string) error

	// TriggerScan triggers an immediate job scan.
	TriggerScan(userID string) error

	// OpenLogs opens the log viewer.
	OpenLogs(userID string) error

	// Shutdown gracefully stops the daemon.
	// This is supported on Unix (unlike Windows where SCM handles it).
	Shutdown() error

	// GetRecentLogs returns recent log entries from the daemon.
	// v4.5.0: Added userID parameter for per-user routing in service mode.
	// In subprocess mode, userID is ignored (only one user).
	GetRecentLogs(userID string, count int) []LogEntryData
}

// Server handles IPC requests from clients via Unix domain socket.
type Server struct {
	handler    ServiceHandler
	logger     *logging.Logger
	listener   net.Listener
	socketPath string

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewServer creates a new IPC server.
func NewServer(handler ServiceHandler, logger *logging.Logger) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		handler:    handler,
		logger:     logger,
		socketPath: GetSocketPath(),
		ctx:        ctx,
		cancel:     cancel,
	}
}

// NewServerWithPath creates a new IPC server with a custom socket path.
func NewServerWithPath(handler ServiceHandler, logger *logging.Logger, socketPath string) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		handler:    handler,
		logger:     logger,
		socketPath: socketPath,
		ctx:        ctx,
		cancel:     cancel,
	}
}

// Start begins listening for IPC connections.
func (s *Server) Start() error {
	// Ensure socket directory exists
	socketDir := filepath.Dir(s.socketPath)
	if err := os.MkdirAll(socketDir, 0700); err != nil {
		return fmt.Errorf("failed to create socket directory: %w", err)
	}

	// Remove any stale socket file
	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove stale socket: %w", err)
	}

	// Create Unix socket listener
	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("failed to create Unix socket: %w", err)
	}
	s.listener = listener

	// Set socket permissions (user only)
	if err := os.Chmod(s.socketPath, 0600); err != nil {
		s.listener.Close()
		return fmt.Errorf("failed to set socket permissions: %w", err)
	}

	s.logger.Info().Str("socket", s.socketPath).Msg("IPC server started")

	// Start accepting connections
	s.wg.Add(1)
	go s.acceptLoop()

	return nil
}

// Stop gracefully shuts down the IPC server.
func (s *Server) Stop() {
	s.logger.Debug().Msg("Stopping IPC server")
	s.cancel()

	if s.listener != nil {
		s.listener.Close()
	}

	s.wg.Wait()

	// Clean up socket file
	os.Remove(s.socketPath)

	s.logger.Info().Msg("IPC server stopped")
}

// GetSocketPath returns the socket path being used.
func (s *Server) GetSocketPath() string {
	return s.socketPath
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

// handleConnection processes a single client connection.
func (s *Server) handleConnection(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()

	// Set read/write deadlines
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	reader := bufio.NewReader(conn)

	// Read request (newline-delimited JSON)
	data, err := reader.ReadBytes('\n')
	if err != nil {
		if err != io.EOF {
			s.logger.Warn().Err(err).Msg("Failed to read IPC request")
		}
		return
	}

	// Decode request
	req, err := DecodeRequest(data)
	if err != nil {
		s.logger.Warn().Err(err).Msg("Failed to decode IPC request")
		s.sendResponse(conn, NewErrorResponse("invalid request format"))
		return
	}

	s.logger.Debug().
		Str("type", string(req.Type)).
		Str("user_id", req.UserID).
		Msg("Received IPC request")

	// Handle request
	resp := s.handleRequest(req)

	// Send response
	s.sendResponse(conn, resp)
}

// handleRequest processes a request and returns a response.
func (s *Server) handleRequest(req *Request) *Response {
	switch req.Type {
	case MsgGetStatus:
		status := s.handler.GetStatus()
		return NewStatusResponse(status)

	case MsgGetUserList:
		users := s.handler.GetUserList()
		return NewUserListResponse(users)

	case MsgPauseUser:
		if err := s.handler.PauseUser(req.UserID); err != nil {
			return NewErrorResponse(err.Error())
		}
		return NewOKResponse()

	case MsgResumeUser:
		if err := s.handler.ResumeUser(req.UserID); err != nil {
			return NewErrorResponse(err.Error())
		}
		return NewOKResponse()

	case MsgTriggerScan:
		userID := req.UserID
		if userID == "" {
			userID = "all"
		}
		if err := s.handler.TriggerScan(userID); err != nil {
			return NewErrorResponse(err.Error())
		}
		return NewOKResponse()

	case MsgOpenLogs:
		userID := req.UserID
		if userID == "" {
			userID = "service"
		}
		if err := s.handler.OpenLogs(userID); err != nil {
			return NewErrorResponse(err.Error())
		}
		return NewOKResponse()

	case MsgOpenGUI:
		// GUI opening is handled by the caller, not the daemon
		return NewOKResponse()

	case MsgGetRecentLogs:
		// v4.5.0: Return recent log entries for GUI display
		// In subprocess mode, userID is ignored; in service mode, routes to calling user
		logs := s.handler.GetRecentLogs(req.UserID, 100) // Default to 100 entries
		return NewRecentLogsResponse(logs)

	case MsgShutdown:
		// On Unix, shutdown via IPC is supported
		if err := s.handler.Shutdown(); err != nil {
			return NewErrorResponse(err.Error())
		}
		// Send OK before shutting down
		resp := NewOKResponse()
		// Schedule shutdown after response is sent
		go func() {
			time.Sleep(100 * time.Millisecond)
			s.Stop()
		}()
		return resp

	default:
		return NewErrorResponse(fmt.Sprintf("unknown message type: %s", req.Type))
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
