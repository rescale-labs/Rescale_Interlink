package http

import (
	"net/http"
	"net/url"
	"testing"
)

// TestProxyFuncWithBypass covers the NoProxy matching rules: an empty list
// proxies everything, wildcard/exact domains and CIDR ranges bypass, ports and
// surrounding whitespace do not defeat a match, and anything unmatched is
// routed through the proxy.
func TestProxyFuncWithBypass(t *testing.T) {
	tests := []struct {
		name       string
		noProxy    string
		url        string
		wantBypass bool
	}{
		{name: "empty noProxy proxies everything", noProxy: "", url: "https://api.example.com/data"},
		{name: "wildcard domain", noProxy: "*.example.com", url: "https://api.example.com/data", wantBypass: true},
		{name: "exact domain, root host", noProxy: "example.com", url: "https://example.com/data", wantBypass: true},
		// Per the httpproxy spec, a domain without a leading dot also matches subdomains.
		{name: "exact domain, subdomain", noProxy: "example.com", url: "https://api.example.com/data", wantBypass: true},
		{name: "CIDR range", noProxy: "10.0.0.0/8", url: "http://10.1.2.3:8080/api", wantBypass: true},
		{name: "non-matching host is proxied", noProxy: "*.internal.corp,10.0.0.0/8", url: "https://api.rescale.com/v3/"},
		{name: "non-matching host, wildcard only", noProxy: "*.internal.corp", url: "https://api.external.com/data"},

		// Comma-separated list, each entry exercised.
		{name: "list: wildcard match", noProxy: "*.example.com, 192.168.0.0/16, internal.corp", url: "https://api.example.com/data", wantBypass: true},
		{name: "list: cidr match", noProxy: "*.example.com, 192.168.0.0/16, internal.corp", url: "http://192.168.1.100/api", wantBypass: true},
		{name: "list: exact domain match", noProxy: "*.example.com, 192.168.0.0/16, internal.corp", url: "https://internal.corp/status", wantBypass: true},
		{name: "list: non-match", noProxy: "*.example.com, 192.168.0.0/16, internal.corp", url: "https://api.rescale.com/v3/"},

		{name: "exact blob host", noProxy: "teststorageacct.blob.core.windows.net", url: "https://teststorageacct.blob.core.windows.net/container/blob", wantBypass: true},
		{name: "explicit :443 still matches an entry without a port", noProxy: "teststorageacct.blob.core.windows.net", url: "https://teststorageacct.blob.core.windows.net:443/container/blob", wantBypass: true},
		{name: "multi-label wildcard domain", noProxy: "*.corp.example.com", url: "https://sub.corp.example.com/api", wantBypass: true},

		// Entries are trimmed, so surrounding spaces must not break a match.
		{name: "spacing in list, first entry", noProxy: " host1.example.com , host2.example.com ", url: "https://host1.example.com/api", wantBypass: true},
		{name: "spacing in list, second entry", noProxy: " host1.example.com , host2.example.com ", url: "https://host2.example.com/api", wantBypass: true},
	}

	proxyURL, err := url.Parse("http://proxy.corp:8080")
	if err != nil {
		t.Fatalf("failed to parse proxy URL: %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest("GET", tt.url, nil)
			if err != nil {
				t.Fatalf("failed to build request: %v", err)
			}

			result, err := proxyFuncWithBypass(proxyURL, tt.noProxy)(req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantBypass {
				if result != nil {
					t.Errorf("expected bypass (nil) for %s, got %v", tt.url, result)
				}
				return
			}
			if result == nil {
				t.Fatalf("expected proxy URL for %s, got nil (direct)", tt.url)
			}
			if result.Host != "proxy.corp:8080" {
				t.Errorf("expected proxy host proxy.corp:8080, got %s", result.Host)
			}
		})
	}
}
