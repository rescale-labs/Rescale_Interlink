package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/logging"
)

func TestRedactArgs_SpaceSeparated(t *testing.T) {
	args := []string{"rescale-int", "daemon", "run", "--api-key", "secret123", "--poll-interval", "2m"}
	result := redactArgs(args)

	if result[4] != "[REDACTED]" {
		t.Errorf("Expected [REDACTED], got %q", result[4])
	}
	// Original should not be mutated
	if args[4] != "secret123" {
		t.Errorf("Original args were mutated: %q", args[4])
	}
	// Other args should be unchanged
	if result[5] != "--poll-interval" {
		t.Errorf("Non-key arg was modified: %q", result[5])
	}
}

func TestRedactArgs_EqualsSeparated(t *testing.T) {
	args := []string{"rescale-int", "daemon", "run", "--api-key=secret123", "--poll-interval", "2m"}
	result := redactArgs(args)

	if result[3] != "--api-key=[REDACTED]" {
		t.Errorf("Expected --api-key=[REDACTED], got %q", result[3])
	}
	// Original should not be mutated
	if args[3] != "--api-key=secret123" {
		t.Errorf("Original args were mutated: %q", args[3])
	}
}

func TestRedactArgs_NoApiKey(t *testing.T) {
	args := []string{"rescale-int", "daemon", "run", "--poll-interval", "2m"}
	result := redactArgs(args)

	if strings.Join(result, " ") != strings.Join(args, " ") {
		t.Errorf("Args without --api-key should be unchanged, got %v", result)
	}
}

func TestRedactArgs_ApiKeyAtEnd(t *testing.T) {
	// --api-key as last arg with no value following (edge case)
	args := []string{"rescale-int", "--api-key"}
	result := redactArgs(args)

	// Should not panic or modify anything since there's no next arg
	if result[1] != "--api-key" {
		t.Errorf("Expected --api-key unchanged (no value to redact), got %q", result[1])
	}
}

func TestRedactArgs_EmptyArgs(t *testing.T) {
	result := redactArgs([]string{})
	if len(result) != 0 {
		t.Errorf("Expected empty result, got %v", result)
	}
}

// TestDaemonNotifyFuncRoutesByLevel covers the notice hook the daemon installs
// over root's stderr-bound one. A detached daemon child has no stderr, and these
// notices deliberately bypass the standard logger the daemon bridges, so without
// this hook every throttling and retry notice is discarded.
func TestDaemonNotifyFuncRoutesByLevel(t *testing.T) {
	var buf bytes.Buffer
	notify := daemonNotifyFunc(logging.NewLoggerWithWriter(&buf))

	notify("warn", "Throttled by the Rescale API")
	notify("info", "Rate limit coordinator reconnected")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 log entries, got %d: %q", len(lines), buf.String())
	}

	want := []struct {
		level   string
		message string
	}{
		{"warn", "Throttled by the Rescale API"},
		{"info", "Rate limit coordinator reconnected"},
	}
	for i, w := range want {
		var entry struct {
			Level   string `json:"level"`
			Message string `json:"message"`
			Source  string `json:"source"`
		}
		if err := json.Unmarshal([]byte(lines[i]), &entry); err != nil {
			t.Fatalf("entry %d is not JSON (%v): %q", i, err, lines[i])
		}
		if entry.Level != w.level {
			t.Errorf("entry %d level = %q, want %q", i, entry.Level, w.level)
		}
		if entry.Message != w.message {
			t.Errorf("entry %d message = %q, want %q", i, entry.Message, w.message)
		}
		if entry.Source != "rate-limit" {
			t.Errorf("entry %d source = %q, want rate-limit", i, entry.Source)
		}
	}
}
