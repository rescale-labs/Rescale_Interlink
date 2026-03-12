package api

import (
	nethttp "net/http"
	"strings"
	"time"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/ratelimit"
)

// NewClientForTest creates a Client for use in cross-package tests.
// Bypasses platform URL allowlist validation — httptest server URLs
// (http://127.0.0.1:XXXXX) are not in the allowlist.
// DO NOT use in production code.
func NewClientForTest(cfg *config.Config) *Client {
	return &Client{
		httpClient: &nethttp.Client{},
		config:     cfg,
		baseURL:    strings.TrimSuffix(cfg.APIBaseURL, "/"),
		apiKey:     cfg.APIKey,
		store:      ratelimit.GlobalStore(),
		metrics: &apiMetrics{
			callsByPath:   make(map[string]int64),
			callsByScope:  make(map[ratelimit.Scope]int64),
			windowStart:   time.Now(),
			scopeInWindow: make(map[ratelimit.Scope]int64),
		},
	}
}
