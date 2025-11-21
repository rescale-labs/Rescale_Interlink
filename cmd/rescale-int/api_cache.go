package main

import (
	"context"
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/models"
)

// APICache manages cached data from Rescale API to avoid repeated slow fetches
type APICache struct {
	coreTypes     []models.CoreType
	analysisCodes []string
	lastFetched   time.Time
	isLoading     bool
	loadError     error
	mu            sync.RWMutex
}

// NewAPICache creates a new API cache
func NewAPICache() *APICache {
	return &APICache{
		analysisCodes: getDefaultAnalysisCodes(),
	}
}

// GetCoreTypes returns cached core types, fetching if necessary
// Returns cached data, loading status, and any error
func (c *APICache) GetCoreTypes() ([]models.CoreType, bool, error) {
	c.mu.RLock()
	if len(c.coreTypes) > 0 {
		types := c.coreTypes
		c.mu.RUnlock()
		return types, false, nil
	}
	isLoading := c.isLoading
	err := c.loadError
	c.mu.RUnlock()

	return nil, isLoading, err
}

// GetAnalysisCodes returns the list of available analysis codes
func (c *APICache) GetAnalysisCodes() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.analysisCodes
}

// FetchCoreTypes fetches core types from API and caches them
// This is a slow operation and should be called in a goroutine with progress feedback
func (c *APICache) FetchCoreTypes(ctx context.Context, apiClient interface {
	GetCoreTypes(ctx context.Context) ([]models.CoreType, error)
}) error {
	c.mu.Lock()
	if c.isLoading {
		c.mu.Unlock()
		return nil // Already loading
	}
	c.isLoading = true
	c.mu.Unlock()

	// Fetch from API
	coreTypes, err := apiClient.GetCoreTypes(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()

	c.isLoading = false
	if err != nil {
		c.loadError = err
		// Use fallback defaults on error
		c.coreTypes = getDefaultCoreTypes()
		return err
	}

	c.coreTypes = coreTypes
	c.lastFetched = time.Now()
	c.loadError = nil
	return nil
}

// IsLoading returns whether core types are currently being fetched
func (c *APICache) IsLoading() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.isLoading
}

// GetLastFetched returns when core types were last successfully fetched
func (c *APICache) GetLastFetched() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastFetched
}

// getDefaultCoreTypes returns fallback core types if API fetch fails
func getDefaultCoreTypes() []models.CoreType {
	return []models.CoreType{
		{Code: "emerald", Name: "Emerald"},
		{Code: "sapphire", Name: "Sapphire"},
		{Code: "calcitev2", Name: "Calcite v2"},
		{Code: "onyx", Name: "Onyx"},
		{Code: "nickel", Name: "Nickel"},
	}
}

// getDefaultAnalysisCodes returns the list of common analysis codes
// TODO: This could be fetched from API in the future if endpoint exists
func getDefaultAnalysisCodes() []string {
	return []string{
		"powerflow",
		"openfoam",
		"ansys_fluent",
		"ansys_mechanical",
		"abaqus",
		"ls-dyna",
		"nastran",
		"user_included",
		"python",
	}
}
