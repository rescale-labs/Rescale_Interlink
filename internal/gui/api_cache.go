package gui

import (
	"sync"

	"github.com/rescale/rescale-int/internal/models"
)

// APICache manages cached data from Rescale API to avoid repeated slow fetches
type APICache struct {
	coreTypes     []models.CoreType
	analysisCodes []string
	automations   []models.Automation // v3.6.1
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

// GetAutomations returns cached automations (v3.6.1)
func (c *APICache) GetAutomations() []models.Automation {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.automations
}

// SetAutomations sets the cached automations (v3.6.1)
func (c *APICache) SetAutomations(automations []models.Automation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.automations = automations
}

// getDefaultCoreTypes returns fallback core types
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
