// Package daemon provides background service functionality for auto-downloading completed jobs.
// v4.3.2: Custom log writer for daemon that captures logs for IPC streaming.
package daemon

import (
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"gopkg.in/natefinch/lumberjack.v2"
)

// DaemonLogWriter is a multi-writer that sends logs to:
// 1. Console (stdout/stderr)
// 2. File (if configured)
// 3. LogBuffer (for IPC streaming)
type DaemonLogWriter struct {
	mu         sync.RWMutex
	console    io.Writer
	file       *lumberjack.Logger
	buffer     *LogBuffer
	fileEnabled bool
}

// DaemonLogConfig configures the daemon logger.
type DaemonLogConfig struct {
	// LogFile is the path to write logs (empty = no file logging)
	LogFile string

	// Console enables console output (default: true for foreground, false for background)
	Console bool

	// BufferSize is the number of log entries to keep in memory for IPC
	BufferSize int
}

// NewDaemonLogWriter creates a new daemon log writer.
func NewDaemonLogWriter(cfg DaemonLogConfig) *DaemonLogWriter {
	w := &DaemonLogWriter{
		buffer: NewLogBuffer(cfg.BufferSize),
	}

	if cfg.Console {
		w.console = zerolog.ConsoleWriter{
			Out:        os.Stdout,
			TimeFormat: "15:04:05",
		}
	}

	if cfg.LogFile != "" {
		w.file = &lumberjack.Logger{
			Filename:   cfg.LogFile,
			MaxSize:    10, // MB
			MaxBackups: 5,
			MaxAge:     30, // days
			Compress:   true,
		}
		w.fileEnabled = true
	}

	return w
}

// Write implements io.Writer for zerolog.
// Parses JSON log entries and routes to appropriate destinations.
func (w *DaemonLogWriter) Write(p []byte) (n int, err error) {
	n = len(p)

	// Parse the JSON log entry
	var entry struct {
		Level   string                 `json:"level"`
		Time    string                 `json:"time"`
		Message string                 `json:"message"`
		Stage   string                 `json:"stage"`
		Extra   map[string]interface{} `json:"-"`
	}

	// Unmarshal to get basic fields
	if err := json.Unmarshal(p, &entry); err == nil {
		// Get all fields for extras
		var allFields map[string]interface{}
		json.Unmarshal(p, &allFields)

		// Remove known fields to get extras
		delete(allFields, "level")
		delete(allFields, "time")
		delete(allFields, "message")
		delete(allFields, "stage")

		// Add to buffer for IPC streaming
		w.buffer.Add(
			entry.Level,
			entry.Stage,
			entry.Message,
			allFields,
		)
	}

	// Write to console
	w.mu.RLock()
	if w.console != nil {
		w.console.Write(p)
	}

	// Write to file
	if w.fileEnabled && w.file != nil {
		// Format for file: timestamp [LEVEL] stage: message
		timestamp := time.Now().Format("2006-01-02 15:04:05.000")
		level := entry.Level
		if level == "" {
			level = "INFO"
		}
		stage := entry.Stage
		if stage == "" {
			stage = "daemon"
		}
		msg := entry.Message
		if msg == "" {
			msg = string(p)
		}

		fileEntry := timestamp + " [" + level + "] " + stage + ": " + msg + "\n"
		w.file.Write([]byte(fileEntry))
	}
	w.mu.RUnlock()

	return n, nil
}

// GetBuffer returns the log buffer for IPC access.
func (w *DaemonLogWriter) GetBuffer() *LogBuffer {
	return w.buffer
}

// Close closes the file logger if open.
func (w *DaemonLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file != nil {
		return w.file.Close()
	}
	return nil
}

// SetFileLogging enables or disables file logging.
func (w *DaemonLogWriter) SetFileLogging(enabled bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.fileEnabled = enabled
}

// CreateDaemonLogger creates a zerolog logger configured for daemon use.
// Returns the logger and the writer (for accessing the log buffer).
func CreateDaemonLogger(cfg DaemonLogConfig) (zerolog.Logger, *DaemonLogWriter) {
	writer := NewDaemonLogWriter(cfg)

	// Create zerolog with JSON output (for parsing)
	// The writer will format for console/file appropriately
	logger := zerolog.New(writer).
		With().
		Timestamp().
		Str("stage", "daemon").
		Logger()

	return logger, writer
}
