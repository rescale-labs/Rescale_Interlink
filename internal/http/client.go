package http

import (
	"crypto/tls"
	nethttp "net/http"
	"os"
	"time"

	"github.com/rescale/rescale-int/internal/config"
	"golang.org/x/net/http2"
)

// CreateOptimizedClient creates an HTTP client optimized for large file transfers with proxy support.
// Configuration based on extensive upload performance testing and benchmarking.
//
// Key features:
//   - Proxy support (uses ConfigureHTTPClient as base)
//   - Large connection pool for concurrent operations (512 total, 64 per host)
//   - Extended timeouts to handle large file transfers
//   - HTTP/2 support with runtime toggle (DISABLE_HTTP2 env var)
//   - Connection reuse for 5-10x speedup on repeated transfers
//   - Disabled compression (no benefit for already-compressed files)
//
// This client is shared between upload and download operations to ensure
// consistent behavior and performance characteristics.
//
// The cfg parameter provides proxy configuration. If cfg is nil, proxy settings
// are read from environment variables (HTTP_PROXY, HTTPS_PROXY, NO_PROXY).
func CreateOptimizedClient(cfg *config.Config) (*nethttp.Client, error) {
	var baseClient *nethttp.Client
	var err error

	if cfg != nil {
		// Use ConfigureHTTPClient to get a client with proper proxy settings
		// This ensures S3/Azure uploads respect the same proxy configuration as API calls
		baseClient, err = ConfigureHTTPClient(cfg)
		if err != nil {
			return nil, err
		}
	} else {
		// Fallback: create client without proxy configuration
		// This maintains backward compatibility if called without config
		baseClient = &nethttp.Client{}
	}

	// Get the transport from the base client
	tr, ok := baseClient.Transport.(*nethttp.Transport)
	if !ok {
		// If transport is not *nethttp.Transport (e.g., wrapped by NTLM negotiator),
		// we can't apply optimizations, so return the base client as-is
		// This happens with NTLM proxy mode which uses ntlmssp.Negotiator wrapper
		return baseClient, nil
	}

	// Enhance the transport with upload/download optimizations
	// These settings were determined through extensive performance testing

	// Connection pooling - supports up to ~5 concurrent file operations efficiently
	tr.MaxIdleConns = 512        // Total idle connections across all hosts
	tr.MaxIdleConnsPerHost = 100 // Idle connections per host (S3/Azure endpoints)
	tr.MaxConnsPerHost = 100     // Active + idle connections per host (must be >= MaxIdleConnsPerHost)
	tr.IdleConnTimeout = 90 * time.Second

	// Timeouts - extended to handle large file transfers
	tr.TLSHandshakeTimeout = 60 * time.Second  // Increased for slow networks and high concurrency
	tr.ExpectContinueTimeout = 1 * time.Second // For HTTP 100-continue

	// Optimizations
	tr.DisableCompression = true // No benefit for already-compressed files (tar.gz, etc.)
	tr.ForceAttemptHTTP2 = true  // HTTP/2 provides better multiplexing

	// Ensure HTTP/2 is properly configured
	_ = http2.ConfigureTransport(tr)

	// Runtime toggle for HTTP/2 (useful for debugging or compatibility issues)
	// Set DISABLE_HTTP2=true environment variable to force HTTP/1.1
	if os.Getenv("DISABLE_HTTP2") == "true" {
		tr.ForceAttemptHTTP2 = false
		tr.TLSNextProto = make(map[string]func(string, *tls.Conn) nethttp.RoundTripper)
	}

	// Update the client's transport with our optimized version
	baseClient.Transport = tr
	baseClient.Timeout = 0 // No overall timeout - each operation sets its own timeout

	return baseClient, nil
}
