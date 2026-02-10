package api

import (
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/config"
)

// TestNewClientRejectsEmptyBaseURL verifies that NewClient fails with a clear error
// when APIBaseURL is empty, instead of creating a broken client that produces
// "unsupported protocol scheme" errors on every request.
func TestNewClientRejectsEmptyBaseURL(t *testing.T) {
	cfg := &config.Config{
		APIBaseURL: "",
		APIKey:     "test-key",
		ProxyMode:  "no-proxy",
	}

	_, err := NewClient(cfg)
	if err == nil {
		t.Fatal("NewClient() should return error for empty APIBaseURL")
	}

	if !strings.Contains(err.Error(), "API base URL is empty") {
		t.Errorf("NewClient() error = %q, want error containing 'API base URL is empty'", err.Error())
	}
}

// TestNewClientAcceptsValidBaseURL verifies NewClient works with a valid config.
func TestNewClientAcceptsValidBaseURL(t *testing.T) {
	cfg := &config.Config{
		APIBaseURL: "https://platform.rescale.com",
		APIKey:     "test-key",
		ProxyMode:  "no-proxy",
	}

	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient() error = %v, want nil", err)
	}
	if client == nil {
		t.Fatal("NewClient() returned nil client")
	}
}
