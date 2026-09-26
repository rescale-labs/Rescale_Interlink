// Package logging provides structured logging for both CLI and GUI modes.
package logging

import (
	"io"
	"os"

	"github.com/mattn/go-isatty"
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

// terminalFd reports whether fd shows colour: a terminal (on Windows, a console
// that renders escape codes; see consoleColor), or an MSYS or Cygwin terminal,
// which Windows sees as a pipe. Replaceable so a test can stand in a terminal.
var terminalFd = func(fd uintptr) bool { return consoleColor(fd) || isatty.IsCygwinTerminal(fd) }

// IsTerminal reports whether what is written to w reaches a terminal that shows
// colour. A file is asked directly; a wrapper answers for its destination by
// implementing IsTerminal() bool, as a redacting writer and the progress display
// do; anything else is taken to be no terminal.
func IsTerminal(w io.Writer) bool {
	switch w := w.(type) {
	case *os.File:
		return terminalFd(w.Fd())
	case interface{ IsTerminal() bool }:
		return w.IsTerminal()
	}
	return false
}

// Via returns w marked as ending up on dest, for a writer that draws on dest
// but cannot say so itself, such as the progress display.
func Via(w io.Writer, dest *os.File) io.Writer { return via{w, dest} }

type via struct {
	io.Writer
	dest *os.File
}

func (v via) IsTerminal() bool { return IsTerminal(v.dest) }

// NewConsoleWriter formats log lines for a person reading w. Colour only when w
// reaches a terminal: in a pipe or a file the escape codes are noise to whatever
// reads them. zerolog itself drops colour when NO_COLOR is set.
func NewConsoleWriter(w io.Writer) zerolog.ConsoleWriter {
	return zerolog.ConsoleWriter{Out: w, TimeFormat: "15:04:05", NoColor: !IsTerminal(w)}
}

// NewLogger creates a new logger for the specified mode.
func NewLogger(mode string, eventBus *events.EventBus) *Logger {
	// CLI mode logs to stdout (stderr is reserved for progress bars); GUI mode
	// to stderr, for debugging.
	stream := os.Stderr
	if mode == "cli" {
		stream = os.Stdout
	}
	output := NewConsoleWriter(stream)

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

// NewLoggerWithWriter creates a logger that writes to the specified writer.
// Used by daemon to capture logs for IPC streaming.
func NewLoggerWithWriter(writer io.Writer) *Logger {
	logger := zerolog.New(writer).
		With().
		Timestamp().
		Logger()

	return &Logger{
		zlog:     logger,
		mode:     "daemon",
		eventBus: nil,
		output:   writer,
	}
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

// WithOutput returns a copy of the logger that writes to w, leaving this logger
// untouched. Used to route a command's logs through its progress display for the
// duration of a transfer: the output goes through a ConsoleWriter, so what mpb
// receives is a plain formatted line, not JSON.
//
// Prefer this over SetOutput when transfer goroutines are already running — they
// share the logger, and swapping its writer underneath them is a data race.
//
// Like SetOutput, this rebuilds the zerolog logger from scratch, so context
// fields added via With() are dropped. Call it before adding per-operation
// fields, not after.
func (l *Logger) WithOutput(w io.Writer) *Logger {
	out := NewConsoleWriter(w)
	return &Logger{
		zlog:     zerolog.New(out).With().Timestamp().Logger(),
		mode:     l.mode,
		eventBus: l.eventBus,
		output:   out,
	}
}

// SetOutput changes the output writer for the logger.
// This is useful for redirecting logs through progress bars.
func (l *Logger) SetOutput(w io.Writer) {
	l.output = w
	l.zlog = zerolog.New(NewConsoleWriter(w)).With().Timestamp().Logger()
}

// Output returns the current output writer.
func (l *Logger) Output() io.Writer {
	return l.output
}

// Errorf logs an error message with printf-style formatting.
func (l *Logger) Errorf(format string, args ...interface{}) {
	l.zlog.Error().Msgf(format, args...)
}

// SetGlobalLevel sets the global log level.
func SetGlobalLevel(level zerolog.Level) {
	zerolog.SetGlobalLevel(level)
}

func init() {
	// Set default log level to info
	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	// Configure global logger
	log.Logger = log.Output(NewConsoleWriter(os.Stderr))
}
