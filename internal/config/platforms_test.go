package config

import (
	"strings"
	"testing"
)

func TestValidatePlatformURL_AllAllowed(t *testing.T) {
	for _, p := range AllowedPlatformURLs {
		if err := ValidatePlatformURL(p.URL); err != nil {
			t.Errorf("ValidatePlatformURL(%q) = %v, want nil", p.URL, err)
		}
	}
}

func TestValidatePlatformURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		// wantErrSubstr empty means the URL must validate; otherwise the error
		// must name the reason so the user can act on it.
		wantErrSubstr string
	}{
		{name: "no scheme auto-adds https", url: "platform.rescale.com"},
		{name: "trailing slash", url: "https://eu.rescale.com/"},
		{name: "case insensitive host", url: "PLATFORM.RESCALE.COM"},

		// Unknown host.
		{name: "unknown host", url: "https://evil.example.com", wantErrSubstr: "unrecognized platform URL"},
		{name: "subdomain attack", url: "https://platform.rescale.com.evil.com", wantErrSubstr: "unrecognized platform URL"},
		{name: "empty URL", url: "", wantErrSubstr: "empty"},

		// Strict origin enforcement — each of these is a credential
		// exfiltration vector if it slips through.
		{name: "http scheme", url: "http://platform.rescale.com", wantErrSubstr: "HTTPS"},
		{name: "custom port", url: "https://platform.rescale.com:8443", wantErrSubstr: "port"},
		{name: "userinfo", url: "https://user@platform.rescale.com", wantErrSubstr: "userinfo"},
		{name: "path", url: "https://platform.rescale.com/foo", wantErrSubstr: "path"},
		{name: "query", url: "https://platform.rescale.com?bar=1", wantErrSubstr: "query"},
		{name: "fragment", url: "https://platform.rescale.com#frag", wantErrSubstr: "fragment"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePlatformURL(tt.url)
			if tt.wantErrSubstr == "" {
				if err != nil {
					t.Errorf("ValidatePlatformURL(%q) = %v, want nil", tt.url, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidatePlatformURL(%q) = nil, want error", tt.url)
			}
			if !strings.Contains(err.Error(), tt.wantErrSubstr) {
				t.Errorf("error = %q, want mention of %q", err.Error(), tt.wantErrSubstr)
			}
		})
	}
}
