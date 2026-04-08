package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	nethttp "net/http"
	"strings"

	"github.com/rescale/rescale-int/internal/constants"
)

// paginateRaw fetches all pages of a paginated v3 API endpoint and returns
// the combined results as raw JSON messages, preserving all fields from the API
// without deserializing into typed structs.
func (c *Client) paginateRaw(ctx context.Context, startURL string) ([]json.RawMessage, error) {
	var all []json.RawMessage
	nextURL := startURL
	pageCount := 0

	for nextURL != "" {
		pageCount++
		if pageCount > constants.MaxPaginationPages {
			log.Printf("Warning: Pagination limit reached after %d pages (%d raw results fetched)", pageCount-1, len(all))
			break
		}

		resp, err := c.doRequest(ctx, "GET", nextURL, nil)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != nethttp.StatusOK {
			body := readResponseBody(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("raw paginate failed: status %d: %s", resp.StatusCode, body)
		}

		var result struct {
			Count   int               `json:"count"`
			Next    *string           `json:"next"`
			Results []json.RawMessage `json:"results"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("failed to decode paginated response: %w", err)
		}
		resp.Body.Close()

		all = append(all, result.Results...)

		if result.Next != nil && *result.Next != "" {
			nextURL = strings.TrimPrefix(*result.Next, c.baseURL)
		} else {
			nextURL = ""
		}
	}

	return all, nil
}

// GetCoreTypesRaw returns core types as raw JSON, preserving all API fields.
// Uses v2 endpoint which includes price/lowPriorityPrice/walltimeRequired fields
// absent from v3. Only called from compat-mode list-info.
func (c *Client) GetCoreTypesRaw(ctx context.Context, includeInactive bool) ([]json.RawMessage, error) {
	url := "/api/v2/coretypes/"
	if !includeInactive {
		url = "/api/v2/coretypes/?isActive=true"
	}
	return c.paginateRaw(ctx, url)
}

// GetAnalysesRaw returns analyses as raw JSON, preserving all API fields.
// Uses v2 endpoint which includes supportDesks field absent from v3.
// Only called from compat-mode list-info.
func (c *Client) GetAnalysesRaw(ctx context.Context) ([]json.RawMessage, error) {
	return c.paginateRaw(ctx, "/api/v2/analyses/")
}

// GetJobStatusesRaw returns job statuses as raw JSON, preserving fields like
// notify and preventDuplicates that aren't in the typed model.
func (c *Client) GetJobStatusesRaw(ctx context.Context, jobID string) ([]json.RawMessage, error) {
	path := fmt.Sprintf("/api/v3/jobs/%s/statuses/", jobID)
	return c.paginateRaw(ctx, path)
}

// GetJobConnectionDetailsRaw returns the v2 connection_details response as raw JSON.
func (c *Client) GetJobConnectionDetailsRaw(ctx context.Context, jobID string) (json.RawMessage, error) {
	path := fmt.Sprintf("/api/v2/jobs/%s/connection_details/", jobID)

	resp, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusOK {
		body := readResponseBody(resp.Body)
		return nil, fmt.Errorf("get connection details failed: status %d: %s", resp.StatusCode, body)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read connection details response: %w", err)
	}

	return json.RawMessage(data), nil
}

// GetJobRaw returns a job as raw JSON, preserving all API fields.
func (c *Client) GetJobRaw(ctx context.Context, jobID string) (json.RawMessage, error) {
	path := fmt.Sprintf("/api/v3/jobs/%s/", jobID)

	resp, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusOK {
		body := readResponseBody(resp.Body)
		return nil, fmt.Errorf("get job failed: status %d: %s", resp.StatusCode, body)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read job response: %w", err)
	}

	return json.RawMessage(data), nil
}

// GetFileInfoRaw returns file metadata as raw JSON, preserving all API fields.
func (c *Client) GetFileInfoRaw(ctx context.Context, fileID string) (json.RawMessage, error) {
	path := fmt.Sprintf("/api/v3/files/%s/", fileID)

	resp, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusOK {
		body := readResponseBody(resp.Body)
		return nil, fmt.Errorf("get file info failed: status %d: %s", resp.StatusCode, body)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read file info response: %w", err)
	}

	return json.RawMessage(data), nil
}
