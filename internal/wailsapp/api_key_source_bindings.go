package wailsapp

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync/atomic"

	"github.com/rescale/rescale-int/internal/config"
)

// credSourceDiagSeq tags each credentialSource() invocation so we can correlate
// the GUI-visible state with the resolution path. Diagnostic only.
var credSourceDiagSeq atomic.Uint64

// CredentialSourceDTO describes where the Setup screen's active API key came
// from without exposing the key value.
type CredentialSourceDTO struct {
	Source              string `json:"source"`
	Label               string `json:"label"`
	Detail              string `json:"detail"`
	Warning             string `json:"warning"`
	HasAPIKey           bool   `json:"hasApiKey"`
	HasSavedToken       bool   `json:"hasSavedToken"`
	EnvironmentPresent  bool   `json:"environmentPresent"`
	LegacyConfigPresent bool   `json:"legacyConfigPresent"`
}

// ClearSavedAPIKeyResultDTO reports the result of removing the saved token file.
type ClearSavedAPIKeyResultDTO struct {
	Removed          bool                `json:"removed"`
	Message          string              `json:"message"`
	CredentialSource CredentialSourceDTO `json:"credentialSource"`
}

// GetCredentialSource returns the active GUI credential source for Setup.
func (a *App) GetCredentialSource() CredentialSourceDTO {
	return a.credentialSource()
}

// ClearSavedAPIKey removes the saved token file and refreshes in-memory config
// from the remaining GUI-supported sources.
func (a *App) ClearSavedAPIKey() (ClearSavedAPIKeyResultDTO, error) {
	removed, err := removeSavedAPIKeyTokenFiles()
	if err != nil {
		return ClearSavedAPIKeyResultDTO{
			Removed:          removed > 0,
			Message:          "Failed to remove saved API key",
			CredentialSource: a.credentialSource(),
		}, fmt.Errorf("failed to remove saved API key: %w", err)
	}

	if a.config != nil {
		apiKey, _ := resolveGUIAPIKeySource()
		a.config.APIKey = apiKey
		if a.engine != nil {
			cfgCopy := *a.config
			go func(cfg *config.Config) {
				if err := a.engine.UpdateConfig(cfg); err != nil {
					if wailsLogger != nil {
						wailsLogger.Warn().Err(err).Msg("Failed to update engine after clearing saved API key")
					}
				}
			}(&cfgCopy)
		}
	}

	source := a.credentialSource()
	message := "No saved API key file was present"
	if removed > 0 {
		switch source.Source {
		case "environment":
			message = "Saved API key removed. Active key now comes from RESCALE_API_KEY."
		case "":
			message = "Saved API key removed. No API key is configured."
		default:
			message = fmt.Sprintf("Saved API key removed. Active source is now %s.", source.Label)
		}
		if a.engine != nil {
			a.logInfo("config", "Saved API key token file removed")
		} else if wailsLogger != nil {
			wailsLogger.Info().Msg("Saved API key token file removed")
		}
	}

	return ClearSavedAPIKeyResultDTO{
		Removed:          removed > 0,
		Message:          message,
		CredentialSource: source,
	}, nil
}

func (a *App) credentialSource() CredentialSourceDTO {
	seq := credSourceDiagSeq.Add(1)

	activeKey := ""
	if a.config != nil {
		activeKey = strings.TrimSpace(a.config.APIKey)
	}

	tokenPath := config.GetDefaultTokenPath()
	tokenStat := "missing"
	tokenSize := int64(-1)
	if tokenPath != "" {
		if info, statErr := os.Stat(tokenPath); statErr == nil {
			tokenStat = "exists"
			tokenSize = info.Size()
		} else if os.IsNotExist(statErr) {
			tokenStat = "missing"
		} else {
			tokenStat = fmt.Sprintf("stat-error: %v", statErr)
		}
	}

	resolvedKey, source := resolveGUIAPIKeySource()
	resolvedSource := source
	resolvedHasKey := resolvedKey != ""

	if activeKey == "" {
		source = ""
	} else if resolvedKey == "" || activeKey != resolvedKey {
		source = "direct-input"
	}

	hasSaved := savedAPIKeyTokenPresent()
	envPresent := os.Getenv("RESCALE_API_KEY") != ""
	legacyPresent := legacyAPIKeyPresent()

	dto := CredentialSourceDTO{
		Source:              source,
		HasAPIKey:           activeKey != "",
		HasSavedToken:       hasSaved,
		EnvironmentPresent:  envPresent,
		LegacyConfigPresent: legacyPresent,
	}

	if wailsLogger != nil {
		wailsLogger.Info().
			Uint64("seq", seq).
			Str("tokenPath", tokenPath).
			Str("tokenStat", tokenStat).
			Int64("tokenSize", tokenSize).
			Str("resolvedSource", resolvedSource).
			Bool("resolvedHasKey", resolvedHasKey).
			Bool("activeKeyPresent", activeKey != "").
			Str("finalSource", source).
			Bool("HasSavedToken", hasSaved).
			Bool("EnvironmentPresent", envPresent).
			Bool("LegacyConfigPresent", legacyPresent).
			Msg("DIAG credentialSource")
	}

	switch source {
	case "token-file":
		dto.Label = "Saved token file"
		dto.Detail = "Stored in Interlink's per-user token file."
	case "environment":
		dto.Label = "RESCALE_API_KEY environment variable"
		dto.Detail = "Read from the current process environment."
		if runtime.GOOS == "windows" {
			dto.Warning = "On Windows, environment variables may be user-scoped or machine-scoped. Interlink reads this value but does not save it at startup."
		}
	case "direct-input":
		dto.Label = "Direct input"
		dto.Detail = "Current value has not been saved to the token file."
	case "":
		dto.Label = "Not configured"
		dto.Detail = "No API key is currently active."
	default:
		dto.Label = source
		dto.Detail = "Resolved from a supported credential source."
	}

	if dto.LegacyConfigPresent && source != "apiconfig" {
		dto.Warning = appendCredentialWarning(dto.Warning, "A legacy apiconfig file still contains an API key; the GUI is not using it as the active source.")
	}

	return dto
}

func appendCredentialWarning(existing, next string) string {
	if existing == "" {
		return next
	}
	return existing + " " + next
}

func resolveGUIAPIKeySource() (string, string) {
	if tokenPath := config.GetDefaultTokenPath(); tokenPath != "" {
		if key, err := config.ReadTokenFile(tokenPath); err == nil && key != "" {
			return key, "token-file"
		}
	}
	if envKey := strings.TrimSpace(os.Getenv("RESCALE_API_KEY")); envKey != "" {
		return envKey, "environment"
	}
	return "", ""
}

func savedAPIKeyTokenPresent() bool {
	key, source := resolveGUIAPIKeySource()
	return source == "token-file" && key != ""
}

func legacyAPIKeyPresent() bool {
	home := getHomeDir()
	if home == "" {
		return false
	}
	cfg, err := config.LoadAPIConfig(config.APIConfigPathForUser(home))
	return err == nil && strings.TrimSpace(cfg.APIKey) != ""
}

func removeSavedAPIKeyTokenFiles() (int, error) {
	removed := 0
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		tokenPath := config.GetDefaultTokenPath()
		if tokenPath == "" || seen[tokenPath] {
			break
		}
		seen[tokenPath] = true

		if err := os.Remove(tokenPath); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return removed, err
		}
		removed++
	}
	return removed, nil
}
