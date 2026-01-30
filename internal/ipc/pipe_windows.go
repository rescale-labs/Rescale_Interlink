//go:build windows

// Package ipc provides inter-process communication between the Windows service
// and the GUI/tray application using named pipes.
package ipc

import (
	"context"
	"errors"
	"syscall"
	"time"

	"github.com/Microsoft/go-winio"
)

// Windows error codes for named pipes
const (
	ERROR_FILE_NOT_FOUND = syscall.Errno(2)
	ERROR_PIPE_BUSY      = syscall.Errno(231)
	ERROR_ACCESS_DENIED  = syscall.Errno(5)
)

// IsPipeInUse checks if the named pipe exists (another daemon may own it).
// Returns true if pipe exists (connected, busy, or access denied).
// Returns false ONLY if pipe does not exist (ERROR_FILE_NOT_FOUND).
// v4.5.5: Robust Windows error handling - os.IsNotExist is unreliable for pipes.
func IsPipeInUse() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	conn, err := winio.DialPipeContext(ctx, PipeName)
	if conn != nil {
		conn.Close()
		return true // Pipe exists and we connected
	}
	if err == nil {
		return false // Shouldn't happen, but treat as absent
	}

	// v4.5.5: Use errors.As to unwrap winio's wrapped errors
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if errno == ERROR_FILE_NOT_FOUND {
			return false // Pipe definitively does not exist
		}
		// ERROR_PIPE_BUSY, ERROR_ACCESS_DENIED -> pipe exists
		return true
	}

	// For any other error (timeout, wrapped errors, etc.), assume pipe exists to be safe
	return true
}
