// Package wailsapp provides the Wails-based GUI for Rescale Interlink.
package wailsapp

import (
	"fmt"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/ipc"
)

// ensureAllConfigPersisted writes the current in-memory Config and API key to
// disk before handing off to a different-identity process (Windows Service
// via UAC, or a subprocess daemon). In-memory state must reach disk before
// the handoff — otherwise the consuming process reads stale or missing files.
//
// Idempotent: calling with unchanged state is a no-op. Secrets that should
// never be persisted (currently the proxy password) are filtered out by
// config.SaveConfigCSV; this helper does not touch them.
//
// Token file handling:
//   - In-memory key set → write to the token file iff it differs from disk.
//   - In-memory key cleared → remove the token file. Otherwise a service
//     booting later would resurrect the stale credential.
//
// Called before:
//   - StartDaemon, StartServiceElevated, InstallServiceElevated,
//     InstallAndStartServiceElevated, ReloadDaemonConfig (handoffs).
//   - SaveDaemonConfig (persistence is the point).
func (a *App) ensureAllConfigPersisted() error {
	if a.config == nil {
		return nil
	}

	configPath := config.GetDefaultConfigPath()
	if configPath != "" {
		if err := config.SaveConfigCSV(a.config, configPath); err != nil {
			return fmt.Errorf("%s: %w", ipc.CanonicalText[ipc.CodeConfigInvalid], err)
		}
	}

	tokenPath := config.GetDefaultTokenPath()
	if tokenPath == "" {
		return nil
	}

	if a.config.APIKey == "" {
		if removed, err := removeSavedAPIKeyTokenFiles(); err != nil {
			return fmt.Errorf("%s: failed to remove stale token file: %w",
				ipc.CanonicalText[ipc.CodeConfigInvalid], err)
		} else if removed > 0 {
			a.logInfo("config", "API key cleared, token file removed")
		}
		return nil
	}

	existing, _ := config.ReadTokenFile(tokenPath)
	if existing == a.config.APIKey {
		return nil
	}
	if err := config.WriteTokenFile(tokenPath, a.config.APIKey); err != nil {
		return fmt.Errorf("%s: failed to write API key to token file: %w",
			ipc.CanonicalText[ipc.CodeNoTokenFile], err)
	}
	a.logInfo("config", "API key written to token file")
	return nil
}
