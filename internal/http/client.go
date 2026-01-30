package http

import (
	"crypto/tls"
	nethttp "net/http"
	"os"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/constants"
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
		// v4.5.4: Clear the 300s timeout to allow long transfers
		// Per-operation timeouts should be used via context instead
		baseClient.Timeout = 0
		return baseClient, nil
	}

	// Enhance the transport with upload/download optimizations
	// These settings were determined through extensive performance testing

	// Connection pooling - supports up to ~5 concurrent file operations efficiently
	tr.MaxIdleConns = 512        // Total idle connections across all hosts
	tr.MaxIdleConnsPerHost = 100 // Idle connections per host (S3/Azure endpoints)
	tr.MaxConnsPerHost = 100     // Active + idle connections per host (must be >= MaxIdleConnsPerHost)
	tr.IdleConnTimeout = constants.HTTPIdleConnTimeout

	// Timeouts - extended to handle large file transfers
	tr.TLSHandshakeTimeout = constants.HTTPTLSHandshakeTimeout   // Increased for slow networks and high concurrency
	tr.ExpectContinueTimeout = constants.HTTPExpectContinueTimeout // For HTTP 100-continue

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

	// v4.5.4: Disable HTTP/2 when proxy is active to avoid stream errors
	// Proxies often have issues with HTTP/2 multiplexing, causing mid-transfer failures.
	// Trust config proxy mode first; only check env vars for "system" mode or when no config.
	var proxyActive bool
	if cfg != nil {
		switch cfg.ProxyMode {
		case "no-proxy", "":
			proxyActive = false
		case "system":
			// System mode: check env vars
			proxyActive = os.Getenv("HTTP_PROXY") != "" || os.Getenv("HTTPS_PROXY") != "" ||
				os.Getenv("http_proxy") != "" || os.Getenv("https_proxy") != ""
		default:
			// ntlm, basic, etc. - proxy is definitely active
			proxyActive = true
		}
	} else {
		// No config: check env vars
		proxyActive = os.Getenv("HTTP_PROXY") != "" || os.Getenv("HTTPS_PROXY") != "" ||
			os.Getenv("http_proxy") != "" || os.Getenv("https_proxy") != ""
	}

	// Allow power users to force HTTP/2 even through proxy with FORCE_HTTP2=true
	if proxyActive && os.Getenv("FORCE_HTTP2") != "true" {
		tr.ForceAttemptHTTP2 = false
		tr.TLSNextProto = make(map[string]func(string, *tls.Conn) nethttp.RoundTripper)
	}

	// Update the client's transport with our optimized version
	baseClient.Transport = tr
	baseClient.Timeout = 0 // No overall timeout - each operation sets its own timeout

	return baseClient, nil
}
