//go:build windows

package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// currentUserSID is a variable so a test can stand in another SID, or none.
var currentUserSID = CurrentUserSID

// pipeOwnerSID returns the owner of the pipe conn is connected to. A variable
// so a test can stand in a pipe another user created.
var pipeOwnerSID = func(conn net.Conn) (string, error) {
	f, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return "", fmt.Errorf("no handle on %T", conn)
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return "", err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return "", err
	}
	return owner.String(), nil
}

var errNotOurPipe = errors.New("owned by another user, so not used")

func userSID() (string, error) {
	sid, err := currentUserSID()
	if err != nil {
		return "", fmt.Errorf("%w: %w", errNoSID, err)
	}
	return sid, nil
}

// UserPipeName names this user's pipe for base: the daemon's, where the app,
// the tray and the CLI find it, or the rate-limit coordinator's.
func UserPipeName(base string) (string, error) {
	sid, err := userSID()
	if err != nil {
		return "", err
	}
	return pipeNameFor(base, sid)
}

// ListenUserPipe listens on a pipe only this user and LocalSystem can open,
// owned by this user so that DialUserPipe can tell it from a pipe of the same
// name another user created first. It never joins such a pipe: go-winio
// creates the first instance with FILE_CREATE, as FILE_FLAG_FIRST_PIPE_INSTANCE
// does, so listening on a name that exists fails.
func ListenUserPipe(name string, bufferSize int32) (net.Listener, error) {
	sid, err := userSID()
	if err != nil {
		return nil, err
	}
	return winio.ListenPipe(name, &winio.PipeConfig{
		SecurityDescriptor: pipeSDDL(sid),
		MessageMode:        true,
		InputBufferSize:    bufferSize,
		OutputBufferSize:   bufferSize,
	})
}

// DialUserPipe connects to a pipe this user owns, and closes one it does not
// own before sending anything. A pipe name is easy to predict, so another user
// can create it first, but cannot make this user its owner.
func DialUserPipe(ctx context.Context, name string) (net.Conn, error) {
	sid, err := userSID()
	if err != nil {
		return nil, err
	}
	conn, err := winio.DialPipeContext(ctx, name)
	if err != nil {
		return nil, err
	}
	owner, err := pipeOwnerSID(conn)
	if err == nil && owner != sid {
		err = errNotOurPipe
	}
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return conn, nil
}

// IsPipeInUse reports whether this user's daemon pipe exists. Without the
// user's SID there is no pipe to name, and none in use.
func IsPipeInUse() bool {
	name, err := UserPipeName(pipeBase)
	return err == nil && pipeInUse(name)
}

// PipeOwnedByAnotherUser reports whether this user's daemon pipe exists but
// another user created it: another user owns it, or it does not admit this
// user, as this user's own always does. No daemon of this user can listen
// there until it is gone.
func PipeOwnedByAnotherUser() bool {
	name, err := UserPipeName(pipeBase)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	conn, err := DialUserPipe(ctx, name)
	if err == nil {
		conn.Close()
	}
	return errors.Is(err, errNotOurPipe) || errors.Is(err, windows.ERROR_ACCESS_DENIED)
}

// pipeInUse checks if the named pipe exists (another daemon may own it).
// Returns true if pipe exists (connected, busy, or access denied).
// Returns false ONLY if pipe does not exist (ERROR_FILE_NOT_FOUND).
// Uses errors.As to unwrap errno because os.IsNotExist is unreliable for pipes.
func pipeInUse(name string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	conn, err := winio.DialPipeContext(ctx, name)
	if conn != nil {
		conn.Close()
		return true // Pipe exists and we connected
	}
	if err == nil {
		return false // Shouldn't happen, but treat as absent
	}

	var errno syscall.Errno
	if errors.As(err, &errno) {
		if errno == windows.ERROR_FILE_NOT_FOUND {
			return false // Pipe definitively does not exist
		}
		// ERROR_PIPE_BUSY, ERROR_ACCESS_DENIED -> pipe exists
		return true
	}

	// For any other error (timeout, wrapped errors, etc.), assume pipe exists to be safe
	return true
}
