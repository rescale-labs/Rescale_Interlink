// Package logging provides structured logging for both CLI and GUI modes.
package logging

import (
	"io"
	"os"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/rescale/rescale-int/internal/events"
)

// Logger wraps zerolog with mode-specific behavior.
type Logger struct {
	zlog     zerolog.Logger
	mode     string // "cli" or "gui"
	eventBus *events.EventBus
	output   io.Writer // current output writer
}

// NewLogger creates a new logger for the specified mode.
func NewLogger(mode string, eventBus *events.EventBus) *Logger {
	var output io.Writer

	if mode == "cli" {
		// CLI mode: Use stdout for logs (stderr reserved for progress bars)
		output = zerolog.ConsoleWriter{
			Out:        os.Stdout,
			TimeFormat: "15:04:05",
		}
	} else {
		// GUI mode: Write to stderr for debugging
		output = zerolog.ConsoleWriter{
			Out:        os.Stderr,
			TimeFormat: "15:04:05",
		}
		// In GUI mode, we could also send to event bus if needed
	}

	logger := zerolog.New(output).
		With().
		Timestamp().
		Logger()

	return &Logger{
		zlog:     logger,
		mode:     mode,
		eventBus: eventBus,
		output:   output,
	}
}

// NewDefaultCLILogger creates a default CLI logger.
func NewDefaultCLILogger() *Logger {
	return NewLogger("cli", nil)
}

// Info returns an info level event.
func (l *Logger) Info() *zerolog.Event {
	return l.zlog.Info()
}

// Error returns an error level event.
func (l *Logger) Error() *zerolog.Event {
	return l.zlog.Error()
}

// Debug returns a debug level event.
func (l *Logger) Debug() *zerolog.Event {
	return l.zlog.Debug()
}

// Warn returns a warn level event.
func (l *Logger) Warn() *zerolog.Event {
	return l.zlog.Warn()
}

// Fatal returns a fatal level event.
func (l *Logger) Fatal() *zerolog.Event {
	return l.zlog.Fatal()
}

// With creates a child logger with additional context.
func (l *Logger) With() zerolog.Context {
	return l.zlog.With()
}

// SetOutput changes the output writer for the logger.
// This is useful for redirecting logs through progress bars.
func (l *Logger) SetOutput(w io.Writer) {
	l.output = w
	// Rebuild the logger with the new writer, preserving formatting
	if l.mode == "cli" {
		l.zlog = zerolog.New(zerolog.ConsoleWriter{
			Out:        w,
			TimeFormat: "15:04:05",
		}).With().Timestamp().Logger()
	} else {
		// GUI mode
		l.zlog = zerolog.New(zerolog.ConsoleWriter{
			Out:        w,
			TimeFormat: "15:04:05",
		}).With().Timestamp().Logger()
	}
}

// Output returns the current output writer.
func (l *Logger) Output() io.Writer {
	return l.output
}

// Debugf logs a debug message with printf-style formatting.
// This is only shown when debug/verbose mode is enabled.
func (l *Logger) Debugf(format string, args ...interface{}) {
	l.zlog.Debug().Msgf(format, args...)
}

// Infof logs an info message with printf-style formatting.
func (l *Logger) Infof(format string, args ...interface{}) {
	l.zlog.Info().Msgf(format, args...)
}

// Errorf logs an error message with printf-style formatting.
func (l *Logger) Errorf(format string, args ...interface{}) {
	l.zlog.Error().Msgf(format, args...)
}

// Warnf logs a warning message with printf-style formatting.
func (l *Logger) Warnf(format string, args ...interface{}) {
	l.zlog.Warn().Msgf(format, args...)
}

// SetGlobalLevel sets the global log level.
func SetGlobalLevel(level zerolog.Level) {
	zerolog.SetGlobalLevel(level)
}

func init() {
	// Set default log level to info
	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	// Configure global logger
	log.Logger = log.Output(zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: "15:04:05",
	})
}
