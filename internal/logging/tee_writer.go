// TeeWriter intercepts stdlib log.Printf output and routes it to both
// the underlying writer (stderr) and the EventBus as LogEvents for GUI Activity Logs.
package logging

import (
	"io"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/rescale/rescale-int/internal/events"
)

// prefixInfo maps a bracketed prefix to its EventBus log level, stage name,
// and whether the prefix should be throttled to avoid flooding the GUI.
type prefixInfo struct {
	level    events.LogLevel
	stage    string
	throttle bool
}

// knownPrefixes maps bracket tags to their classification.
var knownPrefixes = map[string]prefixInfo{
	"BATCH":     {events.DebugLevel, "BATCH", false},
	"SLOT":      {events.DebugLevel, "SLOT", true},
	"DEBUG":     {events.DebugLevel, "DEBUG", true},
	"CRED":      {events.WarnLevel, "CRED", false},
	"RATELIMIT": {events.DebugLevel, "RATELIMIT", true},  // DEBUG + throttled (floods during transfers)
	"FIPS":      {events.InfoLevel, "FIPS", false},
	"TIMING":    {events.DebugLevel, "TIMING", true},    // DEBUG + throttled (floods during transfers)
}

// TeeWriter intercepts log.Printf output and routes it to both
// the underlying writer (stderr) and the EventBus as LogEvents.
type TeeWriter struct {
	underlying io.Writer
	eventBus   *events.EventBus
	mu         sync.Mutex
	lastEmit   map[string]time.Time
	interval   time.Duration // throttle interval per prefix
}

// NewTeeWriter creates a TeeWriter that writes to both underlying and EventBus.
// Throttled prefixes ([SLOT], [DEBUG]) are limited to one EventBus publish per interval.
func NewTeeWriter(underlying io.Writer, eventBus *events.EventBus) *TeeWriter {
	return &TeeWriter{
		underlying: underlying,
		eventBus:   eventBus,
		lastEmit:   make(map[string]time.Time),
		interval:   500 * time.Millisecond, // max 2/sec per throttled prefix
	}
}

// Write implements io.Writer. It always writes to the underlying writer first,
// then parses the output and conditionally publishes to the EventBus.
func (tw *TeeWriter) Write(p []byte) (n int, err error) {
	// Always write to underlying first — stderr output is never lost
	n, err = tw.underlying.Write(p)

	// Best-effort EventBus publish — don't fail the write if this errors
	if tw.eventBus != nil {
		tw.publishLines(string(p))
	}

	return n, err
}

// publishLines splits the input into lines and publishes each to the EventBus.
func (tw *TeeWriter) publishLines(text string) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}

		// Strip Go stdlib log timestamp prefix (e.g. "2026/03/10 14:05:23 ")
		line = stripStdlibTimestamp(line)

		level, stage := classifyLine(line)

		info, isKnown := knownPrefixes[stage]
		if isKnown && info.throttle {
			if tw.shouldThrottle(stage) {
				continue
			}
		}

		tw.eventBus.PublishLog(level, line, stage, "", nil)
	}
}

// stripStdlibTimestamp removes the "YYYY/MM/DD HH:MM:SS " prefix that Go's
// stdlib log package prepends by default. Returns the line unchanged if no
// timestamp prefix is found.
func stripStdlibTimestamp(line string) string {
	// Go stdlib format: "2006/01/02 15:04:05 " = 20 chars
	// Minimum length check: need at least 20 chars for timestamp + 1 char content
	if len(line) < 21 {
		return line
	}

	// Quick check: first char must be a digit (year start)
	if line[0] < '0' || line[0] > '9' {
		return line
	}

	// Verify pattern: NNNN/NN/NN NN:NN:NN
	// Positions:       0123456789012345678
	if line[4] == '/' && line[7] == '/' && line[10] == ' ' &&
		line[13] == ':' && line[16] == ':' && line[19] == ' ' {
		return line[20:]
	}

	return line
}

// classifyLine extracts the bracketed prefix from a log line and returns
// the corresponding log level and stage name.
func classifyLine(line string) (events.LogLevel, string) {
	// Find first '[' in the line
	start := strings.IndexByte(line, '[')
	if start < 0 {
		return events.DebugLevel, "backend"
	}

	end := strings.IndexByte(line[start:], ']')
	if end < 0 {
		return events.DebugLevel, "backend"
	}

	tag := line[start+1 : start+end]

	// Validate: tag should be uppercase letters only (not a timestamp or other bracket content)
	if !isUpperAlpha(tag) {
		return events.DebugLevel, "backend"
	}

	if info, ok := knownPrefixes[tag]; ok {
		return info.level, info.stage
	}

	return events.DebugLevel, "backend"
}

// isUpperAlpha returns true if s contains only uppercase ASCII letters.
func isUpperAlpha(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsUpper(r) || r > 127 {
			return false
		}
	}
	return true
}

// shouldThrottle returns true if this prefix was emitted too recently.
func (tw *TeeWriter) shouldThrottle(prefix string) bool {
	tw.mu.Lock()
	defer tw.mu.Unlock()

	now := time.Now()
	if last, ok := tw.lastEmit[prefix]; ok {
		if now.Sub(last) < tw.interval {
			return true
		}
	}
	tw.lastEmit[prefix] = now
	return false
}
