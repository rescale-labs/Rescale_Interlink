// Package compat provides rescale-cli compatibility mode for Interlink.
// It implements a separate Cobra command tree that mirrors rescale-cli's
// flag syntax, exit codes, and output format while using Interlink's
// backend services.
//
// Architecture: compat imports config, api, models, version, and reporting
// directly — it does NOT import the cli package, avoiding import cycles.
package compat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
)

// ExitCodeCompatError is the exit code for compat mode errors,
// matching rescale-cli's convention.
const ExitCodeCompatError = 33

// compatContextKey is the context key for storing CompatContext on Cobra commands.
type compatContextKey struct{}

// CompatContext holds compat-mode state that persists across PersistentPreRunE
// and individual command RunE functions.
type CompatContext struct {
	APIKey     string
	APIBaseURL string
	Quiet      bool
	NoPrompt   bool
	AuthEmail  string      // cached from GetUserProfile after auth
	apiClient  *api.Client // lazily created, cached
}

// IsCompatMode returns true if the given args indicate compat mode should activate.
// Compat mode activates when:
//   - --compat flag is present anywhere in args
//   - the binary name (args[0]) ends with "rescale-cli"
func IsCompatMode(args []string) bool {
	if len(args) == 0 {
		return false
	}

	// Check --compat flag
	for _, arg := range args[1:] {
		if arg == "--compat" {
			return true
		}
		// Stop scanning at first non-flag that isn't a known global flag value
		// to avoid matching --compat inside subcommand arg values.
		// However, --compat is always a boolean flag so it won't be a value.
	}

	// Check binary name
	base := filepath.Base(args[0])
	return base == "rescale-cli" || base == "rescale-cli.exe"
}

// FilterCompatFlag returns a copy of args with --compat removed.
// Other args are preserved in order.
func FilterCompatFlag(args []string) []string {
	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		if arg != "--compat" {
			filtered = append(filtered, arg)
		}
	}
	return filtered
}

// FormatSLF4JTimestamp formats a time in rescale-cli's SLF4J-style timestamp format.
func FormatSLF4JTimestamp(t time.Time) string {
	return t.Format("2006-01-02 15:04:05,000")
}

// FormatAuthLine formats the authentication success message matching rescale-cli output.
func FormatAuthLine(email string) string {
	return FormatSLF4JTimestamp(time.Now()) + " - Authenticated as " + email
}

// FormatErrorMessage formats an error message with SLF4J timestamp prefix.
func FormatErrorMessage(msg string) string {
	return FormatSLF4JTimestamp(time.Now()) + " - ERROR - " + msg
}

// SetCompatContext stores a CompatContext in the command's context.
func SetCompatContext(cmd *cobra.Command, cc *CompatContext) {
	ctx := context.WithValue(cmd.Context(), compatContextKey{}, cc)
	cmd.SetContext(ctx)
}

// GetCompatContext retrieves the CompatContext from the command's context.
// Returns nil if no compat context is set.
func GetCompatContext(cmd *cobra.Command) *CompatContext {
	if cmd.Context() == nil {
		return nil
	}
	cc, _ := cmd.Context().Value(compatContextKey{}).(*CompatContext)
	return cc
}

// GetAPIClient returns a configured API client, creating one lazily on first call.
// Credential resolution follows compat precedence: -p flag > RESCALE_API_KEY env > default token file.
// Profile/apiconfig support is deferred to Plan 2+.
func (cc *CompatContext) GetAPIClient(ctx context.Context) (*api.Client, error) {
	if cc.apiClient != nil {
		return cc.apiClient, nil
	}

	// Compat credential chain: flag > env > default token file
	apiKey := cc.APIKey
	if apiKey == "" {
		apiKey = os.Getenv("RESCALE_API_KEY")
	}
	if apiKey == "" {
		if tokenPath := config.GetDefaultTokenPath(); tokenPath != "" {
			if key, err := config.ReadTokenFile(tokenPath); err == nil && key != "" {
				apiKey = key
			}
		}
	}

	if apiKey == "" {
		return nil, fmt.Errorf("no API key provided: use -p flag, RESCALE_API_KEY env var, or run 'rescale-int config init'")
	}

	// Base URL chain: flag > env > default
	baseURL := cc.APIBaseURL
	if baseURL == "" {
		baseURL = os.Getenv("RESCALE_API_URL")
	}
	if baseURL == "" {
		baseURL = "https://platform.rescale.com"
	}

	// Ensure HTTPS scheme
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "https://" + baseURL
	}

	cfg := &config.Config{
		APIKey:     apiKey,
		APIBaseURL: baseURL,
		TenantURL:  baseURL,
	}
	client, err := api.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create API client: %w", err)
	}
	cc.apiClient = client
	return client, nil
}

// Printf prints formatted output to stdout, suppressed in quiet mode.
// This is for informational output — errors and data output bypass this.
func (cc *CompatContext) Printf(format string, args ...interface{}) {
	if cc.Quiet {
		return
	}
	fmt.Fprintf(os.Stdout, format, args...)
}
