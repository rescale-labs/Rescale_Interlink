package notify

import (
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if !cfg.Enabled {
		t.Error("Expected Enabled to be true by default")
	}
	if !cfg.ShowDownloadComplete {
		t.Error("Expected ShowDownloadComplete to be true by default")
	}
	if !cfg.ShowDownloadFailed {
		t.Error("Expected ShowDownloadFailed to be true by default")
	}
	if cfg.ShowServiceStatus {
		t.Error("Expected ShowServiceStatus to be false by default")
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"short", 10, "short"},
		{"exactly10c", 10, "exactly10c"},
		{"this is a long string", 10, "this is..."},
		{"", 10, ""},
		{"abc", 3, "abc"},
		{"abcd", 3, "..."},
	}

	for _, tt := range tests {
		result := truncate(tt.input, tt.maxLen)
		if result != tt.expected {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, result, tt.expected)
		}
	}
}

func TestShortenPath(t *testing.T) {
	tests := []struct {
		input string
		short bool // expect it to be shortened
	}{
		{"/short/path", false},
		{"/a/very/long/path/that/exceeds/the/maximum/length/for/notification/display/file.txt", true},
		{"C:\\Users\\TestUser\\Downloads\\file.txt", false},
	}

	for _, tt := range tests {
		result := shortenPath(tt.input)
		if tt.short && len(result) >= len(tt.input) {
			t.Errorf("shortenPath(%q) was not shortened: %q", tt.input, result)
		}
		if !tt.short && result != tt.input {
			// For short paths, should return unchanged
			t.Logf("shortenPath(%q) = %q (length check only)", tt.input, result)
		}
	}
}

func TestNewNotifier(t *testing.T) {
	// Test with nil config (should use defaults)
	n := NewNotifier(nil, nil)
	if n == nil {
		t.Fatal("NewNotifier returned nil")
	}
	if !n.IsEnabled() {
		t.Error("Expected notifier to be enabled by default")
	}

	// Test with custom config
	cfg := &Config{Enabled: false}
	n2 := NewNotifier(cfg, nil)
	if n2.IsEnabled() {
		t.Error("Expected notifier to be disabled when config.Enabled=false")
	}
}

func TestSetEnabled(t *testing.T) {
	n := NewNotifier(nil, nil)

	// Initially enabled
	if !n.IsEnabled() {
		t.Error("Expected initially enabled")
	}

	// Disable
	n.SetEnabled(false)
	if n.IsEnabled() {
		t.Error("Expected disabled after SetEnabled(false)")
	}

	// Re-enable
	n.SetEnabled(true)
	if !n.IsEnabled() {
		t.Error("Expected enabled after SetEnabled(true)")
	}
}

func TestParseNotifyConfig(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]string
		expected *Config
	}{
		{
			name:     "empty settings use defaults",
			settings: map[string]string{},
			expected: DefaultConfig(),
		},
		{
			name: "all disabled",
			settings: map[string]string{
				"enabled":                "false",
				"show_download_complete": "false",
				"show_download_failed":   "false",
				"show_service_status":    "false",
			},
			expected: &Config{
				Enabled:              false,
				ShowDownloadComplete: false,
				ShowDownloadFailed:   false,
				ShowServiceStatus:    false,
			},
		},
		{
			name: "all enabled",
			settings: map[string]string{
				"enabled":                "true",
				"show_download_complete": "true",
				"show_download_failed":   "true",
				"show_service_status":    "true",
			},
			expected: &Config{
				Enabled:              true,
				ShowDownloadComplete: true,
				ShowDownloadFailed:   true,
				ShowServiceStatus:    true,
			},
		},
		{
			name: "case insensitive",
			settings: map[string]string{
				"enabled": "TRUE",
			},
			expected: &Config{
				Enabled:              true,
				ShowDownloadComplete: true,
				ShowDownloadFailed:   true,
				ShowServiceStatus:    false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseNotifyConfig(tt.settings)

			if result.Enabled != tt.expected.Enabled {
				t.Errorf("Enabled: got %v, want %v", result.Enabled, tt.expected.Enabled)
			}
			if result.ShowDownloadComplete != tt.expected.ShowDownloadComplete {
				t.Errorf("ShowDownloadComplete: got %v, want %v", result.ShowDownloadComplete, tt.expected.ShowDownloadComplete)
			}
			if result.ShowDownloadFailed != tt.expected.ShowDownloadFailed {
				t.Errorf("ShowDownloadFailed: got %v, want %v", result.ShowDownloadFailed, tt.expected.ShowDownloadFailed)
			}
			if result.ShowServiceStatus != tt.expected.ShowServiceStatus {
				t.Errorf("ShowServiceStatus: got %v, want %v", result.ShowServiceStatus, tt.expected.ShowServiceStatus)
			}
		})
	}
}

func TestNotifierDisabled_NoSend(t *testing.T) {
	// When disabled, notification methods should not panic or error
	cfg := &Config{Enabled: false}
	n := NewNotifier(cfg, nil)

	// These should all be no-ops when disabled
	n.DownloadComplete("TestJob", "/path/to/output")
	n.DownloadFailed("TestJob", "test error")
	n.ServiceStarted(1)
	n.ServiceStopped()
	n.NewJobsFound(5)
	n.Alert("test alert")
	n.Beep()

	// If we get here without panicking, the test passes
}
