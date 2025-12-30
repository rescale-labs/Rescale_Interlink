// Package wailsapp provides common error definitions.
package wailsapp

import "errors"

var (
	// ErrNoEngine is returned when engine is not initialized.
	ErrNoEngine = errors.New("engine not initialized")

	// ErrNoTransferService is returned when transfer service is not available.
	ErrNoTransferService = errors.New("transfer service not available")

	// ErrNoFileService is returned when file service is not available.
	ErrNoFileService = errors.New("file service not available")

	// ErrNoAPIClient is returned when API client is not configured.
	ErrNoAPIClient = errors.New("API client not configured")
)
