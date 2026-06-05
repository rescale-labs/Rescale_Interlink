//go:build !fips && !fips3

package config

import "testing"

func TestNTLMProxySupportedInNonFIPSBuild(t *testing.T) {
	if !NTLMProxySupported() {
		t.Fatal("NTLMProxySupported() = false, want true")
	}

	if err := ValidateProxyModeForBuild("ntlm"); err != nil {
		t.Fatalf("ValidateProxyModeForBuild(\"ntlm\") = %v, want nil", err)
	}
}
