//go:build fips || fips3

package config

import (
	"fmt"
	"strings"
)

const ntlmProxyUnsupportedMessage = "NTLM proxy mode is disabled in FIPS builds because it requires non-FIPS MD4/MD5 algorithms; use 'basic', 'system', or 'no-proxy'"

// NTLMProxySupported reports whether this build permits NTLM proxy mode.
func NTLMProxySupported() bool {
	return false
}

// ValidateProxyModeForBuild rejects proxy modes unavailable in this build.
func ValidateProxyModeForBuild(proxyMode string) error {
	if strings.EqualFold(proxyMode, "ntlm") {
		return fmt.Errorf("%s", ntlmProxyUnsupportedMessage)
	}
	return nil
}

func ntlmProxyBuildWarning() string {
	return ntlmProxyUnsupportedMessage
}
