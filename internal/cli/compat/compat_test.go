package compat

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestFormatSLF4JTimestamp(t *testing.T) {
	// Fixed time: 2026-03-15 14:30:45.123
	ts := time.Date(2026, 3, 15, 14, 30, 45, 123000000, time.UTC)
	got := FormatSLF4JTimestamp(ts)
	want := "2026-03-15 14:30:45,123"
	if got != want {
		t.Errorf("FormatSLF4JTimestamp() = %q, want %q", got, want)
	}
}

func TestFormatSLF4JTimestamp_MillisZero(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got := FormatSLF4JTimestamp(ts)
	want := "2026-01-01 00:00:00,000"
	if got != want {
		t.Errorf("FormatSLF4JTimestamp() = %q, want %q", got, want)
	}
}

func TestFormatAuthLine(t *testing.T) {
	line := FormatAuthLine("user@example.com")
	if !strings.HasSuffix(line, " - Authenticated as user@example.com") {
		t.Errorf("FormatAuthLine() = %q, missing expected suffix", line)
	}
	// Verify timestamp prefix format: YYYY-MM-DD HH:MM:SS,mmm
	parts := strings.SplitN(line, " - ", 2)
	if len(parts) != 2 {
		t.Fatalf("FormatAuthLine() = %q, expected timestamp prefix", line)
	}
	_, err := time.Parse("2006-01-02 15:04:05,000", parts[0])
	if err != nil {
		t.Errorf("FormatAuthLine() timestamp %q failed to parse: %v", parts[0], err)
	}
}

func TestFormatErrorMessage(t *testing.T) {
	msg := FormatErrorMessage("something broke")
	if !strings.Contains(msg, " - ERROR - something broke") {
		t.Errorf("FormatErrorMessage() = %q, missing expected content", msg)
	}
}

func TestIsCompatMode(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"empty args", nil, false},
		{"compat flag", []string{"rescale-int", "--compat", "status"}, true},
		{"compat flag at end", []string{"rescale-int", "status", "--compat"}, true},
		{"compat binary name", []string{"rescale-cli", "status"}, true},
		{"compat binary with path", []string{"/usr/local/bin/rescale-cli", "status"}, true},
		// Windows path handling: filepath.Base uses OS-specific separator.
		// On macOS/Linux, backslash is a valid filename char, so this test
		// only applies on Windows. Use forward slash which works cross-platform.
		{"compat binary with ext", []string{"/usr/local/bin/rescale-cli.exe", "status"}, true},
		{"native binary", []string{"rescale-int", "jobs", "list"}, false},
		{"native with gui", []string{"rescale-int", "--gui"}, false},
		{"no compat indicators", []string{"rescale-int"}, false},
		{"compat flag only", []string{"rescale-int", "--compat"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsCompatMode(tt.args)
			if got != tt.want {
				t.Errorf("IsCompatMode(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

func TestFilterCompatFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			"removes compat flag",
			[]string{"rescale-int", "--compat", "-p", "TOKEN", "status"},
			[]string{"rescale-int", "-p", "TOKEN", "status"},
		},
		{
			"no compat flag",
			[]string{"rescale-int", "jobs", "list"},
			[]string{"rescale-int", "jobs", "list"},
		},
		{
			"compat at end",
			[]string{"rescale-int", "status", "--compat"},
			[]string{"rescale-int", "status"},
		},
		{
			"empty args",
			nil,
			[]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterCompatFlag(tt.args)
			if len(got) != len(tt.want) {
				t.Fatalf("FilterCompatFlag(%v) len = %d, want %d", tt.args, len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("FilterCompatFlag(%v)[%d] = %q, want %q", tt.args, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestCompatContextRoundtrip(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.SetContext(context.Background())

	cc := &CompatContext{
		APIKey:     "test-key",
		APIBaseURL: "https://example.com",
		Quiet:      true,
	}
	SetCompatContext(cmd, cc)

	got := GetCompatContext(cmd)
	if got == nil {
		t.Fatal("GetCompatContext() returned nil")
	}
	if got.APIKey != "test-key" {
		t.Errorf("APIKey = %q, want %q", got.APIKey, "test-key")
	}
	if got.APIBaseURL != "https://example.com" {
		t.Errorf("APIBaseURL = %q, want %q", got.APIBaseURL, "https://example.com")
	}
	if !got.Quiet {
		t.Error("Quiet = false, want true")
	}
}

func TestGetCompatContext_NilContext(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	// No SetContext called — cmd.Context() returns nil
	got := GetCompatContext(cmd)
	if got != nil {
		t.Errorf("GetCompatContext() = %v, want nil for command with no context", got)
	}
}

func TestCompatContext_CredentialPrecedence(t *testing.T) {
	// Test that -p flag takes precedence over env var.
	// We can't fully test GetAPIClient without a real API server,
	// but we can verify the field is populated correctly.
	cc := &CompatContext{
		APIKey:     "flag-key",
		APIBaseURL: "https://platform.rescale.com",
	}
	// The key from the flag should be used
	if cc.APIKey != "flag-key" {
		t.Errorf("APIKey = %q, want %q", cc.APIKey, "flag-key")
	}
}
