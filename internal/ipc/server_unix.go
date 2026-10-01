//go:build !windows

package ipc

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/logging"
)

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
	if s.socketPath == "" {
		return errNoHome
	}
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

// handleConnection processes a single client connection.
func (s *Server) handleConnection(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()

	// Set read/write deadlines
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	// Read request (newline-delimited JSON) with bounded buffer to prevent OOM
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, maxIPCMessageSize), maxIPCMessageSize)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			if err == bufio.ErrTooLong {
				s.sendResponse(conn, NewErrorResponse("IPC message exceeds maximum size"))
				return
			}
			s.logger.Debug().Err(err).Msg("IPC read error")
		}
		return
	}
	data := scanner.Bytes()

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

	case MsgGetRecentLogs:
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

	case MsgReloadConfig:
		userID := req.UserID
		result := s.handler.ReloadConfig(userID)
		return NewReloadConfigResponse(result)

	case MsgGetTransferStatus:
		data, err := s.handler.GetTransferStatus(req.UserID)
		if err != nil {
			return NewErrorResponse(err.Error())
		}
		return NewDaemonTransferSnapshotResponse(data)

	case MsgCancelDaemonBatch:
		if err := s.handler.CancelDaemonBatch(req.UserID, req.BatchID); err != nil {
			return NewErrorResponse(err.Error())
		}
		return NewOKResponse()

	case MsgCancelDaemonTransfer:
		if err := s.handler.CancelDaemonTransfer(req.UserID, req.TaskID); err != nil {
			return NewErrorResponse(err.Error())
		}
		return NewOKResponse()

	case MsgRetryFailedInDaemonBatch:
		if err := s.handler.RetryFailedInDaemonBatch(req.UserID, req.BatchID); err != nil {
			return NewErrorResponse(err.Error())
		}
		return NewOKResponse()

	default:
		return NewErrorResponse(fmt.Sprintf("unknown message type: %s", req.Type))
	}
}
