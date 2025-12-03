// Package validation provides core type validation against the Rescale API.
// Version: 1.0.0
// Date: October 7, 2025
//
// This package validates hardware core types by fetching available options
// from the Rescale API and providing helpful error messages with suggestions
// for invalid types.
//
// Features:
//   - Fetches available core types from /api/v3/coretypes/
//   - Caches results (see constants.ValidationCacheTTL) to reduce API calls
//   - Provides suggestions for typos (e.g., "emerld" → "emerald")
//   - Thread-safe with concurrent access support
//
// Part of PUR (Parallel Uploader and Runner) v1.0.0
package validation

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/models"
)

// CoreTypeValidator validates core types against available options
type CoreTypeValidator struct {
	client      *api.Client
	coreTypes   []models.CoreType
	coreTypeMap map[string]bool
	mu          sync.RWMutex
	lastFetch   time.Time
	cacheTTL    time.Duration
}

// NewCoreTypeValidator creates a new core type validator
func NewCoreTypeValidator(client *api.Client) *CoreTypeValidator {
	return &CoreTypeValidator{
		client:      client,
		coreTypeMap: make(map[string]bool),
		cacheTTL:    constants.ValidationCacheTTL,
	}
}

// FetchCoreTypes fetches available core types from API
func (v *CoreTypeValidator) FetchCoreTypes(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	// Check if cache is still valid
	if time.Since(v.lastFetch) < v.cacheTTL && len(v.coreTypes) > 0 {
		return nil
	}

	coreTypes, err := v.client.GetCoreTypes(ctx, true)
	if err != nil {
		return fmt.Errorf("failed to fetch core types: %w", err)
	}

	v.coreTypes = coreTypes
	v.coreTypeMap = make(map[string]bool)

	// All core types returned by the API are available for use
	for _, ct := range coreTypes {
		v.coreTypeMap[strings.ToLower(ct.Code)] = true
	}

	v.lastFetch = time.Now()

	return nil
}

// Validate checks if a core type is valid
func (v *CoreTypeValidator) Validate(coreType string) error {
	v.mu.RLock()
	defer v.mu.RUnlock()

	if len(v.coreTypeMap) == 0 {
		return fmt.Errorf("core types not loaded - call FetchCoreTypes first")
	}

	normalizedType := strings.ToLower(strings.TrimSpace(coreType))
	if !v.coreTypeMap[normalizedType] {
		// Find similar core types for helpful error message
		similar := v.findSimilarCoreTypes(normalizedType, 3)
		if len(similar) > 0 {
			return fmt.Errorf("invalid core type %q, did you mean: %s", coreType, strings.Join(similar, ", "))
		}
		return fmt.Errorf("invalid core type %q - not found in available core types", coreType)
	}

	return nil
}

// findSimilarCoreTypes finds core types similar to the given invalid type
func (v *CoreTypeValidator) findSimilarCoreTypes(invalid string, limit int) []string {
	var similar []string

	for _, ct := range v.coreTypes {
		code := strings.ToLower(ct.Code)

		// Check if invalid type is a substring of valid type
		if strings.Contains(code, invalid) || strings.Contains(invalid, code) {
			similar = append(similar, ct.Code)
			if len(similar) >= limit {
				break
			}
		}
	}

	// If no similar found, return first few available types
	if len(similar) == 0 {
		for i, ct := range v.coreTypes {
			similar = append(similar, ct.Code)
			if i >= limit-1 {
				break
			}
		}
	}

	return similar
}
