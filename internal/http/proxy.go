package http

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	nethttp "net/http"
	"net/url"
	"strings"
	"time"

	ntlmssp "github.com/Azure/go-ntlmssp"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/constants"
)

// ConfigureHTTPClient configures an HTTP client with proxy settings
func ConfigureHTTPClient(cfg *config.Config) (*nethttp.Client, error) {
	transport := &nethttp.Transport{
		DialContext: (&net.Dialer{
			Timeout:   constants.HTTPDialTimeout,
			KeepAlive: constants.HTTPDialKeepAlive,
		}).DialContext,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   100, // CRITICAL: Keep 100 idle connections (was missing, defaulted to 2)
		MaxConnsPerHost:       100, // Total connections per host (must be >= MaxIdleConnsPerHost)
		IdleConnTimeout:       constants.HTTPIdleConnTimeout,
		TLSHandshakeTimeout:   constants.HTTPTLSHandshakeTimeout, // Extended for slow networks and high concurrency
		ExpectContinueTimeout: constants.HTTPExpectContinueTimeout,
	}

	// Configure proxy based on mode
	switch strings.ToLower(cfg.ProxyMode) {
	case "no-proxy", "":
		// No proxy
		transport.Proxy = nil

	case "system":
		// Use system proxy settings from environment
		transport.Proxy = nethttp.ProxyFromEnvironment

	case "ntlm":
		// NTLM authentication
		if cfg.ProxyHost == "" {
			return nil, fmt.Errorf("proxy host required for NTLM mode")
		}

		proxyURL := buildProxyURL(cfg)
		transport.Proxy = nethttp.ProxyURL(proxyURL)

		// Wrap transport with NTLM
		client := &nethttp.Client{
			Transport: ntlmssp.Negotiator{
				RoundTripper: transport,
			},
			Timeout: 300 * time.Second,
		}

		// Perform warmup if requested
		if cfg.ProxyWarmup {
			if err := warmupProxy(client, cfg); err != nil {
				return nil, fmt.Errorf("proxy warmup failed: %w", err)
			}
		}

		return client, nil

	case "basic":
		// Basic authentication
		if cfg.ProxyHost == "" {
			return nil, fmt.Errorf("proxy host required for basic auth mode")
		}

		proxyURL := buildProxyURL(cfg)
		transport.Proxy = nethttp.ProxyURL(proxyURL)

		// Add basic auth credentials
		if cfg.ProxyUser != "" {
			proxyURL.User = url.UserPassword(cfg.ProxyUser, cfg.ProxyPassword)
		}

		// Automatic warmup for basic mode to prevent session timeouts
		client := &nethttp.Client{
			Transport: transport,
			Timeout:   300 * time.Second,
		}

		// Always perform warmup in basic mode
		if err := warmupProxy(client, cfg); err != nil {
			return nil, fmt.Errorf("proxy warmup failed: %w", err)
		}

		return client, nil

	default:
		return nil, fmt.Errorf("unsupported proxy mode: %s", cfg.ProxyMode)
	}

	client := &nethttp.Client{
		Transport: transport,
		Timeout:   300 * time.Second,
	}

	// Perform warmup if requested
	if cfg.ProxyWarmup && cfg.ProxyMode != "no-proxy" && cfg.ProxyMode != "" {
		if err := warmupProxy(client, cfg); err != nil {
			return nil, fmt.Errorf("proxy warmup failed: %w", err)
		}
	}

	return client, nil
}

// buildProxyURL constructs a proxy URL from config
func buildProxyURL(cfg *config.Config) *url.URL {
	scheme := "http"
	host := cfg.ProxyHost
	port := cfg.ProxyPort

	if port == 0 {
		port = 8080 // Default proxy port
	}

	proxyURL := &url.URL{
		Scheme: scheme,
		Host:   fmt.Sprintf("%s:%d", host, port),
	}

	if cfg.ProxyUser != "" {
		proxyURL.User = url.UserPassword(cfg.ProxyUser, cfg.ProxyPassword)
	}

	return proxyURL
}

// warmupProxy performs a warmup request to establish proxy connection
func warmupProxy(client *nethttp.Client, cfg *config.Config) error {
	// Use a lightweight endpoint for warmup
	warmupURL := cfg.APIBaseURL
	if warmupURL == "" {
		warmupURL = "https://platform.rescale.com"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := nethttp.NewRequestWithContext(ctx, "GET", warmupURL+"/api/v3/", nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("warmup request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return fmt.Errorf("warmup request returned server error: %d", resp.StatusCode)
	}

	return nil
}

// NeedsProxyPassword returns true if the proxy configuration requires a password
// but one has not been provided. Used by CLI to determine if interactive prompt is needed.
func NeedsProxyPassword(cfg *config.Config) bool {
	mode := strings.ToLower(cfg.ProxyMode)
	// Only basic and ntlm modes require credentials
	if mode != "basic" && mode != "ntlm" {
		return false
	}
	// If user is set but password is not, we need to prompt
	if cfg.ProxyUser != "" && cfg.ProxyPassword == "" {
		return true
	}
	return false
}
