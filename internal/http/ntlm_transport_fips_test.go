//go:build fips || fips3

package http

import (
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/config"
)

func TestConfigureHTTPClientRejectsNTLMInFIPSBuild(t *testing.T) {
	_, err := ConfigureHTTPClient(&config.Config{
		ProxyMode: "ntlm",
		ProxyHost: "proxy.example.com",
		ProxyPort: 8080,
	})
	if err == nil {
		t.Fatal("ConfigureHTTPClient returned nil error for NTLM in FIPS build")
	}
	if !strings.Contains(err.Error(), "disabled in FIPS builds") {
		t.Fatalf("error = %q, want FIPS build message", err)
	}
}
