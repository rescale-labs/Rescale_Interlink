package http

import (
	"crypto/tls"
	nethttp "net/http"
	"os"
	"sync"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/constants"
	"golang.org/x/net/http2"
)

// Transfer clients, one per proxy configuration. S3 and Azure clients are built
// per file, and each used to get a transport of its own: a connection pool per
// file whose idle connections outlived it, kept here for sleep/wake cleanup and
// never released. One pool per configuration bounds both, and lets a batch reuse
// its connections from one file to the next.
var (
	transportMu     sync.Mutex
	transferClients = map[transferClientKey]*nethttp.Client{}
)

// transferClientKey is every setting ConfigureHTTPClient builds a transport from,
// and whether it warms the proxy up: a client built without that must not stand
// in for one a caller asked to have warmed.
type transferClientKey struct {
	mode, host, user, password, noProxy string
	port                                int
	warmup                              bool
}

// CloseAllIdleConnections closes idle connections on every transfer client.
// Called by the engine's stale-connection callback after sleep/wake wall-clock gaps.
// Safe to call concurrently; idempotent.
func CloseAllIdleConnections() {
	transportMu.Lock()
	defer transportMu.Unlock()
	for _, c := range transferClients {
		c.CloseIdleConnections()
	}
}

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
//
// Callers with the same proxy configuration get the same client, so it must not
// be modified.
func CreateOptimizedClient(cfg *config.Config) (*nethttp.Client, error) {
	if cfg == nil {
		cfg = &config.Config{ProxyMode: "system"}
	}
	key := transferClientKey{cfg.ProxyMode, cfg.ProxyHost, cfg.ProxyUser, cfg.ProxyPassword, cfg.NoProxy, cfg.ProxyPort, cfg.ProxyWarmup}

	transportMu.Lock()
	defer transportMu.Unlock()
	if client := transferClients[key]; client != nil {
		return client, nil
	}
	client, err := newOptimizedClient(cfg)
	if err != nil {
		return nil, err
	}
	transferClients[key] = client
	return client, nil
}

func newOptimizedClient(cfg *config.Config) (*nethttp.Client, error) {
	// ConfigureHTTPClient gives S3 and Azure the same proxy handling as API calls.
	baseClient, err := ConfigureHTTPClient(cfg)
	if err != nil {
		return nil, err
	}

	// Get the transport from the base client
	tr, ok := baseClient.Transport.(*nethttp.Transport)
	if !ok {
		// If transport is not *nethttp.Transport (e.g., wrapped by NTLM negotiator),
		// we can't apply optimizations, so return the base client as-is
		// This happens with NTLM proxy mode which uses ntlmssp.Negotiator wrapper
		// Clear the 300s timeout to allow long transfers.
		// Per-operation timeouts should be used via context instead.
		baseClient.Timeout = 0
		return baseClient, nil
	}
	// The proxy warm-up may already have sent a request through tr, and a
	// transport must not be changed once it has been used.
	tr = tr.Clone()

	// Enhance the transport with upload/download optimizations
	// These settings were determined through extensive performance testing

	// Connection pooling - supports up to ~5 concurrent file operations efficiently
	tr.MaxIdleConns = constants.HTTPTransferMaxIdleConns
	tr.MaxIdleConnsPerHost = constants.HTTPMaxIdleConnsPerHost
	tr.MaxConnsPerHost = constants.HTTPMaxConnsPerHost
	tr.IdleConnTimeout = constants.HTTPIdleConnTimeout

	// Timeouts - extended to handle large file transfers
	tr.TLSHandshakeTimeout = constants.HTTPTLSHandshakeTimeout     // Increased for slow networks and high concurrency
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

	// Disable HTTP/2 when proxy is active to avoid stream errors.
	// Proxies often have issues with HTTP/2 multiplexing, causing mid-transfer failures.
	// Trust config proxy mode first; only check env vars for "system" mode or when no config.
	var proxyActive bool
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
