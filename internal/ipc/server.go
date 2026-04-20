//go:build windows

// Package ipc provides inter-process communication between the Windows service
// and the GUI/tray application using named pipes.
package ipc

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"reflect"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"github.com/rescale/rescale-int/internal/logging"
	"golang.org/x/sys/windows"
)

const maxIPCMessageSize = 1 << 20 // 1MB - bounds IPC message reads to prevent OOM

var (
	modkernel32                   = windows.NewLazySystemDLL("kernel32.dll")
	procGetNamedPipeClientProcessId = modkernel32.NewProc("GetNamedPipeClientProcessId")
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

	// Shutdown gracefully stops the daemon.
	Shutdown() error

	// GetRecentLogs returns recent log entries from the daemon.
	// In service mode, userID routes to the correct per-user daemon.
	// In subprocess mode, userID is ignored (only one user).
	GetRecentLogs(userID string, count int) []LogEntryData

	// ReloadConfig requests daemon config reload.
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

// Server handles IPC requests from clients via named pipe.
type Server struct {
	handler  ServiceHandler
	logger   *logging.Logger
	listener net.Listener

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// ownerSID is the SID of the user who started the daemon.
	// Used for per-user authorization to prevent cross-user daemon control.
	ownerSID string

	// serviceMode indicates multi-user Windows Service mode.
	// In service mode, owner-based auth is relaxed because user-scoped
	// routing handles isolation (each user can only affect their own daemon).
	serviceMode bool
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
	if sid, err := getCurrentUserSID(); err == nil {
		s.ownerSID = sid
		logger.Debug().Str("owner_sid", sid).Msg("IPC server owner SID captured")
	} else {
		logger.Warn().Err(err).Msg("Failed to get owner SID; cross-user authorization disabled")
	}

	return s
}

// NewServiceModeServer creates a new IPC server for multi-user Windows Service mode.
// In service mode, authorization is relaxed because user-scoped routing
// handles isolation. Any authenticated user is allowed to connect and control
// their own daemon via the handler's user-scoped operations.
func NewServiceModeServer(handler ServiceHandler, logger *logging.Logger) *Server {
	s := NewServer(handler, logger)
	s.serviceMode = true
	logger.Info().Msg("IPC server configured for multi-user service mode")
	return s
}

// getCurrentUserSID returns the SID of the current process owner.
func getCurrentUserSID() (string, error) {
	token, err := windows.OpenCurrentProcessToken()
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

// Start begins listening for IPC connections.
func (s *Server) Start() error {
	if IsPipeInUse() {
		return fmt.Errorf("failed to create named pipe: pipe already exists (another daemon is running). Stop the existing daemon first")
	}

	// Create named pipe listener with security descriptor
	//
	// SECURITY NOTE:
	// The descriptor "D:P(A;;GA;;;AU)" allows any authenticated user to connect.
	// However, modify operations (Pause, Resume, TriggerScan, Shutdown) are now
	// protected by per-user authorization in authorizeModifyRequest().
	//
	// Security model:
	// - Any authenticated user can connect and perform read-only operations
	//   (GetStatus, GetUserList, GetRecentLogs, OpenLogs)
	// - Only the daemon owner (matched by SID) can perform modify operations
	// - This prevents User A from controlling User B's daemon
	//
	// Alternative descriptors (for reference):
	// - "D:P(A;;GA;;;BA)(A;;GA;;;SY)" - Allow only Administrators and SYSTEM
	// - "D:P(A;;GA;;;BA)(A;;GA;;;AU)" - Allow Admins full control, Authenticated Users connect
	cfg := &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;AU)", // DACL: Allow Generic All for Authenticated Users
		MessageMode:        true,
		InputBufferSize:    4096,
		OutputBufferSize:   4096,
	}

	listener, err := winio.ListenPipe(PipeName, cfg)
	if err != nil {
		if strings.Contains(err.Error(), "Access is denied") {
			return fmt.Errorf("failed to create named pipe: another daemon is running or pipe is stale. Error: %w", err)
		}
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

	callerSID := ""
	if pid, err := getNamedPipeClientPID(conn); err == nil && pid > 0 {
		s.logger.Debug().Uint32("caller_pid", pid).Msg("IPC client PID extracted")
		if sid, err := getProcessOwnerSID(pid); err == nil {
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

// getNamedPipeClientPID extracts the client process ID from a named pipe connection.
// Uses the Windows GetNamedPipeClientProcessId API via reflection to access the underlying handle.
// Walks embedded structs recursively (win32Pipe -> win32File -> handle) because go-winio
// nests the handle several layers deep.
func getNamedPipeClientPID(conn net.Conn) (uint32, error) {
	v := reflect.ValueOf(conn)
	handle, found := findHandleRecursive(v, 0)
	if !found {
		return 0, fmt.Errorf("could not extract handle from connection type %T", conn)
	}

	var clientPID uint32
	r1, _, err := procGetNamedPipeClientProcessId.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&clientPID)),
	)
	if r1 == 0 {
		return 0, fmt.Errorf("GetNamedPipeClientProcessId failed: %w", err)
	}
	return clientPID, nil
}

// findHandleRecursive searches for a 'handle' field through embedded structs.
// Uses unsafe to read unexported fields (go-winio's handle is unexported).
func findHandleRecursive(v reflect.Value, depth int) (windows.Handle, bool) {
	if depth > 5 { // Prevent infinite recursion
		return 0, false
	}

	// Dereference pointers and interfaces
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return 0, false
		}
		v = v.Elem()
	}

	if v.Kind() != reflect.Struct {
		return 0, false
	}

	// Look for 'handle' field directly (may be unexported)
	if handleField := v.FieldByName("handle"); handleField.IsValid() {
		kind := handleField.Kind()
		if kind == reflect.Uintptr || kind == reflect.Uint || kind == reflect.Uint64 {
			// Use unsafe access for unexported fields
			if handleField.CanAddr() {
				ptr := unsafe.Pointer(handleField.UnsafeAddr())
				val := reflect.NewAt(handleField.Type(), ptr).Elem()
				return windows.Handle(val.Uint()), true
			}
			// Fallback for exported fields
			if handleField.CanUint() {
				return windows.Handle(handleField.Uint()), true
			}
		}
	}

	// Recursively search all fields (including embedded)
	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		if handle, found := findHandleRecursive(field, depth+1); found {
			return handle, true
		}
	}

	return 0, false
}

// getProcessOwnerSID returns the SID of the owner of a process by PID.
func getProcessOwnerSID(pid uint32) (string, error) {
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

// resolveUserScope centralizes the spec §11.3 IPC authorization policy
// so that every user-scoped handler inherits the same behavior:
//
//   - In service mode the returned userID is ALWAYS callerSID (req.UserID
//     is ignored — a client cannot ask the service to act on another
//     user's daemon).
//   - In service mode, an empty callerSID fails-closed with an audit-log
//     line and an error response. Silent scoping to userID="" violates
//     §11.3 ("modify requests from an unidentifiable caller are rejected")
//     and makes SID-based filtering of read responses meaningless.
//   - When mustAuthorizeModify is true, authorizeModifyRequest gates the
//     request regardless of mode (subprocess mode enforces owner match).
//
// In subprocess mode the returned userID is simply req.UserID — the
// handler decides how to interpret an empty value. A handler that has a
// sensible subprocess-mode default passes subprocessFallback; others
// treat empty as an error after the helper returns.
//
// The policy is enforced over the full user-scoped message catalog by
// TestServiceMode_UserScopedMessages_FailClosedWithoutCallerSID in
// server_security_test.go — new handler types must be registered there
// or the test will fail.
func (s *Server) resolveUserScope(
	operation string,
	callerSID string,
	reqUserID string,
	subprocessFallback string,
	mustAuthorizeModify bool,
) (userID string, errResp *Response) {
	if s.serviceMode {
		if callerSID == "" {
			s.logger.Info().
				Str("operation", operation).
				Msg("IPC request denied: could not identify caller")
			return "", NewErrorResponse("unauthorized: could not identify caller")
		}
		userID = callerSID
	} else {
		userID = reqUserID
		if userID == "" {
			userID = subprocessFallback
		}
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
		// In service mode, filter to caller's own entry only
		if s.serviceMode {
			if callerSID == "" {
				return NewErrorResponse("unauthorized: could not identify caller")
			}
			allUsers := s.handler.GetUserList()
			var filtered []UserStatus
			for _, u := range allUsers {
				if u.SID == callerSID {
					filtered = append(filtered, u)
				}
			}
			return NewUserListResponse(filtered)
		}
		users := s.handler.GetUserList()
		return NewUserListResponse(users)

	case MsgPauseUser:
		userID, errResp := s.resolveUserScope("PauseUser", callerSID, req.UserID, "", true)
		if errResp != nil {
			return errResp
		}
		if userID == "" {
			return NewErrorResponse("user_id required for PauseUser")
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
		if userID == "" {
			return NewErrorResponse("user_id required for ResumeUser")
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

	case MsgOpenLogs:
		// OpenLogs has a service-scope bypass: a client may pass userID="service"
		// to open the service's own logs in service mode. Any other value goes
		// through the standard user-scope helper.
		if s.serviceMode && req.UserID == "service" {
			if err := s.handler.OpenLogs("service"); err != nil {
				return NewErrorResponse(err.Error())
			}
			return NewOKResponse()
		}
		userID, errResp := s.resolveUserScope("OpenLogs", callerSID, req.UserID, "service", false)
		if errResp != nil {
			return errResp
		}
		if err := s.handler.OpenLogs(userID); err != nil {
			return NewErrorResponse(err.Error())
		}
		return NewOKResponse()

	case MsgOpenGUI:
		// GUI opening is handled by the tray app, not the service
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
// In service mode, authorization is relaxed — any authenticated user is allowed
// because user-scoped routing in the handler ensures isolation.
func (s *Server) authorizeModifyRequest(callerSID, operation string) error {
	// In service mode, any authenticated user is allowed
	// User-scoped routing in the handler handles isolation
	if s.serviceMode {
		if callerSID == "" {
			// Use INFO level for visibility in Activity tab
			s.logger.Info().
				Str("operation", operation).
				Msg("IPC request denied: could not identify caller")
			return fmt.Errorf("unauthorized: could not identify caller")
		}
		// Allow - routing will scope to caller's daemon
		return nil
	}

	// Subprocess mode: owner-based authorization
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

// GetSocketPath returns the named pipe path (for API compatibility with Unix).
// On Windows, this returns the named pipe path rather than a socket path.
func (s *Server) GetSocketPath() string {
	return PipeName
}
