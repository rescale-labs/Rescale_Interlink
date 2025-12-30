//go:build windows

// Package ipc provides inter-process communication between the Windows service
// and the GUI/tray application using named pipes.
package ipc

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/rescale/rescale-int/internal/logging"
)

// ServiceHandler defines the interface for service operations.
// The Windows service implements this to handle IPC requests.
type ServiceHandler interface {
	// GetStatus returns the current service status.
	GetStatus() *StatusData

	// GetUserList returns the list of user daemon statuses.
	GetUserList() []UserStatus

	// PauseUser pauses auto-download for a specific user.
	PauseUser(userID string) error

	// ResumeUser resumes auto-download for a specific user.
	ResumeUser(userID string) error

	// TriggerScan triggers an immediate job scan.
	// If userID is "all", scans for all users; otherwise scans for specific user.
	TriggerScan(userID string) error

	// OpenLogs opens the log viewer for a user or the service.
	// If userID is "service", opens service logs; otherwise opens user logs.
	OpenLogs(userID string) error
}

// Server handles IPC requests from clients via named pipe.
type Server struct {
	handler  ServiceHandler
	logger   *logging.Logger
	listener net.Listener

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewServer creates a new IPC server.
func NewServer(handler ServiceHandler, logger *logging.Logger) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		handler: handler,
		logger:  logger,
		ctx:     ctx,
		cancel:  cancel,
	}
}

// Start begins listening for IPC connections.
func (s *Server) Start() error {
	// Create named pipe listener with security descriptor
	// Allow authenticated users to connect
	cfg := &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;AU)", // DACL: Allow Generic All for Authenticated Users
		MessageMode:        true,
		InputBufferSize:    4096,
		OutputBufferSize:   4096,
	}

	listener, err := winio.ListenPipe(PipeName, cfg)
	if err != nil {
		return fmt.Errorf("failed to create named pipe: %w", err)
	}
	s.listener = listener

	s.logger.Info().Str("pipe", PipeName).Msg("IPC server started")

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
	s.logger.Info().Msg("IPC server stopped")
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
		if req.UserID == "" {
			return NewErrorResponse("user_id required for PauseUser")
		}
		if err := s.handler.PauseUser(req.UserID); err != nil {
			return NewErrorResponse(err.Error())
		}
		return NewOKResponse()

	case MsgResumeUser:
		if req.UserID == "" {
			return NewErrorResponse("user_id required for ResumeUser")
		}
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
		// GUI opening is handled by the tray app, not the service
		return NewOKResponse()

	case MsgShutdown:
		// Shutdown is handled by SCM, not IPC
		return NewErrorResponse("shutdown via IPC not supported; use service manager")

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
