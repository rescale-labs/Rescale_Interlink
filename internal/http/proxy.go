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
)

// ConfigureHTTPClient configures an HTTP client with proxy settings
func ConfigureHTTPClient(cfg *config.Config) (*nethttp.Client, error) {
	transport := &nethttp.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   100, // CRITICAL: Keep 100 idle connections (was missing, defaulted to 2)
		MaxConnsPerHost:       100, // Total connections per host (must be >= MaxIdleConnsPerHost)
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   60 * time.Second, // Extended for slow networks and high concurrency
		ExpectContinueTimeout: 1 * time.Second,
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

		// v0.7.2: Automatic warmup for basic mode to prevent session timeouts
		client := &nethttp.Client{
			Transport: transport,
			Timeout:   300 * time.Second,
		}

		// Always perform warmup in basic mode (automatic for --basic mode in v0.7.2)
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

// ForceWarmup performs a warmup request to establish fresh proxy connection (v0.7.2)
// This is used for retry logic when proxy timeout is detected
func ForceWarmup(client *nethttp.Client, apiBaseURL string) error {
	if apiBaseURL == "" {
		apiBaseURL = "https://platform.rescale.com"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Use GET request (not HEAD) for full authentication handshake
	req, err := nethttp.NewRequestWithContext(ctx, "GET", apiBaseURL+"/api/v3/", nil)
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

// ShouldBypassProxy checks if a host should bypass the proxy
func ShouldBypassProxy(host string, noProxy string) bool {
	if noProxy == "" {
		return false
	}

	host = strings.ToLower(strings.TrimSpace(host))
	bypasses := strings.Split(noProxy, ",")

	for _, bypass := range bypasses {
		bypass = strings.ToLower(strings.TrimSpace(bypass))
		if bypass == "" {
			continue
		}

		// Exact match
		if host == bypass {
			return true
		}

		// Domain suffix match
		if strings.HasPrefix(bypass, ".") && strings.HasSuffix(host, bypass) {
			return true
		}

		// Subdomain match
		if strings.HasSuffix(host, "."+bypass) {
			return true
		}
	}

	return false
}
