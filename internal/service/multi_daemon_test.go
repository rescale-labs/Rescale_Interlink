package service

import (
	"testing"

	"github.com/rescale/rescale-int/internal/config"
)

func TestHashUserConfig_NilReturnsEmpty(t *testing.T) {
	if h := hashUserConfig(nil); h != "" {
		t.Fatalf("nil config should hash to empty, got %q", h)
	}
}

func TestHashUserConfig_SameFieldsSameHash(t *testing.T) {
	a := &config.Config{APIBaseURL: "https://platform.rescale.com", ProxyHost: "p", ProxyPort: 8080}
	b := &config.Config{APIBaseURL: "https://platform.rescale.com", ProxyHost: "p", ProxyPort: 8080}
	if hashUserConfig(a) != hashUserConfig(b) {
		t.Fatalf("equal configs must hash equally")
	}
}

func TestHashUserConfig_APIKeyIgnored(t *testing.T) {
	a := &config.Config{APIBaseURL: "https://x", APIKey: "aaa"}
	b := &config.Config{APIBaseURL: "https://x", APIKey: "bbb"}
	if hashUserConfig(a) != hashUserConfig(b) {
		t.Fatalf("APIKey must not contribute to the hash (tracked separately)")
	}
}

func TestHashUserConfig_ProxyPasswordIgnored(t *testing.T) {
	a := &config.Config{APIBaseURL: "https://x", ProxyPassword: "secret"}
	b := &config.Config{APIBaseURL: "https://x", ProxyPassword: "other"}
	if hashUserConfig(a) != hashUserConfig(b) {
		t.Fatalf("ProxyPassword must not contribute to the hash (never persisted)")
	}
}

func TestHashUserConfig_DetectsAPIBaseURLChange(t *testing.T) {
	a := &config.Config{APIBaseURL: "https://platform.rescale.com"}
	b := &config.Config{APIBaseURL: "https://eu.rescale.com"}
	if hashUserConfig(a) == hashUserConfig(b) {
		t.Fatalf("APIBaseURL change must affect hash")
	}
}

func TestHashUserConfig_DetectsProxyEdit(t *testing.T) {
	a := &config.Config{APIBaseURL: "https://x", ProxyMode: "no-proxy"}
	b := &config.Config{APIBaseURL: "https://x", ProxyMode: "basic", ProxyHost: "p", ProxyPort: 8080}
	if hashUserConfig(a) == hashUserConfig(b) {
		t.Fatalf("proxy change must affect hash")
	}
}
