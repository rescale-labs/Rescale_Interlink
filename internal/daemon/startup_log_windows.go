//go:build windows

// Package daemon provides the auto-download daemon functionality.
package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rescale/rescale-int/internal/config"
)

// StartupLogPath returns the path to the daemon startup log.
// This log captures early startup errors BEFORE IPC is available,
// allowing diagnosis of subprocess launch failures from GUI/tray.
func StartupLogPath() string {
	return filepath.Join(config.LogDirectory(), config.StartupLogName)
}

// WriteStartupLog appends a message to the startup log.
// Writes to a predictable file location that users can check for launch failures.
func WriteStartupLog(format string, args ...interface{}) {
	logPath := StartupLogPath()

	// Ensure directory exists
	// 0700 restricts directory access to owner only
	if err := os.MkdirAll(filepath.Dir(logPath), 0700); err != nil {
		return
	}

	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()

	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	message := fmt.Sprintf(format, args...)
	f.WriteString(fmt.Sprintf("[%s] %s\n", timestamp, message))
}

