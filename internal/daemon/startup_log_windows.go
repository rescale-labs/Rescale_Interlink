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
// v4.3.8: This log captures early startup errors BEFORE IPC is available,
// allowing diagnosis of subprocess launch failures from GUI/tray.
// v4.4.2: Uses centralized LogDirectory() for consistent log location.
func StartupLogPath() string {
	return filepath.Join(config.LogDirectory(), "daemon-startup.log")
}

// WriteStartupLog appends a message to the startup log.
// v4.3.8: Used for debugging daemon launch failures from GUI/tray.
// This writes to a predictable file location that users can check.
func WriteStartupLog(format string, args ...interface{}) {
	logPath := StartupLogPath()

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		return
	}

	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()

	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	message := fmt.Sprintf(format, args...)
	f.WriteString(fmt.Sprintf("[%s] %s\n", timestamp, message))
}

// ClearStartupLog truncates the startup log.
// v4.3.8: Called on successful daemon startup to clear old entries.
func ClearStartupLog() {
	logPath := StartupLogPath()
	_ = os.Truncate(logPath, 0)
}
