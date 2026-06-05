//go:build !fips && !fips3

package config

// NTLMProxySupported reports whether this build permits NTLM proxy mode.
func NTLMProxySupported() bool {
	return true
}

// ValidateProxyModeForBuild rejects proxy modes unavailable in this build.
func ValidateProxyModeForBuild(_ string) error {
	return nil
}

func ntlmProxyBuildWarning() string {
	return ""
}
