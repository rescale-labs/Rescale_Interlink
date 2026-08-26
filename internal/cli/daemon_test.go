package cli

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/logging"
)

func TestRedactArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "space separated value is redacted",
			args: []string{"rescale-int", "daemon", "run", "--api-key", "secret123", "--poll-interval", "2m"},
			want: []string{"rescale-int", "daemon", "run", "--api-key", "[REDACTED]", "--poll-interval", "2m"},
		},
		{
			name: "equals separated value is redacted",
			args: []string{"rescale-int", "daemon", "run", "--api-key=secret123", "--poll-interval", "2m"},
			want: []string{"rescale-int", "daemon", "run", "--api-key=[REDACTED]", "--poll-interval", "2m"},
		},
		{
			name: "no api key is left alone",
			args: []string{"rescale-int", "daemon", "run", "--poll-interval", "2m"},
			want: []string{"rescale-int", "daemon", "run", "--poll-interval", "2m"},
		},
		{
			// --api-key last, with no value to redact: must not panic or reach past the end.
			name: "api key at end",
			args: []string{"rescale-int", "--api-key"},
			want: []string{"rescale-int", "--api-key"},
		},
		{
			name: "empty args",
			args: []string{},
			want: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := slices.Clone(tt.args)

			got := redactArgs(tt.args)

			if !slices.Equal(got, tt.want) {
				t.Errorf("redactArgs() = %v, want %v", got, tt.want)
			}
			if !slices.Equal(tt.args, original) {
				t.Errorf("redactArgs() mutated its input: %v, was %v", tt.args, original)
			}
		})
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
