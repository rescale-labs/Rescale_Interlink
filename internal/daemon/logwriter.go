// Package daemon provides background service functionality for auto-downloading completed jobs.
package daemon

import (
	"encoding/json"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/reporting"
)

// DaemonLogWriter is a multi-writer that sends logs to:
// 1. Console (stdout/stderr)
// 2. File (if configured)
// 3. LogBuffer (for IPC streaming)
type DaemonLogWriter struct {
	mu          sync.RWMutex
	console     io.Writer
	file        *lumberjack.Logger
	buffer      *LogBuffer
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
		w.console = logging.NewConsoleWriter(os.Stdout)
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
// Parses JSON log entries and routes to appropriate destinations. Every entry
// is redacted first: an error it quotes can carry a signed URL, and the daemon's
// standard logger, notices and zerolog all end here.
func (w *DaemonLogWriter) Write(p []byte) (n int, err error) {
	n = len(p)
	p = []byte(reporting.RedactSecrets(string(p)))

	// One parse serves the IPC buffer and the file. The fields left once level,
	// time, message and stage are taken out are the entry's own: the error,
	// the job, the path.
	var fields map[string]any
	parsed := json.Unmarshal(p, &fields) == nil
	take := func(key string) string {
		s, _ := fields[key].(string)
		delete(fields, key)
		return s
	}
	level, stage, msg := take("level"), take("stage"), take("message")
	delete(fields, "time")
	if parsed {
		w.buffer.Add(level, stage, msg, fields)
	}

	// Write to console
	w.mu.RLock()
	if w.console != nil {
		w.console.Write(p)
	}

	// Write to file: timestamp [level] stage: message, then the fields, sorted.
	if w.fileEnabled && w.file != nil {
		if level == "" {
			level = "INFO"
		}
		if stage == "" {
			stage = "daemon"
		}
		if !parsed {
			msg = string(p)
		}
		line := time.Now().Format("2006-01-02 15:04:05.000") + " [" + level + "] " + stage + ": " + msg
		for _, key := range slices.Sorted(maps.Keys(fields)) {
			line += " " + key + "=" + fieldText(fields[key])
		}
		w.file.Write([]byte(line + "\n"))
	}
	w.mu.RUnlock()

	return n, nil
}

// fieldText is a field's value as the log file shows it, and as zerolog's
// console writer does: a string bare unless it needs quoting, anything else
// as JSON.
func fieldText(v any) string {
	s, ok := v.(string)
	if !ok {
		b, _ := json.Marshal(v)
		return string(b)
	}
	if strings.ContainsFunc(s, func(r rune) bool { return r <= ' ' || r > '~' || r == '"' || r == '\\' }) {
		return strconv.Quote(s)
	}
	return s
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
