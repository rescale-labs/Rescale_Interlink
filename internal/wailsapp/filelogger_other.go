//go:build !windows

// Package wailsapp provides the Wails-based GUI for Rescale Interlink.
// v4.3.2: Cross-platform file logging with rotation.
// v4.4.2: Uses centralized LogDirectory() for consistent log location.
package wailsapp

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/config"
	"gopkg.in/natefinch/lumberjack.v2"
)

var (
	// fileLogger is the rotating file logger
	fileLogger *lumberjack.Logger
	// fileLoggerMu protects fileLogger
	fileLoggerMu sync.RWMutex
	// fileLoggingEnabled tracks if file logging is enabled
	fileLoggingEnabled bool
)

// InitFileLogger initializes file-based logging with rotation.
// v4.3.2: Now works on all platforms (macOS, Linux, Windows).
// v4.4.2: Uses centralized LogDirectory() for consistent log location.
// Location: ~/.config/rescale/logs/ (Unix) or %LOCALAPPDATA%\Rescale\Interlink\logs (Windows)
func InitFileLogger() error {
	fileLoggerMu.Lock()

	if fileLogger != nil {
		fileLoggerMu.Unlock()
		return nil // Already initialized
	}

	// v4.4.2: Use centralized log directory
	logDir := config.LogDirectory()
	if err := os.MkdirAll(logDir, 0755); err != nil {
		fileLoggerMu.Unlock()
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	// Configure rotating file logger
	logPath := filepath.Join(logDir, "interlink.log")
	fileLogger = &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    10, // MB per file
		MaxBackups: 5,  // Keep 5 old log files
		MaxAge:     30, // Days to keep old logs
		Compress:   true,
	}

	fileLoggingEnabled = true
	fileLoggerMu.Unlock()

	// Log startup message (outside lock since WriteToLogFile acquires lock)
	WriteToLogFile("INFO", "Interlink", fmt.Sprintf("File logging started at %s", logPath))
	WriteToLogFile("INFO", "Interlink", fmt.Sprintf("Startup time: %s", time.Now().Format(time.RFC3339)))

	return nil
}

// EnableFileLogging enables or disables file logging.
// v4.3.2: User can toggle file logging from GUI settings.
func EnableFileLogging(enabled bool) error {
	if enabled {
		// Initialize if not already done
		if err := InitFileLogger(); err != nil {
			return err
		}
	}

	fileLoggerMu.Lock()
	defer fileLoggerMu.Unlock()
	fileLoggingEnabled = enabled
	return nil
}

// IsFileLoggingEnabled returns whether file logging is currently enabled.
func IsFileLoggingEnabled() bool {
	fileLoggerMu.RLock()
	defer fileLoggerMu.RUnlock()
	return fileLoggingEnabled && fileLogger != nil
}

// WriteToLogFile writes a message to the rotating log file.
// This is called in addition to Activity tab logging (additive).
func WriteToLogFile(level, stage, message string) {
	fileLoggerMu.RLock()
	defer fileLoggerMu.RUnlock()

	if fileLogger == nil || !fileLoggingEnabled {
		return
	}

	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	logLine := fmt.Sprintf("[%s] [%s] %s: %s\n", timestamp, level, stage, message)
	fileLogger.Write([]byte(logLine))
}

// CloseFileLogger closes the file logger (call on shutdown).
func CloseFileLogger() {
	fileLoggerMu.Lock()
	defer fileLoggerMu.Unlock()

	if fileLogger != nil {
		WriteToLogFileUnsafe("INFO", "Interlink", "Shutting down")
		fileLogger.Close()
		fileLogger = nil
		fileLoggingEnabled = false
	}
}

// WriteToLogFileUnsafe writes without locking (caller must hold lock).
func WriteToLogFileUnsafe(level, stage, message string) {
	if fileLogger == nil {
		return
	}
	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	logLine := fmt.Sprintf("[%s] [%s] %s: %s\n", timestamp, level, stage, message)
	fileLogger.Write([]byte(logLine))
}

// GetLogFilePath returns the current log file path.
func GetLogFilePath() string {
	fileLoggerMu.RLock()
	defer fileLoggerMu.RUnlock()

	if fileLogger != nil {
		return fileLogger.Filename
	}
	return ""
}

// GetFileLogWriter returns an io.Writer for zerolog integration.
func GetFileLogWriter() io.Writer {
	fileLoggerMu.RLock()
	defer fileLoggerMu.RUnlock()

	if fileLogger == nil || !fileLoggingEnabled {
		return io.Discard
	}
	return fileLogger
}
