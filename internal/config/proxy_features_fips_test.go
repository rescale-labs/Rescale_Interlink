//go:build fips || fips3

package config

import (
	"strings"
	"testing"
)

func TestNTLMProxyDisabledInFIPSBuild(t *testing.T) {
	if NTLMProxySupported() {
		t.Fatal("NTLMProxySupported() = true, want false")
	}

	err := ValidateProxyModeForBuild("ntlm")
	if err == nil {
		t.Fatal("ValidateProxyModeForBuild(\"ntlm\") returned nil, want error")
	}
	if !strings.Contains(err.Error(), "disabled in FIPS builds") {
		t.Fatalf("error = %q, want FIPS build message", err)
	}

	cfg := &Config{
		APIKey:        "test-key",
		APIBaseURL:    "https://platform.rescale.com",
		TarWorkers:    1,
		UploadWorkers: 1,
		JobWorkers:    1,
		ProxyMode:     "ntlm",
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Config.Validate() returned nil for NTLM in FIPS build, want error")
	}
}
