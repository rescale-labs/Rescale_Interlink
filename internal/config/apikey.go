// Package config provides configuration management for Rescale Interlink.
package config

import (
	"fmt"
	"os"
	"sync"
)

var legacyKeyWarningOnce sync.Once

func warnLegacyAPIKeyOnce() {
	legacyKeyWarningOnce.Do(func() {
		fmt.Fprintf(os.Stderr, "WARNING: API key loaded from legacy apiconfig file. "+
			"For improved security, run 'rescale-int config init' to migrate your key "+
			"to the token file, then delete the api_key entry from your apiconfig.\n")
	})
}

// ResolveAPIKey returns the API key from, in order: apiKey (such as the
// --api-key flag), the token file that 'config init' and the app write, the
// api_key an older version kept in apiconfig, and RESCALE_API_KEY. It returns
// "" when none has one.
func ResolveAPIKey(apiKey string) string {
	if apiKey != "" {
		return apiKey
	}
	if tokenPath := GetDefaultTokenPath(); tokenPath != "" {
		if key, err := ReadTokenFile(tokenPath); err == nil && key != "" {
			return key
		}
	}
	if cfg, err := LoadAPIConfig(""); err == nil && cfg.APIKey != "" {
		warnLegacyAPIKeyOnce()
		return cfg.APIKey
	}
	return os.Getenv("RESCALE_API_KEY")
}
