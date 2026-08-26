package service

import (
	"testing"

	"github.com/rescale/rescale-int/internal/config"
)

// TestHashUserConfig pins which fields the daemon's config fingerprint covers.
// Secrets are excluded deliberately: the API key is tracked separately and the
// proxy password is never persisted, so neither may move the hash.
func TestHashUserConfig(t *testing.T) {
	if h := hashUserConfig(nil); h != "" {
		t.Fatalf("nil config should hash to empty, got %q", h)
	}

	tests := []struct {
		name     string
		a, b     *config.Config
		wantSame bool
		reason   string
	}{
		{
			name:     "identical fields",
			a:        &config.Config{APIBaseURL: "https://platform.rescale.com", ProxyHost: "p", ProxyPort: 8080},
			b:        &config.Config{APIBaseURL: "https://platform.rescale.com", ProxyHost: "p", ProxyPort: 8080},
			wantSame: true, reason: "equal configs must hash equally",
		},
		{
			name:     "APIKey differs",
			a:        &config.Config{APIBaseURL: "https://x", APIKey: "aaa"},
			b:        &config.Config{APIBaseURL: "https://x", APIKey: "bbb"},
			wantSame: true, reason: "APIKey must not contribute to the hash (tracked separately)",
		},
		{
			name:     "ProxyPassword differs",
			a:        &config.Config{APIBaseURL: "https://x", ProxyPassword: "secret"},
			b:        &config.Config{APIBaseURL: "https://x", ProxyPassword: "other"},
			wantSame: true, reason: "ProxyPassword must not contribute to the hash (never persisted)",
		},
		{
			name:   "APIBaseURL differs",
			a:      &config.Config{APIBaseURL: "https://platform.rescale.com"},
			b:      &config.Config{APIBaseURL: "https://eu.rescale.com"},
			reason: "APIBaseURL change must affect hash",
		},
		{
			name:   "proxy settings differ",
			a:      &config.Config{APIBaseURL: "https://x", ProxyMode: "no-proxy"},
			b:      &config.Config{APIBaseURL: "https://x", ProxyMode: "basic", ProxyHost: "p", ProxyPort: 8080},
			reason: "proxy change must affect hash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if same := hashUserConfig(tt.a) == hashUserConfig(tt.b); same != tt.wantSame {
				t.Fatalf("hashes equal = %v, want %v: %s", same, tt.wantSame, tt.reason)
			}
		})
	}
}
