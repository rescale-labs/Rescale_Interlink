// Package wailsapp provides the Wails-based GUI for Rescale Interlink.
package wailsapp

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
	"weak"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/events"
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
// Location: ~/.config/rescale/logs/ (Unix) or %LOCALAPPDATA%\Rescale\Interlink\logs (Windows)
func InitFileLogger() error {
	fileLoggerMu.Lock()

	if fileLogger != nil {
		fileLoggerMu.Unlock()
		return nil // Already initialized
	}

	logDir := config.LogDirectory()
	if err := os.MkdirAll(logDir, 0700); err != nil {
		fileLoggerMu.Unlock()
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	// Configure rotating file logger
	logPath := filepath.Join(logDir, config.InterlinkLogName)
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

// written holds, weakly, the log events App.log has already written to
// interlink.log itself, synchronously, so the event bridge writes only the
// others. An entry goes when the bridge meets its event, or when the event is
// collected without reaching the bridge, which the event bus's drop of events
// for a subscriber that falls behind can cause.
var written sync.Map // weak.Pointer[events.LogEvent] -> struct{}

// publishWritten publishes a log event whose message is already in interlink.log.
func publishWritten(bus *events.EventBus, level events.LogLevel, message, stage string) {
	ev := &events.LogEvent{
		BaseEvent: events.BaseEvent{EventType: events.EventLog, Time: time.Now()},
		Level:     level, Message: message, Stage: stage,
	}
	key := weak.Make(ev)
	written.Store(key, struct{}{})
	runtime.AddCleanup(ev, func(key weak.Pointer[events.LogEvent]) { written.Delete(key) }, key)
	bus.Publish(ev)
}

// writeLogEvent writes a bus log event to interlink.log, as dto shows it in
// the Activity tab (redacted), unless App.log already has.
func writeLogEvent(ev *events.LogEvent, dto LogEventDTO) {
	if _, done := written.LoadAndDelete(weak.Make(ev)); done {
		return
	}
	message := dto.Message
	if dto.Error != "" {
		message += ": " + dto.Error
	}
	WriteToLogFile(dto.Level, dto.Stage, message)
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
