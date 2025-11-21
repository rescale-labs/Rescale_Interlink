package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	nethttp "net/http"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/http"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/ratelimit"
)

// retryLogger implements the retryablehttp.LeveledLogger interface
type retryLogger struct{}

func (l *retryLogger) Error(msg string, keysAndValues ...interface{}) {
	log.Printf("🔄 [RETRY ERROR] %s %v", msg, keysAndValues)
}

func (l *retryLogger) Info(msg string, keysAndValues ...interface{}) {
	// Only log errors and warnings, not all info
}

func (l *retryLogger) Debug(msg string, keysAndValues ...interface{}) {
	// Only log errors and warnings
}

func (l *retryLogger) Warn(msg string, keysAndValues ...interface{}) {
	log.Printf("⚠️  [RETRY WARN] %s %v", msg, keysAndValues)
}

// apiMetrics tracks API usage statistics
type apiMetrics struct {
	sync.Mutex
	totalCalls    int64
	callsByPath   map[string]int64
	windowStart   time.Time
	callsInWindow int64
}

// Client represents the Rescale API client
type Client struct {
	httpClient       *nethttp.Client
	config           *config.Config
	baseURL          string
	apiKey           string
	userScopeLimiter *ratelimit.RateLimiter // All v3 endpoints (user scope: 7200/hour)
	jobSubmitLimiter *ratelimit.RateLimiter // POST /api/v2/jobs/{id}/submit/ only
	jobsUsageLimiter *ratelimit.RateLimiter // v2 job query endpoints (jobs-usage scope: 90000/hour)
	metrics          *apiMetrics            // API usage tracking
}

// NewClient creates a new API client
func NewClient(cfg *config.Config) (*Client, error) {
	// Configure HTTP client with proxy support
	httpClient, err := http.ConfigureHTTPClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to configure HTTP client: %w", err)
	}

	// Wrap with retry logic
	retryClient := retryablehttp.NewClient()
	retryClient.HTTPClient = httpClient
	retryClient.RetryMax = 10 // Increased from 5 to 10 (11 total attempts)
	retryClient.RetryWaitMin = 1 * time.Second
	retryClient.RetryWaitMax = 30 * time.Second
	retryClient.Logger = &retryLogger{} // Enable error/warning logging

	// Rate Limiter Setup
	// All v3 API endpoints share the "user" scope (7200/hour = 2 req/sec limit).
	// We use a single shared rate limiter instance for all v3 calls.
	// See internal/ratelimit/constants.go for detailed scope assignments.

	return &Client{
		httpClient:       retryClient.StandardClient(),
		config:           cfg,
		baseURL:          strings.TrimSuffix(cfg.APIBaseURL, "/"),
		apiKey:           cfg.APIKey,
		userScopeLimiter: ratelimit.NewUserScopeRateLimiter(),     // All v3 endpoints
		jobSubmitLimiter: ratelimit.NewJobSubmissionRateLimiter(), // v2 submit only
		jobsUsageLimiter: ratelimit.NewJobsUsageRateLimiter(),     // v2 job queries
		metrics: &apiMetrics{
			callsByPath: make(map[string]int64),
			windowStart: time.Now(),
		},
	}, nil
}

// GetConfig returns the configuration used by this API client
// This is needed by upload/download modules to configure their HTTP clients with proxy settings
func (c *Client) GetConfig() *config.Config {
	return c.config
}

// doRequest performs an HTTP request with authentication and rate limiting
func (c *Client) doRequest(ctx context.Context, method, path string, body interface{}) (*nethttp.Response, error) {
	// Select appropriate rate limiter based on endpoint scope
	limiter := c.userScopeLimiter // DEFAULT: all v3 endpoints use user scope (1.6 req/sec)

	if strings.Contains(path, "/api/v2/jobs/") {
		if strings.Contains(path, "/submit/") {
			// Job submission scope (0.139 req/sec)
			limiter = c.jobSubmitLimiter
		} else {
			// v2 job query endpoints use jobs-usage scope (20 req/sec)
			// Examples: GET /api/v2/jobs/{id}/files/
			limiter = c.jobsUsageLimiter
		}
	}
	// Note: All v3 endpoints (files, folders, jobs, credentials, etc.) share the same
	// user scope limiter on Rescale's side, so no need for separate routing.

	// Wait for rate limiter to allow request
	if err := limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limiter cancelled: %w", err)
	}

	// Track API call metrics
	c.metrics.Lock()
	c.metrics.totalCalls++
	c.metrics.callsByPath[path]++
	c.metrics.callsInWindow++

	// Log stats every 30 seconds
	if time.Since(c.metrics.windowStart) >= 30*time.Second {
		reqPerSec := float64(c.metrics.callsInWindow) / 30.0

		// Calculate percentages relative to both our target and Rescale's hard limit
		targetRate := 1.6 // Our target: 80% of hard limit (see constants.UserScopeRatePerSec)
		hardLimit := 2.0  // Rescale's hard limit: 7200/hour = 2/sec

		percentOfTarget := (reqPerSec / targetRate) * 100
		percentOfLimit := (reqPerSec / hardLimit) * 100

		// Show both percentages to help diagnose throttling issues
		log.Printf("📊 API usage: %.2f req/sec (%.0f%% of 1.6/sec target, %.0f%% of 2/sec limit), %d total calls",
			reqPerSec, percentOfTarget, percentOfLimit, c.metrics.totalCalls)

		c.metrics.callsInWindow = 0
		c.metrics.windowStart = time.Now()
	}
	c.metrics.Unlock()

	var reqBody io.Reader
	if body != nil {
		jsonData, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(jsonData)
	}

	url := c.baseURL + path
	req, err := nethttp.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Add headers
	req.Header.Set("Authorization", "Token "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Log detailed error information
		log.Printf("❌ API call failed: %s %s - Error: %v", method, path, err)

		// Check for specific error types by string matching
		errStr := err.Error()
		if strings.Contains(errStr, "timeout") {
			log.Printf("   └─ Timeout error (client timeout or network issue)")
		}
		if strings.Contains(errStr, "TLS handshake timeout") {
			log.Printf("   └─ TLS handshake timeout (connection pool may be exhausted)")
		}

		return nil, fmt.Errorf("request failed: %w", err)
	}

	// Check for rate limit (429 Too Many Requests) response
	if resp.StatusCode == 429 {
		// Determine which scope/endpoint is being throttled
		// v0.7.3: Most v3 endpoints belong to "user" scope (7200/hour = 2 req/sec)
		scope := "unknown"
		if strings.HasPrefix(path, "/api/v3/") {
			// v3 endpoints use "user" scope (unless explicitly listed in throttle table)
			scope = "user (v3 default, 7200/hour = 2/sec)"
		} else if strings.Contains(path, "/api/v2/files") {
			scope = "file-access (v2, 90000/hour)"
		} else if strings.Contains(path, "/api/v2/credentials") {
			scope = "credential-access (v2, 90000/hour)"
		} else if strings.Contains(path, "/submit/") {
			scope = "job-submission (1000/hour)"
		} else if strings.Contains(path, "/jobs") || strings.Contains(path, "/desktops") {
			scope = "jobs-usage (v2, 90000/hour)"
		}

		// Log throttle event with details
		log.Printf("⚠️  THROTTLED: %s %s - Rate limit exceeded on '%s' scope", method, path, scope)

		// Check for Retry-After header
		if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
			log.Printf("   └─ Retry-After: %s seconds", retryAfter)
		}

		// Check for rate limit headers
		if remaining := resp.Header.Get("X-RateLimit-Remaining"); remaining != "" {
			log.Printf("   └─ X-RateLimit-Remaining: %s", remaining)
		}
		if limit := resp.Header.Get("X-RateLimit-Limit"); limit != "" {
			log.Printf("   └─ X-RateLimit-Limit: %s", limit)
		}
	}

	return resp, nil
}

// GetUserProfile gets the current user's profile
func (c *Client) GetUserProfile(ctx context.Context) (*models.UserProfile, error) {
	resp, err := c.doRequest(ctx, "GET", "/api/v3/users/me/", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get user profile failed: status %d: %s", resp.StatusCode, string(body))
	}

	var profile models.UserProfile
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		return nil, fmt.Errorf("failed to decode user profile: %w", err)
	}

	return &profile, nil
}

// GetStorageCredentials gets temporary credentials for storage
// If fileInfo is provided, gets credentials for that file's specific storage (allows cross-storage downloads)
// If fileInfo is nil, gets credentials for user's default storage
func (c *Client) GetStorageCredentials(ctx context.Context, fileInfo *models.CloudFile) (*models.S3Credentials, *models.AzureCredentials, error) {
	var requestBody interface{}

	// If file info provided, request credentials for that specific storage
	if fileInfo != nil && fileInfo.Storage != nil && fileInfo.PathParts != nil {
		requestBody = models.CredentialsRequest{
			Storage: models.CredentialsStorageRequest{
				ID:          fileInfo.Storage.ID,
				StorageType: fileInfo.Storage.StorageType,
			},
			Paths: []models.CredentialsPathPartsRequest{
				{PathParts: *fileInfo.PathParts},
			},
		}
	}
	// else: nil body requests user's default storage credentials

	// POST to /api/v3/credentials/
	resp, err := c.doRequest(ctx, "POST", "/api/v3/credentials/", requestBody)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusOK && resp.StatusCode != nethttp.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, nil, fmt.Errorf("get storage credentials failed: status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response to determine storage type
	var rawResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&rawResp); err != nil {
		return nil, nil, fmt.Errorf("failed to decode credentials: %w", err)
	}

	storageType, _ := rawResp["storageType"].(string)

	// Re-encode to handle different credential types
	credData, _ := json.Marshal(rawResp)

	switch storageType {
	case "S3Storage":
		var s3Creds models.S3Credentials
		if err := json.Unmarshal(credData, &s3Creds); err != nil {
			return nil, nil, fmt.Errorf("failed to parse S3 credentials: %w", err)
		}
		return &s3Creds, nil, nil

	case "AzureStorage":
		var azureCreds models.AzureCredentials
		if err := json.Unmarshal(credData, &azureCreds); err != nil {
			return nil, nil, fmt.Errorf("failed to parse Azure credentials: %w", err)
		}
		return nil, &azureCreds, nil

	default:
		return nil, nil, fmt.Errorf("unknown storage type: %s", storageType)
	}
}

// GetRootFolders gets the user's root folders
func (c *Client) GetRootFolders(ctx context.Context) (*models.RootFolders, error) {
	resp, err := c.doRequest(ctx, "GET", "/api/v3/users/me/folders/", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get root folders failed: status %d: %s", resp.StatusCode, string(body))
	}

	var folders models.RootFolders
	if err := json.NewDecoder(resp.Body).Decode(&folders); err != nil {
		return nil, fmt.Errorf("failed to decode root folders: %w", err)
	}

	return &folders, nil
}

// RegisterFile registers an uploaded file with Rescale
func (c *Client) RegisterFile(ctx context.Context, fileReq *models.CloudFileRequest) (*models.CloudFile, error) {
	resp, err := c.doRequest(ctx, "POST", "/api/v3/files/", fileReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Check for success
	if resp.StatusCode == nethttp.StatusCreated || resp.StatusCode == nethttp.StatusOK {
		var file models.CloudFile
		if err := json.NewDecoder(resp.Body).Decode(&file); err != nil {
			return nil, fmt.Errorf("failed to decode file response: %w", err)
		}
		return &file, nil
	}

	// Handle error responses
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Check for "file already exists" conflict (HTTP 409 Conflict or similar)
	if resp.StatusCode == nethttp.StatusConflict || resp.StatusCode == nethttp.StatusBadRequest {
		// Check if error message indicates duplicate file
		bodyLower := strings.ToLower(bodyStr)
		if strings.Contains(bodyLower, "already exists") ||
			strings.Contains(bodyLower, "duplicate") ||
			strings.Contains(bodyLower, "conflict") {
			// Wrap with ErrFileAlreadyExists for easy detection
			return nil, fmt.Errorf("%w: %s", ErrFileAlreadyExists, bodyStr)
		}
	}

	// Other error
	return nil, fmt.Errorf("register file failed: status %d: %s", resp.StatusCode, bodyStr)
}

// GetFileInfo retrieves file information by ID (v3 API)
func (c *Client) GetFileInfo(ctx context.Context, fileID string) (*models.CloudFile, error) {
	path := fmt.Sprintf("/api/v3/files/%s/", fileID)

	resp, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get file info failed: status %d: %s", resp.StatusCode, string(body))
	}

	var file models.CloudFile
	if err := json.NewDecoder(resp.Body).Decode(&file); err != nil {
		return nil, fmt.Errorf("failed to decode file info: %w", err)
	}

	return &file, nil
}

// CreateJob creates a new job (v3 API)
func (c *Client) CreateJob(ctx context.Context, jobReq models.JobRequest) (*models.JobResponse, error) {
	resp, err := c.doRequest(ctx, "POST", "/api/v3/jobs/", jobReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusCreated && resp.StatusCode != nethttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create job failed: status %d: %s", resp.StatusCode, string(body))
	}

	var job models.JobResponse
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return nil, fmt.Errorf("failed to decode job response: %w", err)
	}

	return &job, nil
}

// SubmitJob submits a job for execution (v2 API)
func (c *Client) SubmitJob(ctx context.Context, jobID string) error {
	// v2 API endpoint for submission
	path := fmt.Sprintf("/api/v2/jobs/%s/submit/", jobID)

	resp, err := c.doRequest(ctx, "POST", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusOK && resp.StatusCode != nethttp.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("submit job failed: status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// GetJob retrieves job details
func (c *Client) GetJob(ctx context.Context, jobID string) (*models.JobResponse, error) {
	path := fmt.Sprintf("/api/v3/jobs/%s/", jobID)

	resp, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get job failed: status %d: %s", resp.StatusCode, string(body))
	}

	var job models.JobResponse
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return nil, fmt.Errorf("failed to decode job response: %w", err)
	}

	return &job, nil
}

// ListJobs lists all jobs (with pagination)
func (c *Client) ListJobs(ctx context.Context) ([]models.JobResponse, error) {
	var allJobs []models.JobResponse
	nextURL := "/api/v3/jobs/"

	for nextURL != "" {
		resp, err := c.doRequest(ctx, "GET", nextURL, nil)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != nethttp.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("list jobs failed: status %d: %s", resp.StatusCode, string(body))
		}

		var result struct {
			Count   int                  `json:"count"`
			Next    *string              `json:"next"`
			Results []models.JobResponse `json:"results"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, fmt.Errorf("failed to decode jobs response: %w", err)
		}

		allJobs = append(allJobs, result.Results...)

		if result.Next != nil && *result.Next != "" {
			// Extract path from full URL
			nextURL = strings.TrimPrefix(*result.Next, c.baseURL)
		} else {
			nextURL = ""
		}
	}

	return allJobs, nil
}

// GetCoreTypes retrieves available hardware core types from the Rescale API.
// This is used for validation to ensure jobs use valid core types.
// The endpoint returns all core types, both active and inactive.
// Handles pagination to retrieve all core types (183+ as of Nov 2025).
// Added in v1.0.0 (October 7, 2025) for core type validation feature.
// Fixed in v1.0.8 (November 6, 2025) to handle pagination correctly.
func (c *Client) GetCoreTypes(ctx context.Context) ([]models.CoreType, error) {
	var allCoreTypes []models.CoreType
	nextURL := "/api/v3/coretypes/"

	for nextURL != "" {
		resp, err := c.doRequest(ctx, "GET", nextURL, nil)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != nethttp.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("get core types failed: status %d: %s", resp.StatusCode, string(body))
		}

		var result struct {
			Count   int               `json:"count"`
			Next    *string           `json:"next"`
			Results []models.CoreType `json:"results"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, fmt.Errorf("failed to decode core types response: %w", err)
		}

		allCoreTypes = append(allCoreTypes, result.Results...)

		if result.Next != nil && *result.Next != "" {
			// Extract path from full URL
			nextURL = strings.TrimPrefix(*result.Next, c.baseURL)
		} else {
			nextURL = ""
		}
	}

	return allCoreTypes, nil
}

// GetAnalyses retrieves all available software analyses from Rescale.
// Implements pagination to fetch all results.
func (c *Client) GetAnalyses(ctx context.Context) ([]models.Analysis, error) {
	var allAnalyses []models.Analysis
	nextURL := "/api/v3/analyses/"

	for nextURL != "" {
		resp, err := c.doRequest(ctx, "GET", nextURL, nil)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != nethttp.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("get analyses failed: status %d: %s", resp.StatusCode, string(body))
		}

		var result struct {
			Count   int               `json:"count"`
			Next    *string           `json:"next"`
			Results []models.Analysis `json:"results"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, fmt.Errorf("failed to decode analyses response: %w", err)
		}

		allAnalyses = append(allAnalyses, result.Results...)

		if result.Next != nil && *result.Next != "" {
			// Extract path from full URL
			nextURL = strings.TrimPrefix(*result.Next, c.baseURL)
		} else {
			nextURL = ""
		}
	}

	return allAnalyses, nil
}

// ListFiles retrieves a list of files from the user's library
func (c *Client) ListFiles(ctx context.Context, limit int) ([]interface{}, error) {
	if limit <= 0 {
		limit = 20
	}

	path := fmt.Sprintf("/api/v3/files/?limit=%d", limit)

	resp, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Results []interface{} `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return result.Results, nil
}

// DeleteFile deletes a file from the user's library
func (c *Client) DeleteFile(ctx context.Context, fileID string) error {
	path := fmt.Sprintf("/api/v3/files/%s/", fileID)

	resp, err := c.doRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusNoContent && resp.StatusCode != nethttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// FolderContents represents the contents of a folder
type FolderContents struct {
	Folders []FolderInfo
	Files   []FileInfo
}

// FolderInfo represents basic folder information
type FolderInfo struct {
	ID   string
	Name string
}

// FileInfo represents basic file information
type FileInfo struct {
	ID            string
	Name          string
	DecryptedSize int64
}

// CreateFolder creates a new folder
func (c *Client) CreateFolder(ctx context.Context, name, parentID string) (string, error) {
	requestBody := map[string]interface{}{
		"name": name,
	}

	// Use the folders API endpoint, not files
	path := fmt.Sprintf("/api/v3/folders/%s/", parentID)
	resp, err := c.doRequest(ctx, "POST", path, requestBody)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusCreated && resp.StatusCode != nethttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		ID string `json:"id"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	return result.ID, nil
}

// ListFolderContents lists the contents of a folder
func (c *Client) ListFolderContents(ctx context.Context, folderID string) (*FolderContents, error) {
	// Use folders API to get contents (both files and subfolders)
	path := fmt.Sprintf("/api/v3/folders/%s/contents/", folderID)

	resp, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Folders API returns items with 'type' and 'item' fields
	var result struct {
		Results []map[string]interface{} `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	contents := &FolderContents{
		Folders: make([]FolderInfo, 0),
		Files:   make([]FileInfo, 0),
	}

	for _, entry := range result.Results {
		itemType, _ := entry["type"].(string)
		itemData, ok := entry["item"].(map[string]interface{})
		if !ok {
			continue
		}

		if itemType == "folder" {
			if id, ok := itemData["id"].(string); ok {
				if name, ok := itemData["name"].(string); ok {
					contents.Folders = append(contents.Folders, FolderInfo{
						ID:   id,
						Name: name,
					})
				}
			}
		} else if itemType == "file" {
			id, _ := itemData["id"].(string)
			name, _ := itemData["name"].(string)
			size := int64(0)
			if s, ok := itemData["decryptedSize"].(float64); ok {
				size = int64(s)
			}
			contents.Files = append(contents.Files, FileInfo{
				ID:            id,
				Name:          name,
				DecryptedSize: size,
			})
		}
	}

	return contents, nil
}

// MoveFileToFolder moves a file to a specific folder
func (c *Client) MoveFileToFolder(ctx context.Context, fileID, folderID string) error {
	path := fmt.Sprintf("/api/v3/files/%s/", fileID)

	requestBody := map[string]interface{}{
		"currentFolderId": folderID,
	}

	resp, err := c.doRequest(ctx, "PATCH", path, requestBody)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// DeleteFolder deletes a folder
func (c *Client) DeleteFolder(ctx context.Context, folderID string) error {
	path := fmt.Sprintf("/api/v3/folders/%s/", folderID)

	resp, err := c.doRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusNoContent && resp.StatusCode != nethttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// StopJob stops a running job
func (c *Client) StopJob(ctx context.Context, jobID string) error {
	path := fmt.Sprintf("/api/v3/jobs/%s/stop/", jobID)

	resp, err := c.doRequest(ctx, "POST", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusOK && resp.StatusCode != nethttp.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("stop job failed: status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// GetJobStatuses retrieves detailed status history for a job
func (c *Client) GetJobStatuses(ctx context.Context, jobID string) ([]models.JobStatusEntry, error) {
	path := fmt.Sprintf("/api/v3/jobs/%s/statuses/", jobID)

	resp, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get job statuses failed: status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Results []models.JobStatusEntry `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode job statuses response: %w", err)
	}

	return result.Results, nil
}

// ListJobFiles lists output files for a job (with pagination)
// 2025-11-20: Switched from v3 to v2 endpoint for 12.5x faster rate limit
// v2 uses jobs-usage scope (90000/hour = 25 req/sec) vs v3 user scope (7200/hour = 2 req/sec)
func (c *Client) ListJobFiles(ctx context.Context, jobID string) ([]models.JobFile, error) {
	var allFiles []models.JobFile
	nextURL := fmt.Sprintf("/api/v2/jobs/%s/files/", jobID)

	for nextURL != "" {
		resp, err := c.doRequest(ctx, "GET", nextURL, nil)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != nethttp.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("list job files failed: status %d: %s", resp.StatusCode, string(body))
		}

		var result struct {
			Count   int              `json:"count"`
			Next    *string          `json:"next"`
			Results []models.JobFile `json:"results"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, fmt.Errorf("failed to decode job files response: %w", err)
		}

		allFiles = append(allFiles, result.Results...)

		if result.Next != nil && *result.Next != "" {
			// Extract path from full URL
			nextURL = strings.TrimPrefix(*result.Next, c.baseURL)
		} else {
			nextURL = ""
		}
	}

	return allFiles, nil
}

// DeleteJob deletes a job
func (c *Client) DeleteJob(ctx context.Context, jobID string) error {
	path := fmt.Sprintf("/api/v3/jobs/%s/", jobID)

	resp, err := c.doRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusNoContent && resp.StatusCode != nethttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete job failed: status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}
