// Package api provides automation-related API methods.
// Added in v3.6.1.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/rescale/rescale-int/internal/models"
)

// paginatedAutomationsResponse handles paginated API responses.
type paginatedAutomationsResponse struct {
	Count   int                 `json:"count"`
	Results []models.Automation `json:"results"`
}

// ListAutomations returns all available automations for the user's account.
// Automations are pre-configured scripts that can run before/after job execution.
func (c *Client) ListAutomations(ctx context.Context) ([]models.Automation, error) {
	resp, err := c.doRequest(ctx, "GET", "/api/v3/automations/", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list automations: %w", err)
	}
	defer resp.Body.Close()

	// Read body to allow trying multiple decode formats
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read automations response: %w", err)
	}

	// Try decoding as array first (some API versions return array directly)
	var automations []models.Automation
	if err := json.Unmarshal(body, &automations); err == nil {
		return automations, nil
	}

	// Try paginated response format
	var paginated paginatedAutomationsResponse
	if err := json.Unmarshal(body, &paginated); err != nil {
		return nil, fmt.Errorf("failed to decode automations response: %w", err)
	}

	return paginated.Results, nil
}

// GetAutomation returns details for a specific automation by ID.
func (c *Client) GetAutomation(ctx context.Context, automationID string) (*models.Automation, error) {
	automations, err := c.ListAutomations(ctx)
	if err != nil {
		return nil, err
	}

	for _, a := range automations {
		if a.ID == automationID {
			return &a, nil
		}
	}

	return nil, fmt.Errorf("automation not found: %s", automationID)
}
