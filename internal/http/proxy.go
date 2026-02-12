package http

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	nethttp "net/http"
	"net/url"
	"strings"
	"time"

	ntlmssp "github.com/Azure/go-ntlmssp"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/constants"
	"golang.org/x/net/http/httpproxy"
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
		// v4.5.3: Fall back to no-proxy if host is missing (incomplete saved config)
		// This allows GUI to start so user can reconfigure proxy settings
		if cfg.ProxyHost == "" {
			fmt.Printf("[WARN] Proxy mode is NTLM but host is missing - falling back to no-proxy mode\n")
			transport.Proxy = nil
			return &nethttp.Client{
				Transport: transport,
				Timeout:   300 * time.Second,
			}, nil
		}

		proxyURL := buildProxyURL(cfg)
		transport.Proxy = proxyFuncWithBypass(proxyURL, cfg.NoProxy)

		// Wrap transport with NTLM
		client := &nethttp.Client{
			Transport: ntlmssp.Negotiator{
				RoundTripper: transport,
			},
			Timeout: 300 * time.Second,
		}

		// v3.4.0: Only perform warmup if credentials are complete and warmup is requested
		// If password is missing, skip warmup - let the caller prompt for password
		if cfg.ProxyWarmup && cfg.ProxyUser != "" && cfg.ProxyPassword != "" {
			if err := warmupProxy(client, cfg); err != nil {
				return nil, fmt.Errorf("proxy warmup failed: %w", err)
			}
		}

		return client, nil

	case "basic":
		// Basic authentication
		// v4.5.3: Fall back to no-proxy if host is missing (incomplete saved config)
		// This allows GUI to start so user can reconfigure proxy settings
		if cfg.ProxyHost == "" {
			fmt.Printf("[WARN] Proxy mode is basic but host is missing - falling back to no-proxy mode\n")
			transport.Proxy = nil
			return &nethttp.Client{
				Transport: transport,
				Timeout:   300 * time.Second,
			}, nil
		}

		proxyURL := buildProxyURL(cfg)
		transport.Proxy = proxyFuncWithBypass(proxyURL, cfg.NoProxy)

		// v4.5.3: Log warning when credentials incomplete (user set but password missing)
		// This typically happens on startup when password wasn't saved for security
		if cfg.ProxyUser != "" && cfg.ProxyPassword == "" {
			fmt.Printf("[WARN] Proxy user configured but password missing - proxy auth disabled until password is set\n")
		}

		client := &nethttp.Client{
			Transport: transport,
			Timeout:   300 * time.Second,
		}

		// v3.4.3: Only perform warmup if ProxyWarmup is true AND credentials are complete
		// Previously, basic mode always did warmup regardless of ProxyWarmup flag
		// This was inconsistent with NTLM mode and could cause 30s delays on startup
		if cfg.ProxyWarmup && cfg.ProxyUser != "" && cfg.ProxyPassword != "" {
			if err := warmupProxy(client, cfg); err != nil {
				return nil, fmt.Errorf("proxy warmup failed: %w", err)
			}
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

	// v4.5.3: Only embed credentials if both user AND password are provided
	// Empty password in URL can cause auth failures with some proxies
	if cfg.ProxyUser != "" && cfg.ProxyPassword != "" {
		proxyURL.User = url.UserPassword(cfg.ProxyUser, cfg.ProxyPassword)
	}

	return proxyURL
}

// warmupProxy performs a warmup request to establish proxy connection
// v3.4.3: Reduced timeout from 30s to 15s to minimize UI blocking
func warmupProxy(client *nethttp.Client, cfg *config.Config) error {
	// Use a lightweight endpoint for warmup
	warmupURL := cfg.APIBaseURL
	if warmupURL == "" {
		warmupURL = "https://platform.rescale.com"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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

// proxyFuncWithBypass returns a proxy function that respects the NoProxy bypass list.
// v4.5.9: If noProxy is empty, behaves identically to nethttp.ProxyURL.
// When noProxy is set, uses golang.org/x/net/http/httpproxy to match hosts/CIDRs.
func proxyFuncWithBypass(proxyURL *url.URL, noProxy string) func(*nethttp.Request) (*url.URL, error) {
	if noProxy == "" {
		return nethttp.ProxyURL(proxyURL)
	}
	cfg := httpproxy.Config{
		HTTPProxy:  proxyURL.String(),
		HTTPSProxy: proxyURL.String(),
		NoProxy:    noProxy,
	}
	proxyFunc := cfg.ProxyFunc()
	return func(req *nethttp.Request) (*url.URL, error) {
		result, err := proxyFunc(req.URL)
		if result == nil {
			log.Printf("[PROXY] Bypass: %s (direct connection)", req.URL.Host)
		} else {
			log.Printf("[PROXY] Proxied: %s → %s", req.URL.Host, result.Host)
		}
		return result, err
	}
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
