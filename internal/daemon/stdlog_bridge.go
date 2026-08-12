// Package daemon provides background service functionality for auto-downloading completed jobs.
package daemon

import (
	stdlog "log"
	"strings"

	"github.com/rescale/rescale-int/internal/logging"
)

// stdlogBridge adapts the standard library logger to a zerolog logger.
type stdlogBridge struct {
	logger *logging.Logger
}

// Write implements io.Writer for log.SetOutput. The standard logger hands over
// one already-formatted line per call.
func (b stdlogBridge) Write(p []byte) (int, error) {
	msg := strings.TrimRight(string(p), "\n")
	if msg != "" {
		b.logger.Debug().Str("source", "stdlog").Msg(msg)
	}
	return len(p), nil
}

// RouteStdlibLogTo redirects the standard library logger into the daemon's own
// logger, which reaches the log file and the IPC log buffer.
//
// The shared transfer path writes its per-file diagnostics ([SLOT], [CRED],
// [TIMING], [BATCH]) through the standard logger. A daemon child process is
// started with stderr closed, so those lines were written to /dev/null —
// exactly the diagnostics needed to explain a stalled or failing auto-download.
//
// Call once, from the daemon process only: the standard logger's output is
// process-global.
func RouteStdlibLogTo(logger *logging.Logger) {
	if logger == nil {
		return
	}
	// Drop the standard logger's own date/time prefix; the daemon logger
	// timestamps every entry.
	stdlog.SetFlags(0)
	stdlog.SetOutput(stdlogBridge{logger: logger})
}
