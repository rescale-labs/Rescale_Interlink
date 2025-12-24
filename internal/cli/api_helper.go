// Package cli provides API client helper functions.
package cli

import (
	"fmt"

	"github.com/rescale/rescale-int/internal/api"
)

// getAPIClient loads configuration and creates an API client.
// This is the standard way to get an API client in CLI commands.
// It handles config loading and client creation with proper error wrapping.
func getAPIClient() (*api.Client, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	client, err := api.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create API client: %w", err)
	}

	return client, nil
}
