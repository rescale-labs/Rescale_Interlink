// Package wailsapp provides the Wails-based GUI for Rescale Interlink.
package wailsapp

import (
	"fmt"

	"github.com/rescale/rescale-int/internal/config"
)

// ensureTokenPersisted writes the current API key to the token file if it's missing or stale.
// v4.7.6: Called before daemon/service start to ensure the token file exists for the daemon to read.
// This is in an untagged file so both Windows and non-Windows builds can use it.
func (a *App) ensureTokenPersisted() error {
	if a.config == nil || a.config.APIKey == "" {
		return nil // Nothing to persist
	}
	tokenPath := config.GetDefaultTokenPath()
	if tokenPath == "" {
		return nil // Can't determine token path
	}
	// Write if missing OR if content differs (stale token file)
	existing, _ := config.ReadTokenFile(tokenPath)
	if existing != a.config.APIKey {
		if err := config.WriteTokenFile(tokenPath, a.config.APIKey); err != nil {
			return fmt.Errorf("failed to write API key to token file: %w", err)
		}
		a.logInfo("Daemon", "API key written to token file")
	}
	return nil
}
