//go:build windows

package ipc

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/logging"
	"golang.org/x/sys/windows"
)

// Server handles IPC requests from clients via named pipe.
type Server struct {
	handler  ServiceHandler
	logger   *logging.Logger
	listener net.Listener
	pipeName string

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// ownerSID is the SID of the user who started the daemon.
	// Used for per-user authorization to prevent cross-user daemon control.
	ownerSID string
}

// NewServer creates a new IPC server.
func NewServer(handler ServiceHandler, logger *logging.Logger) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		handler: handler,
		logger:  logger,
		ctx:     ctx,
		cancel:  cancel,
	}

	// Capture the owner's SID for authorization checks
	if sid, err := currentUserSID(); err == nil {
		s.ownerSID = sid
		logger.Debug().Str("owner_sid", sid).Msg("IPC server owner SID captured")
	} else {
		logger.Warn().Err(err).Msg("Failed to get owner SID; the IPC server will not start")
	}

	return s
}

// CurrentUserSID returns the SID of the user this process runs as.
func CurrentUserSID() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("failed to get token user: %w", err)
	}

	return user.User.Sid.String(), nil
}

// Start begins listening for IPC connections.
func (s *Server) Start() error {
	name, err := UserPipeName(pipeBase)
	if err != nil {
		return err
	}
	if pipeInUse(name) {
		return fmt.Errorf("failed to create named pipe: pipe already exists (another daemon is running). Stop the existing daemon first")
	}

	// Only the owner and LocalSystem can open the pipe, so another user can
	// neither read this daemon's status and logs nor send it requests;
	// authorizeModifyRequest still checks each modify request's caller.
	listener, err := ListenUserPipe(name, 4096)
	if err != nil {
		if strings.Contains(err.Error(), "Access is denied") {
			return fmt.Errorf("failed to create named pipe: another daemon is running or pipe is stale. Error: %w", err)
		}
		return fmt.Errorf("failed to create named pipe: %w", err)
	}
	s.listener, s.pipeName = listener, name

	s.logger.Info().Str("pipe", name).Msg("IPC server started")

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

// handleConnection processes a single client connection.
func (s *Server) handleConnection(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()

	// Set read/write deadlines
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	callerSID := ""
	if pid, err := getNamedPipeClientPID(conn); err == nil && pid > 0 {
		s.logger.Debug().Uint32("caller_pid", pid).Msg("IPC client PID extracted")
		if sid, err := ProcessOwnerSID(pid); err == nil {
			callerSID = sid
		} else {
			s.logger.Debug().Err(err).Uint32("pid", pid).Msg("Failed to get SID from PID")
		}
	} else if err != nil {
		s.logger.Debug().Err(err).Str("conn_type", fmt.Sprintf("%T", conn)).Msg("Failed to extract client PID from connection")
	}

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
		Str("caller_sid", callerSID).
		Msg("Received IPC request")

	// Handle request with caller SID for authorization
	resp := s.handleRequest(req, callerSID)

	// Send response
	s.sendResponse(conn, resp)
}

// getNamedPipeClientPID returns the PID of the client at the other end of conn.
func getNamedPipeClientPID(conn net.Conn) (uint32, error) {
	f, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return 0, fmt.Errorf("no handle on %T", conn)
	}
	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(windows.Handle(f.Fd()), &pid); err != nil {
		return 0, fmt.Errorf("GetNamedPipeClientProcessId failed: %w", err)
	}
	return pid, nil
}

// ProcessOwnerSID returns the SID of the user the process with this PID runs as.
func ProcessOwnerSID(pid uint32) (string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", fmt.Errorf("failed to open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(handle)

	var token windows.Token
	err = windows.OpenProcessToken(handle, windows.TOKEN_QUERY, &token)
	if err != nil {
		return "", fmt.Errorf("failed to open process token: %w", err)
	}
	defer token.Close()

	user, err := token.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("failed to get token user: %w", err)
	}

	return user.User.Sid.String(), nil
}

// resolveUserScope returns the user a request is for, req.UserID or else
// fallback, and when mustAuthorizeModify is true refuses a caller who is not
// the daemon's owner.
func (s *Server) resolveUserScope(
	operation string,
	callerSID string,
	reqUserID string,
	fallback string,
	mustAuthorizeModify bool,
) (userID string, errResp *Response) {
	userID = reqUserID
	if userID == "" {
		userID = fallback
	}
	if mustAuthorizeModify {
		if err := s.authorizeModifyRequest(callerSID, operation); err != nil {
			return "", NewErrorResponse(err.Error())
		}
	}
	return userID, nil
}

// handleRequest processes a request and returns a response.
func (s *Server) handleRequest(req *Request, callerSID string) *Response {
	switch req.Type {
	case MsgGetStatus:
		// Read-only: no authorization required
		status := s.handler.GetStatus()
		return NewStatusResponse(status)

	case MsgGetUserList:
		users := s.handler.GetUserList()
		return NewUserListResponse(users)

	case MsgPauseUser:
		userID, errResp := s.resolveUserScope("PauseUser", callerSID, req.UserID, "", true)
		if errResp != nil {
			return errResp
		}
		if err := s.handler.PauseUser(userID); err != nil {
			return NewErrorResponse(err.Error())
		}
		return NewOKResponse()

	case MsgResumeUser:
		userID, errResp := s.resolveUserScope("ResumeUser", callerSID, req.UserID, "", true)
		if errResp != nil {
			return errResp
		}
		if err := s.handler.ResumeUser(userID); err != nil {
			return NewErrorResponse(err.Error())
		}
		return NewOKResponse()

	case MsgTriggerScan:
		userID, errResp := s.resolveUserScope("TriggerScan", callerSID, req.UserID, "all", true)
		if errResp != nil {
			return errResp
		}
		if err := s.handler.TriggerScan(userID); err != nil {
			return NewErrorResponse(err.Error())
		}
		return NewOKResponse()

	case MsgShutdown:
		if err := s.authorizeModifyRequest(callerSID, "Shutdown"); err != nil {
			return NewErrorResponse(err.Error())
		}
		if err := s.handler.Shutdown(); err != nil {
			return NewErrorResponse(err.Error())
		}
		// Schedule server stop after response is sent to client
		go func() {
			time.Sleep(100 * time.Millisecond)
			s.Stop()
		}()
		return NewOKResponse()

	case MsgGetRecentLogs:
		userID, errResp := s.resolveUserScope("GetRecentLogs", callerSID, req.UserID, "", false)
		if errResp != nil {
			return errResp
		}
		logs := s.handler.GetRecentLogs(userID, 100) // Default to 100 entries
		return NewRecentLogsResponse(logs)

	case MsgReloadConfig:
		userID, errResp := s.resolveUserScope("ReloadConfig", callerSID, req.UserID, "", true)
		if errResp != nil {
			return errResp
		}
		result := s.handler.ReloadConfig(userID)
		return NewReloadConfigResponse(result)

	case MsgGetTransferStatus:
		userID, errResp := s.resolveUserScope("GetTransferStatus", callerSID, req.UserID, "", false)
		if errResp != nil {
			return errResp
		}
		data, err := s.handler.GetTransferStatus(userID)
		if err != nil {
			return NewErrorResponse(err.Error())
		}
		return NewDaemonTransferSnapshotResponse(data)

	case MsgCancelDaemonBatch:
		userID, errResp := s.resolveUserScope("CancelDaemonBatch", callerSID, req.UserID, "", true)
		if errResp != nil {
			return errResp
		}
		if err := s.handler.CancelDaemonBatch(userID, req.BatchID); err != nil {
			return NewErrorResponse(err.Error())
		}
		return NewOKResponse()

	case MsgCancelDaemonTransfer:
		userID, errResp := s.resolveUserScope("CancelDaemonTransfer", callerSID, req.UserID, "", true)
		if errResp != nil {
			return errResp
		}
		if err := s.handler.CancelDaemonTransfer(userID, req.TaskID); err != nil {
			return NewErrorResponse(err.Error())
		}
		return NewOKResponse()

	case MsgRetryFailedInDaemonBatch:
		userID, errResp := s.resolveUserScope("RetryFailedInDaemonBatch", callerSID, req.UserID, "", true)
		if errResp != nil {
			return errResp
		}
		if err := s.handler.RetryFailedInDaemonBatch(userID, req.BatchID); err != nil {
			return NewErrorResponse(err.Error())
		}
		return NewOKResponse()

	default:
		return NewErrorResponse(fmt.Sprintf("unknown message type: %s", req.Type))
	}
}

// authorizeModifyRequest checks if the caller is authorized to perform a modify operation.
// Only the daemon owner can execute commands that modify daemon state.
func (s *Server) authorizeModifyRequest(callerSID, operation string) error {
	// Fail-closed for security — if owner SID was not captured at startup,
	// deny modify operations rather than allowing all requests
	if s.ownerSID == "" {
		s.logger.Error().
			Str("operation", operation).
			Msg("Authorization unavailable: owner SID not captured at daemon startup")
		return fmt.Errorf("authorization unavailable: daemon startup failed to capture owner identity")
	}

	// If we couldn't get caller SID, deny access (fail-closed for security)
	if callerSID == "" {
		// Use INFO level for visibility in Activity tab
		s.logger.Info().
			Str("operation", operation).
			Msg("IPC request denied: could not identify caller")
		return fmt.Errorf("unauthorized: could not identify caller")
	}

	// Check if caller matches owner
	if callerSID != s.ownerSID {
		s.logger.Warn().
			Str("operation", operation).
			Str("caller_sid", callerSID).
			Str("owner_sid", s.ownerSID).
			Msg("IPC request denied: cross-user access attempt")
		return fmt.Errorf("unauthorized: only the daemon owner can perform this operation")
	}

	return nil
}

// GetSocketPath returns the named pipe path (for API compatibility with Unix).
// On Windows, this returns the named pipe path rather than a socket path.
func (s *Server) GetSocketPath() string {
	return s.pipeName
}
