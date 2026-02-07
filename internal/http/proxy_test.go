package http

import (
	"net/http"
	"net/url"
	"testing"
)

// TestProxyFuncWithBypass_EmptyNoProxy verifies that an empty noProxy always routes through proxy.
func TestProxyFuncWithBypass_EmptyNoProxy(t *testing.T) {
	proxyURL, _ := url.Parse("http://proxy.corp:8080")
	proxyFunc := proxyFuncWithBypass(proxyURL, "")

	req, _ := http.NewRequest("GET", "https://api.example.com/data", nil)
	result, err := proxyFunc(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected proxy URL, got nil (direct)")
	}
	if result.Host != "proxy.corp:8080" {
		t.Errorf("expected proxy host proxy.corp:8080, got %s", result.Host)
	}
}

// TestProxyFuncWithBypass_WildcardDomain verifies *.example.com bypasses api.example.com.
func TestProxyFuncWithBypass_WildcardDomain(t *testing.T) {
	proxyURL, _ := url.Parse("http://proxy.corp:8080")
	proxyFunc := proxyFuncWithBypass(proxyURL, "*.example.com")

	// Subdomain should bypass proxy
	req, _ := http.NewRequest("GET", "https://api.example.com/data", nil)
	result, err := proxyFunc(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil (bypass) for api.example.com, got %v", result)
	}
}

// TestProxyFuncWithBypass_ExactDomain verifies example.com bypasses root and subdomains.
func TestProxyFuncWithBypass_ExactDomain(t *testing.T) {
	proxyURL, _ := url.Parse("http://proxy.corp:8080")
	proxyFunc := proxyFuncWithBypass(proxyURL, "example.com")

	// Root domain should bypass
	req, _ := http.NewRequest("GET", "https://example.com/data", nil)
	result, err := proxyFunc(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil (bypass) for example.com, got %v", result)
	}

	// Subdomain should also bypass (per httpproxy spec, domain without leading dot matches subdomains)
	req2, _ := http.NewRequest("GET", "https://api.example.com/data", nil)
	result2, err := proxyFunc(req2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result2 != nil {
		t.Errorf("expected nil (bypass) for api.example.com, got %v", result2)
	}
}

// TestProxyFuncWithBypass_CIDR verifies IP/CIDR range matching.
func TestProxyFuncWithBypass_CIDR(t *testing.T) {
	proxyURL, _ := url.Parse("http://proxy.corp:8080")
	proxyFunc := proxyFuncWithBypass(proxyURL, "10.0.0.0/8")

	// IP in range should bypass
	req, _ := http.NewRequest("GET", "http://10.1.2.3:8080/api", nil)
	result, err := proxyFunc(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil (bypass) for 10.1.2.3, got %v", result)
	}
}

// TestProxyFuncWithBypass_NonMatchingHost verifies non-matching hosts route through proxy.
func TestProxyFuncWithBypass_NonMatchingHost(t *testing.T) {
	proxyURL, _ := url.Parse("http://proxy.corp:8080")
	proxyFunc := proxyFuncWithBypass(proxyURL, "*.internal.corp,10.0.0.0/8")

	// External host should use proxy
	req, _ := http.NewRequest("GET", "https://api.rescale.com/v3/", nil)
	result, err := proxyFunc(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected proxy URL for api.rescale.com, got nil (direct)")
	}
	if result.Host != "proxy.corp:8080" {
		t.Errorf("expected proxy host proxy.corp:8080, got %s", result.Host)
	}
}

// TestProxyFuncWithBypass_MultiplePatterns verifies comma-separated patterns work.
func TestProxyFuncWithBypass_MultiplePatterns(t *testing.T) {
	proxyURL, _ := url.Parse("http://proxy.corp:8080")
	proxyFunc := proxyFuncWithBypass(proxyURL, "*.example.com, 192.168.0.0/16, internal.corp")

	tests := []struct {
		name       string
		url        string
		wantBypass bool
	}{
		{"wildcard match", "https://api.example.com/data", true},
		{"cidr match", "http://192.168.1.100/api", true},
		{"exact domain match", "https://internal.corp/status", true},
		{"non-match", "https://api.rescale.com/v3/", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", tt.url, nil)
			result, err := proxyFunc(req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantBypass && result != nil {
				t.Errorf("expected bypass (nil) for %s, got %v", tt.url, result)
			}
			if !tt.wantBypass && result == nil {
				t.Errorf("expected proxy for %s, got nil (bypass)", tt.url)
			}
		})
	}
}
