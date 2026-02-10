package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	nethttp "net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/http"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/ratelimit"
)

// retryLogger implements the retryablehttp.LeveledLogger interface
// In GUI mode, we suppress most retry logs to keep the console clean
type retryLogger struct{}

func (l *retryLogger) Error(msg string, keysAndValues ...interface{}) {
	// Only log retry errors if RESCALE_DEBUG is set (suppress in GUI mode)
	// Context canceled errors are expected during shutdown, don't log them
	errStr := fmt.Sprintf("%v", keysAndValues)
	if strings.Contains(errStr, "context canceled") {
		return // Expected during shutdown, don't spam console
	}
	if os.Getenv("RESCALE_DEBUG") != "" {
		log.Printf("🔄 [RETRY ERROR] %s %v", msg, keysAndValues)
	}
}

func (l *retryLogger) Info(msg string, keysAndValues ...interface{}) {
	// Only log in debug mode
}

func (l *retryLogger) Debug(msg string, keysAndValues ...interface{}) {
	// Only log in debug mode
}

func (l *retryLogger) Warn(msg string, keysAndValues ...interface{}) {
	// Only log if RESCALE_DEBUG is set
	if os.Getenv("RESCALE_DEBUG") != "" {
		log.Printf("⚠️  [RETRY WARN] %s %v", msg, keysAndValues)
	}
}

// getStringField safely extracts a string value from a map[string]interface{}
// Returns empty string and logs a warning if the key is missing or not a string
func getStringField(m map[string]interface{}, key string, context string) string {
	if m == nil {
		return ""
	}
	val, exists := m[key]
	if !exists {
		// Key missing - this is often expected for optional fields, no warning needed
		return ""
	}
	str, ok := val.(string)
	if !ok {
		// Key exists but is wrong type - log warning
		log.Printf("Warning: expected string for %s.%s, got %T", context, key, val)
		return ""
	}
	return str
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
	if cfg.APIBaseURL == "" {
		return nil, fmt.Errorf("API base URL is empty — check configuration (config.csv api_base_url)")
	}

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

// readResponseBody reads and returns the response body content as a string.
// If reading fails, returns a placeholder message indicating the failure.
// This ensures error messages are always informative even when body reading fails.
func readResponseBody(body io.ReadCloser) string {
	data, err := io.ReadAll(body)
	if err != nil {
		return fmt.Sprintf("(failed to read response body: %v)", err)
	}
	return string(data)
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
	// Note: Go's http.Transport automatically handles Accept-Encoding: gzip
	// and transparently decompresses responses. Do NOT set this header manually
	// as it disables automatic decompression (causing JSON decode errors).

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Check for specific error types by string matching
		errStr := err.Error()

		// Don't log context canceled errors - they're expected during shutdown
		// Only log in debug mode (RESCALE_DEBUG=1) to keep GUI console clean
		if !strings.Contains(errStr, "context canceled") && os.Getenv("RESCALE_DEBUG") != "" {
			log.Printf("❌ API call failed: %s %s - Error: %v", method, path, err)
			if strings.Contains(errStr, "timeout") {
				log.Printf("   └─ Timeout error (client timeout or network issue)")
			}
			if strings.Contains(errStr, "TLS handshake timeout") {
				log.Printf("   └─ TLS handshake timeout (connection pool may be exhausted)")
			}
		}

		return nil, fmt.Errorf("request failed: %w", err)
	}

	// Check for rate limit (429 Too Many Requests) response
	if resp.StatusCode == 429 {
		// Determine which scope/endpoint is being throttled
		// Most v3 endpoints belong to "user" scope (7200/hour = 2 req/sec)
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
		body := readResponseBody(resp.Body)
		return nil, fmt.Errorf("get user profile failed: status %d: %s", resp.StatusCode, body)
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

	// If file info provided, request credentials for that specific storage.
	// This enables cross-bucket and cross-storage downloads.
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
		body := readResponseBody(resp.Body)
		return nil, nil, fmt.Errorf("get storage credentials failed: status %d: %s", resp.StatusCode, body)
	}

	// Parse response to determine storage type
	var rawResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&rawResp); err != nil {
		return nil, nil, fmt.Errorf("failed to decode credentials: %w", err)
	}

	storageType := getStringField(rawResp, "storageType", "credentials")

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
		body := readResponseBody(resp.Body)
		return nil, fmt.Errorf("get root folders failed: status %d: %s", resp.StatusCode, body)
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
	bodyStr := readResponseBody(resp.Body)

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
		body := readResponseBody(resp.Body)
		return nil, fmt.Errorf("get file info failed: status %d: %s", resp.StatusCode, body)
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
		body := readResponseBody(resp.Body)
		return nil, fmt.Errorf("create job failed: status %d: %s", resp.StatusCode, body)
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
		body := readResponseBody(resp.Body)
		return fmt.Errorf("submit job failed: status %d: %s", resp.StatusCode, body)
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
		body := readResponseBody(resp.Body)
		return nil, fmt.Errorf("get job failed: status %d: %s", resp.StatusCode, body)
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
	pageCount := 0

	for nextURL != "" {
		// Pagination safety: prevent infinite loops from malformed API responses
		pageCount++
		if pageCount > constants.MaxPaginationPages {
			log.Printf("Warning: Pagination limit reached after %d pages (%d jobs fetched)", pageCount-1, len(allJobs))
			break
		}
		if pageCount == constants.PaginationWarningThreshold {
			log.Printf("Warning: Approaching pagination limit (page %d of %d)", pageCount, constants.MaxPaginationPages)
		}

		resp, err := c.doRequest(ctx, "GET", nextURL, nil)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != nethttp.StatusOK {
			body := readResponseBody(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("list jobs failed: status %d: %s", resp.StatusCode, body)
		}

		var result struct {
			Count   int                  `json:"count"`
			Next    *string              `json:"next"`
			Results []models.JobResponse `json:"results"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("failed to decode jobs response: %w", err)
		}

		// Close body immediately after reading - don't use defer in loop
		resp.Body.Close()

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

// ListJobsWithCutoff lists jobs ordered by dateInserted (newest first) and stops
// when hitting jobs older than the cutoff. This is more efficient for daemon scans
// that only care about recent jobs within a lookback window.
// v4.3.4: Added for daemon scan optimization
func (c *Client) ListJobsWithCutoff(ctx context.Context, cutoff time.Time) ([]models.JobResponse, error) {
	var allJobs []models.JobResponse
	// Order by dateInserted descending (newest first) for early termination
	nextURL := "/api/v3/jobs/?ordering=-dateInserted"
	pageCount := 0

	log.Printf("Daemon scan: Fetching jobs (cutoff: %s)", cutoff.Format("2006-01-02"))

	for nextURL != "" {
		pageCount++
		if pageCount > constants.MaxPaginationPages {
			log.Printf("Warning: Pagination limit reached after %d pages (%d jobs fetched)", pageCount-1, len(allJobs))
			break
		}

		resp, err := c.doRequest(ctx, "GET", nextURL, nil)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != nethttp.StatusOK {
			body := readResponseBody(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("list jobs failed: status %d: %s", resp.StatusCode, body)
		}

		var result struct {
			Count   int                  `json:"count"`
			Next    *string              `json:"next"`
			Results []models.JobResponse `json:"results"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("failed to decode jobs response: %w", err)
		}
		resp.Body.Close()

		// Check if we should stop early (all jobs on this page are older than cutoff)
		// Safety: We only stop if ALL remaining jobs on this page are older than cutoff
		// This ensures we don't miss any jobs that might be out of order
		stopEarly := false
		oldJobsOnPage := 0
		for _, job := range result.Results {
			// Parse job's dateInserted (CreatedAt maps to dateInserted)
			if job.CreatedAt != "" {
				if createdAt, err := time.Parse(time.RFC3339, job.CreatedAt); err == nil {
					if createdAt.Before(cutoff) {
						// This job is older than cutoff
						oldJobsOnPage++
						// Safety: Still add the job - let FindCompletedJobs do the filtering
						// This ensures we don't accidentally skip a job
					}
				}
			}
			allJobs = append(allJobs, job)
		}

		// Only stop early if ALL jobs on this page are older than cutoff
		// This is conservative - we'd rather fetch a few extra jobs than miss any
		if oldJobsOnPage == len(result.Results) && len(result.Results) > 0 {
			stopEarly = true
			log.Printf("Daemon scan: Stopped at page %d - all jobs on page older than cutoff (%d jobs collected)", pageCount, len(allJobs))
		}

		if stopEarly {
			break
		}

		if result.Next != nil && *result.Next != "" {
			nextURL = strings.TrimPrefix(*result.Next, c.baseURL)
		} else {
			nextURL = ""
		}
	}

	return allJobs, nil
}

// GetCoreTypes retrieves available hardware core types from the Rescale API.
// This is used for validation to ensure jobs use valid core types.
// By default (includeInactive=false), only returns active core types.
// Set includeInactive=true to include deprecated/inactive types (for validation).
// Handles pagination to retrieve all core types.
func (c *Client) GetCoreTypes(ctx context.Context, includeInactive bool) ([]models.CoreType, error) {
	var allCoreTypes []models.CoreType
	nextURL := "/api/v3/coretypes/"
	if !includeInactive {
		nextURL = "/api/v3/coretypes/?isActive=true"
	}
	pageCount := 0

	for nextURL != "" {
		// Pagination safety: prevent infinite loops from malformed API responses
		pageCount++
		if pageCount > constants.MaxPaginationPages {
			log.Printf("Warning: Pagination limit reached after %d pages (%d core types fetched)", pageCount-1, len(allCoreTypes))
			break
		}

		resp, err := c.doRequest(ctx, "GET", nextURL, nil)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != nethttp.StatusOK {
			body := readResponseBody(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("get core types failed: status %d: %s", resp.StatusCode, body)
		}

		var result struct {
			Count   int               `json:"count"`
			Next    *string           `json:"next"`
			Results []models.CoreType `json:"results"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("failed to decode core types response: %w", err)
		}

		// Close body immediately after reading - don't use defer in loop
		resp.Body.Close()

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
	pageCount := 0

	for nextURL != "" {
		pageCount++
		if pageCount > constants.MaxPaginationPages {
			log.Printf("Warning: Pagination limit reached after %d pages (%d analyses fetched)", pageCount-1, len(allAnalyses))
			break
		}
		if pageCount == constants.PaginationWarningThreshold {
			log.Printf("Warning: Approaching pagination limit (page %d of %d)", pageCount, constants.MaxPaginationPages)
		}

		resp, err := c.doRequest(ctx, "GET", nextURL, nil)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != nethttp.StatusOK {
			body := readResponseBody(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("get analyses failed: status %d: %s", resp.StatusCode, body)
		}

		var result struct {
			Count   int               `json:"count"`
			Next    *string           `json:"next"`
			Results []models.Analysis `json:"results"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("failed to decode analyses response: %w", err)
		}

		// Close body immediately after reading - don't use defer in loop
		resp.Body.Close()

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
		body := readResponseBody(resp.Body)
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Results []interface{} `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return result.Results, nil
}

// LegacyFilesPage represents a page of legacy files listing.
// v3.6.3: Used for flat file list view (Legacy mode in File Browser).
type LegacyFilesPage struct {
	Files   []FileInfo
	NextURL string // URL to fetch next page (empty if no more)
	HasMore bool   // True if there are more pages
}

// ListFilesPage retrieves a page of files from the user's library (flat list).
// v3.6.3: Used for Legacy mode in File Browser.
// pageURL: pass "" for first page, or NextURL from previous response.
// Orders by most recent first (-dateUploaded) to show newest files at top.
// v4.0.3: Added pageSize parameter - pass 0 for default (25), or specify items per page.
func (c *Client) ListFilesPage(ctx context.Context, pageURL string, pageSize int) (*LegacyFilesPage, error) {
	url := pageURL
	if url == "" {
		// v4.0.3: Use specified page size or default to 25
		ps := 25
		if pageSize > 0 {
			ps = pageSize
		}
		url = fmt.Sprintf("/api/v3/files/?page_size=%d&ordering=-dateUploaded", ps)
	}

	resp, err := c.doRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusOK {
		body := readResponseBody(resp.Body)
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Count    int    `json:"count"`
		Next     string `json:"next"`
		Previous string `json:"previous"`
		Results  []struct {
			ID            string    `json:"id"`
			Name          string    `json:"name"`
			DecryptedSize int64     `json:"decryptedSize"`
			DateUploaded  time.Time `json:"dateUploaded"`
			IsUploaded    bool      `json:"isUploaded"`
			IsDeleted     bool      `json:"isDeleted"`
		} `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	page := &LegacyFilesPage{
		Files:   make([]FileInfo, 0, len(result.Results)),
		NextURL: result.Next,
		HasMore: result.Next != "",
	}

	// Filter to only include uploaded, non-deleted files
	for _, f := range result.Results {
		if f.IsUploaded && !f.IsDeleted {
			page.Files = append(page.Files, FileInfo{
				ID:            f.ID,
				Name:          f.Name,
				DecryptedSize: f.DecryptedSize,
				DateUploaded:  f.DateUploaded,
			})
		}
	}

	return page, nil
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
		body := readResponseBody(resp.Body)
		return fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, body)
	}

	return nil
}

// FolderContents represents the contents of a folder.
// Returns single page with pagination metadata for server-side pagination.
type FolderContents struct {
	Folders []FolderInfo
	Files   []FileInfo
	// Pagination metadata for server-side pagination
	NextURL  string // URL to fetch next page (empty if no more pages)
	PrevURL  string // URL to fetch previous page (empty if on first page)
	PageSize int    // Number of items per page (from API)
	HasMore  bool   // True if there are more pages after this one
}

// FolderInfo represents basic folder information
type FolderInfo struct {
	ID           string
	Name         string
	DateUploaded time.Time
}

// FileInfo represents basic file information
type FileInfo struct {
	ID            string
	Name          string
	DecryptedSize int64
	DateUploaded  time.Time
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
		body := readResponseBody(resp.Body)
		return "", fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, body)
	}

	var result struct {
		ID string `json:"id"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	return result.ID, nil
}

// ListFolderContents fetches the first page of folder contents.
// Returns single page with pagination metadata for fast initial load.
// Use the returned NextURL/PrevURL to navigate between pages.
// For pageURL, pass "" for first page or the NextURL/PrevURL from a previous call.
func (c *Client) ListFolderContents(ctx context.Context, folderID string) (*FolderContents, error) {
	return c.ListFolderContentsPage(ctx, folderID, "", 0)
}

// ListFolderContentsPage fetches a specific page of folder contents.
// Pass pageURL="" for the first page, or use NextURL/PrevURL from previous response.
// v4.0.3: Added pageSize parameter - pass 0 for API default, or specify items per page.
func (c *Client) ListFolderContentsPage(ctx context.Context, folderID, pageURL string, pageSize int) (*FolderContents, error) {
	contents := &FolderContents{
		Folders: make([]FolderInfo, 0),
		Files:   make([]FileInfo, 0),
	}

	// Determine URL for this request
	url := pageURL
	if url == "" {
		// v4.0.3: Include page_size in first page request if specified
		if pageSize > 0 {
			url = fmt.Sprintf("/api/v3/folders/%s/contents/?page_size=%d", folderID, pageSize)
		} else {
			url = fmt.Sprintf("/api/v3/folders/%s/contents/", folderID)
		}
	}

	resp, err := c.doRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != nethttp.StatusOK {
		body := readResponseBody(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Results  []map[string]interface{} `json:"results"`
		Next     *string                  `json:"next"`
		Previous *string                  `json:"previous"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		resp.Body.Close()
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	resp.Body.Close()

	// Process items from this page
	for _, entry := range result.Results {
		itemType := getStringField(entry, "type", "folderContents")
		itemData, ok := entry["item"].(map[string]interface{})
		if !ok {
			continue
		}

		if itemType == "folder" {
			if id, ok := itemData["id"].(string); ok {
				if name, ok := itemData["name"].(string); ok {
					folder := FolderInfo{
						ID:   id,
						Name: name,
					}
					// Parse date: try dateUploaded (library), dateInserted (jobs), dateCreated
					if dateStr, ok := itemData["dateUploaded"].(string); ok {
						if t, err := time.Parse(time.RFC3339, dateStr); err == nil {
							folder.DateUploaded = t
						}
					} else if dateStr, ok := itemData["dateInserted"].(string); ok {
						if t, err := time.Parse(time.RFC3339, dateStr); err == nil {
							folder.DateUploaded = t
						}
					} else if dateStr, ok := itemData["dateCreated"].(string); ok {
						if t, err := time.Parse(time.RFC3339, dateStr); err == nil {
							folder.DateUploaded = t
						}
					}
					contents.Folders = append(contents.Folders, folder)
				}
			}
		} else if itemType == "file" {
			id := getStringField(itemData, "id", "file")
			name := getStringField(itemData, "name", "file")
			size := int64(0)
			// v4.0.6: Handle multiple JSON number representations for large file sizes
			// JSON numbers are float64 by default, which can lose precision for files > 2^53 bytes
			// Also handle string representations and json.Number for robustness
			if rawSize, ok := itemData["decryptedSize"]; ok {
				switch v := rawSize.(type) {
				case float64:
					size = int64(v)
				case string:
					if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
						size = parsed
					}
				case json.Number:
					if parsed, err := v.Int64(); err == nil {
						size = parsed
					}
				}
			}
			file := FileInfo{
				ID:            id,
				Name:          name,
				DecryptedSize: size,
			}
			// Parse date: try dateUploaded (library), dateInserted (jobs), dateCreated
			if dateStr, ok := itemData["dateUploaded"].(string); ok {
				if t, err := time.Parse(time.RFC3339, dateStr); err == nil {
					file.DateUploaded = t
				}
			} else if dateStr, ok := itemData["dateInserted"].(string); ok {
				if t, err := time.Parse(time.RFC3339, dateStr); err == nil {
					file.DateUploaded = t
				}
			} else if dateStr, ok := itemData["dateCreated"].(string); ok {
				if t, err := time.Parse(time.RFC3339, dateStr); err == nil {
					file.DateUploaded = t
				}
			}
			contents.Files = append(contents.Files, file)
		}
	}

	// Set pagination info
	contents.PageSize = len(result.Results)
	if result.Next != nil && *result.Next != "" {
		contents.NextURL = extractAPIPath(*result.Next)
		contents.HasMore = true
	}
	if result.Previous != nil && *result.Previous != "" {
		contents.PrevURL = extractAPIPath(*result.Previous)
	}

	return contents, nil
}

// extractAPIPath extracts the API path from a full URL or returns the path as-is.
func extractAPIPath(url string) string {
	if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") {
		if idx := strings.Index(url, "/api/"); idx >= 0 {
			return url[idx:]
		}
	}
	return url
}

// ListFolderContentsAll fetches ALL pages of folder contents (may be slow for large folders).
// Use this when you need the complete list, not for interactive browsing.
func (c *Client) ListFolderContentsAll(ctx context.Context, folderID string) (*FolderContents, error) {
	contents := &FolderContents{
		Folders: make([]FolderInfo, 0),
		Files:   make([]FileInfo, 0),
	}

	nextURL := fmt.Sprintf("/api/v3/folders/%s/contents/", folderID)
	pageCount := 0

	for nextURL != "" {
		pageCount++
		if pageCount > constants.MaxPaginationPages {
			log.Printf("Warning: Pagination limit reached after %d pages (%d items fetched)",
				pageCount-1, len(contents.Folders)+len(contents.Files))
			break
		}

		page, err := c.ListFolderContentsPage(ctx, folderID, nextURL, 0)
		if err != nil {
			return nil, err
		}

		contents.Folders = append(contents.Folders, page.Folders...)
		contents.Files = append(contents.Files, page.Files...)
		nextURL = page.NextURL
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
		body := readResponseBody(resp.Body)
		return fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, body)
	}

	return nil
}

// FileTag represents a tag on a file
// v3.6.2: File tag management
type FileTag struct {
	Name           string `json:"name"`
	NormalizedName string `json:"normalizedName"`
}

// GetFileTags retrieves all tags for a file
// v3.6.2: File tag management - uses dedicated /tags/ endpoint
func (c *Client) GetFileTags(ctx context.Context, fileID string) ([]string, error) {
	path := fmt.Sprintf("/api/v3/files/%s/tags/", fileID)

	resp, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get file tags: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusOK {
		body := readResponseBody(resp.Body)
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, body)
	}

	var tags []FileTag
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, fmt.Errorf("failed to decode tags response: %w", err)
	}

	// Extract just the names
	result := make([]string, len(tags))
	for i, t := range tags {
		result[i] = t.Name
	}
	return result, nil
}

// AddFileTags adds tags to a file (one POST per tag)
// v3.6.2: File tag management - uses dedicated /tags/ endpoint
func (c *Client) AddFileTags(ctx context.Context, fileID string, tagsToAdd []string) error {
	path := fmt.Sprintf("/api/v3/files/%s/tags/", fileID)

	for _, tag := range tagsToAdd {
		requestBody := map[string]string{"name": tag}
		resp, err := c.doRequest(ctx, "POST", path, requestBody)
		if err != nil {
			return fmt.Errorf("failed to add tag %q: %w", tag, err)
		}
		resp.Body.Close()

		// Accept 200, 201, 202 (Accepted), or 204 as success
		if resp.StatusCode != nethttp.StatusOK && resp.StatusCode != nethttp.StatusCreated &&
			resp.StatusCode != nethttp.StatusAccepted && resp.StatusCode != nethttp.StatusNoContent {
			return fmt.Errorf("failed to add tag %q: status %d", tag, resp.StatusCode)
		}
	}
	return nil
}

// RemoveFileTags removes specific tags from a file (one DELETE per tag)
// v3.6.2: File tag management - uses dedicated /tags/ endpoint
func (c *Client) RemoveFileTags(ctx context.Context, fileID string, tagsToRemove []string) error {
	path := fmt.Sprintf("/api/v3/files/%s/tags/", fileID)

	for _, tag := range tagsToRemove {
		requestBody := map[string]string{"name": tag}
		resp, err := c.doRequest(ctx, "DELETE", path, requestBody)
		if err != nil {
			return fmt.Errorf("failed to remove tag %q: %w", tag, err)
		}
		resp.Body.Close()

		// Accept 200, 202 (Accepted), or 204 as success
		if resp.StatusCode != nethttp.StatusOK && resp.StatusCode != nethttp.StatusAccepted &&
			resp.StatusCode != nethttp.StatusNoContent {
			return fmt.Errorf("failed to remove tag %q: status %d", tag, resp.StatusCode)
		}
	}
	return nil
}

// UpdateFileTags replaces all tags on a file with the given tags
// v3.6.2: File tag management - clears existing tags and adds new ones
func (c *Client) UpdateFileTags(ctx context.Context, fileID string, newTags []string) error {
	// Get current tags
	currentTags, err := c.GetFileTags(ctx, fileID)
	if err != nil {
		return fmt.Errorf("failed to get current tags: %w", err)
	}

	// Remove all existing tags
	if len(currentTags) > 0 {
		if err := c.RemoveFileTags(ctx, fileID, currentTags); err != nil {
			return fmt.Errorf("failed to clear existing tags: %w", err)
		}
	}

	// Add new tags
	if len(newTags) > 0 {
		if err := c.AddFileTags(ctx, fileID, newTags); err != nil {
			return fmt.Errorf("failed to add new tags: %w", err)
		}
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
		body := readResponseBody(resp.Body)
		return fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, body)
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
		body := readResponseBody(resp.Body)
		return fmt.Errorf("stop job failed: status %d: %s", resp.StatusCode, body)
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
		body := readResponseBody(resp.Body)
		return nil, fmt.Errorf("get job statuses failed: status %d: %s", resp.StatusCode, body)
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
	pageCount := 0

	for nextURL != "" {
		pageCount++
		if pageCount > constants.MaxPaginationPages {
			log.Printf("Warning: Pagination limit reached after %d pages (%d files fetched)", pageCount-1, len(allFiles))
			break
		}
		if pageCount == constants.PaginationWarningThreshold {
			log.Printf("Warning: Approaching pagination limit (page %d of %d)", pageCount, constants.MaxPaginationPages)
		}

		resp, err := c.doRequest(ctx, "GET", nextURL, nil)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != nethttp.StatusOK {
			body := readResponseBody(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("list job files failed: status %d: %s", resp.StatusCode, body)
		}

		var result struct {
			Count   int              `json:"count"`
			Next    *string          `json:"next"`
			Results []models.JobFile `json:"results"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("failed to decode job files response: %w", err)
		}

		// Close body immediately after reading - don't use defer in loop
		resp.Body.Close()

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
		body := readResponseBody(resp.Body)
		return fmt.Errorf("delete job failed: status %d: %s", resp.StatusCode, body)
	}

	return nil
}

// =============================================================================
// Job Tags API (v4.0.0 - Auto-Download Eligibility Engine)
// =============================================================================

// JobTag represents a tag on a job
type JobTag struct {
	Name           string `json:"name"`
	NormalizedName string `json:"normalizedName"`
}

// GetJobTags retrieves all tags for a job.
// Used by auto-download eligibility engine to check correctness tags.
func (c *Client) GetJobTags(ctx context.Context, jobID string) ([]string, error) {
	path := fmt.Sprintf("/api/v3/jobs/%s/tags/", jobID)

	resp, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get job tags: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusOK {
		body := readResponseBody(resp.Body)
		return nil, fmt.Errorf("get job tags failed: status %d: %s", resp.StatusCode, body)
	}

	var tags []JobTag
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, fmt.Errorf("failed to decode job tags response: %w", err)
	}

	// Extract just the names
	result := make([]string, len(tags))
	for i, t := range tags {
		result[i] = t.Name
	}
	return result, nil
}

// AddJobTag adds a tag to a job.
// Used by auto-download to mark jobs as downloaded (autoDownloaded:true).
func (c *Client) AddJobTag(ctx context.Context, jobID, tag string) error {
	path := fmt.Sprintf("/api/v3/jobs/%s/tags/", jobID)

	requestBody := map[string]string{"name": tag}
	resp, err := c.doRequest(ctx, "POST", path, requestBody)
	if err != nil {
		return fmt.Errorf("failed to add job tag: %w", err)
	}
	defer resp.Body.Close()

	// Accept 200, 201, 202 (Accepted), or 204 as success
	if resp.StatusCode != nethttp.StatusOK && resp.StatusCode != nethttp.StatusCreated &&
		resp.StatusCode != nethttp.StatusAccepted && resp.StatusCode != nethttp.StatusNoContent {
		body := readResponseBody(resp.Body)
		return fmt.Errorf("add job tag failed: status %d: %s", resp.StatusCode, body)
	}

	return nil
}

// HasJobTag checks if a job has a specific tag.
// Convenience method for eligibility checking.
func (c *Client) HasJobTag(ctx context.Context, jobID, tagName string) (bool, error) {
	tags, err := c.GetJobTags(ctx, jobID)
	if err != nil {
		return false, err
	}
	for _, t := range tags {
		if t == tagName {
			return true, nil
		}
	}
	return false, nil
}

// =============================================================================
// Job Custom Fields API (v4.0.0 - Auto-Download Eligibility Engine)
// =============================================================================

// JobCustomField represents a custom field on a job
type JobCustomField struct {
	FieldID string      `json:"fieldId"`
	Name    string      `json:"name"`
	Value   interface{} `json:"value"`
}

// GetJobCustomFields retrieves all custom fields for a job.
// Used by auto-download eligibility engine to check "Auto Download" field.
// v4.3.2: Fixed to handle object format from API (not array).
func (c *Client) GetJobCustomFields(ctx context.Context, jobID string) ([]JobCustomField, error) {
	path := fmt.Sprintf("/api/v3/jobs/%s/custom-fields/", jobID)

	resp, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get job custom fields: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusOK {
		body := readResponseBody(resp.Body)
		return nil, fmt.Errorf("get job custom fields failed: status %d: %s", resp.StatusCode, body)
	}

	// v4.3.2: API returns object format: {"fieldName": {"meta": {...}, "value": "..."}, ...}
	// Each field has: meta (field definition), user (who set it), value (actual value)
	type fieldData struct {
		Meta struct {
			Name string `json:"name"`
		} `json:"meta"`
		Value interface{} `json:"value"`
	}
	var fieldsMap map[string]fieldData
	if err := json.NewDecoder(resp.Body).Decode(&fieldsMap); err != nil {
		return nil, fmt.Errorf("failed to decode job custom fields response: %w", err)
	}

	// Convert map to slice
	var fields []JobCustomField
	for name, data := range fieldsMap {
		fields = append(fields, JobCustomField{
			Name:  name,
			Value: data.Value,
		})
	}

	return fields, nil
}

// GetJobCustomFieldValue retrieves a specific custom field value by name.
// Returns the value as a string, or empty string if not found.
func (c *Client) GetJobCustomFieldValue(ctx context.Context, jobID, fieldName string) (string, error) {
	fields, err := c.GetJobCustomFields(ctx, jobID)
	if err != nil {
		return "", err
	}

	for _, f := range fields {
		if f.Name == fieldName {
			// v4.3.3: Handle nil/null values (field defined but not set)
			if f.Value == nil {
				return "", nil
			}
			// Convert value to string
			switch v := f.Value.(type) {
			case string:
				return v, nil
			case bool:
				if v {
					return "true", nil
				}
				return "false", nil
			case float64:
				return fmt.Sprintf("%v", v), nil
			default:
				return fmt.Sprintf("%v", v), nil
			}
		}
	}

	return "", nil // Field not found
}

// WorkspaceCustomFieldsResponse represents the API response for workspace custom fields.
// Endpoint: GET /api/v2/organizations/{company_code}/workspaces/{workspace_id}/custom-fields/
// v4.2.1: Added for auto-download setup validation
type WorkspaceCustomFieldsResponse struct {
	// Fields is a map of jobType -> section -> list of fields
	// Example: {"compute": {"Context": [{name: "Auto Download", ...}]}, "workstation": {}}
	Fields    map[string]map[string][]WorkspaceCustomField `json:"fields"`
	IsEnabled bool                                          `json:"isEnabled"`
}

// WorkspaceCustomField represents a custom field definition in a workspace.
// v4.2.1: Used for validating auto-download setup
type WorkspaceCustomField struct {
	Name                 string   `json:"name"`
	ValueType            string   `json:"valueType"`            // "text", "select", "number", "date", "file", "user"
	EnumOptions          []string `json:"enumOptions"`          // For select fields
	Placeholder          string   `json:"placeholder"`
	HelpText             string   `json:"helpText"`
	IsMultiple           bool     `json:"isMultiple"`
	IsRequired           bool     `json:"isRequired"`
	AllowOther           bool     `json:"allowOther"`
	Section              string   `json:"section"`              // "Context", "Inputs", "Findings"
	OriginatingWorkspace string   `json:"originatingWorkspace"`
}

// AutoDownloadValidation contains the results of validating auto-download setup.
// v4.2.1: Used by GUI and CLI to check workspace configuration
type AutoDownloadValidation struct {
	CustomFieldsEnabled      bool     `json:"customFieldsEnabled"`      // Is custom field collection active?
	HasAutoDownloadField     bool     `json:"hasAutoDownloadField"`     // Is "Auto Download" field defined?
	AutoDownloadFieldType    string   `json:"autoDownloadFieldType"`    // "select", "text", etc.
	AutoDownloadFieldSection string   `json:"autoDownloadFieldSection"` // Which section it's in
	AvailableValues          []string `json:"availableValues"`          // If select field, what are the options?
	HasAutoDownloadPathField bool     `json:"hasAutoDownloadPathField"` // Is "Auto Download Path" field defined? (optional)
	Warnings                 []string `json:"warnings"`
	Errors                   []string `json:"errors"`
}

// GetWorkspaceCustomFields retrieves the list of custom fields defined for jobs in the workspace.
// This requires the user's company code and workspace ID, which are obtained from GetUserProfile().
// Endpoint: GET /api/v2/organizations/{company_code}/workspaces/{workspace_id}/custom-fields/
// v4.2.1: Added for auto-download setup validation
func (c *Client) GetWorkspaceCustomFields(ctx context.Context) (*WorkspaceCustomFieldsResponse, error) {
	// First, get user profile to extract company code and workspace ID
	profile, err := c.GetUserProfile(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get user profile: %w", err)
	}

	if profile.Company.Code == "" {
		return nil, fmt.Errorf("user profile does not contain company code")
	}
	if profile.Workspace.ID == "" {
		return nil, fmt.Errorf("user profile does not contain workspace ID")
	}

	// Build the API path
	path := fmt.Sprintf("/api/v2/organizations/%s/workspaces/%s/custom-fields/",
		profile.Company.Code, profile.Workspace.ID)

	resp, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusOK {
		body := readResponseBody(resp.Body)
		return nil, fmt.Errorf("get workspace custom fields failed: status %d: %s", resp.StatusCode, body)
	}

	var result WorkspaceCustomFieldsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode workspace custom fields: %w", err)
	}

	return &result, nil
}

// ValidateAutoDownloadSetup checks if the workspace has the required custom fields for auto-download.
// Returns validation results including whether the "Auto Download" field exists and its configuration.
// v4.2.1: Added for GUI/CLI auto-download setup validation
func (c *Client) ValidateAutoDownloadSetup(ctx context.Context) (*AutoDownloadValidation, error) {
	result := &AutoDownloadValidation{
		Warnings: []string{},
		Errors:   []string{},
	}

	// Get workspace custom fields
	fields, err := c.GetWorkspaceCustomFields(ctx)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("Failed to retrieve workspace custom fields: %v", err))
		return result, nil // Return result with error, don't fail the call
	}

	result.CustomFieldsEnabled = fields.IsEnabled

	if !fields.IsEnabled {
		result.Warnings = append(result.Warnings, "Custom fields are not enabled for this workspace")
		return result, nil
	}

	// Search for "Auto Download" field in all job types and sections
	autoDownloadFieldName := "Auto Download"
	autoDownloadPathFieldName := "Auto Download Path"

	for jobType, sections := range fields.Fields {
		for section, fieldList := range sections {
			for _, field := range fieldList {
				if field.Name == autoDownloadFieldName {
					result.HasAutoDownloadField = true
					result.AutoDownloadFieldType = field.ValueType
					result.AutoDownloadFieldSection = section
					result.AvailableValues = field.EnumOptions

					// Log which job type has the field
					if jobType != "compute" {
						result.Warnings = append(result.Warnings,
							fmt.Sprintf("'%s' field found in '%s' job type (expected 'compute')",
								autoDownloadFieldName, jobType))
					}
				}

				if field.Name == autoDownloadPathFieldName {
					result.HasAutoDownloadPathField = true
				}
			}
		}
	}

	// Build validation messages
	if !result.HasAutoDownloadField {
		result.Errors = append(result.Errors,
			fmt.Sprintf("Required custom field '%s' not found in workspace. Please create it in Workspace Settings > Custom Fields.",
				autoDownloadFieldName))
	} else {
		// v4.3.1: Enhanced validation - verify field type and required options
		// Field type must be "select" (Option List)
		if result.AutoDownloadFieldType != "select" {
			result.Errors = append(result.Errors,
				fmt.Sprintf("'%s' field must be type 'select' (Option List), but is '%s'",
					autoDownloadFieldName, result.AutoDownloadFieldType))
		} else {
			// Verify required options exist
			requiredOptions := []string{"Enabled", "Conditional", "Disabled"}
			for _, required := range requiredOptions {
				found := false
				for _, actual := range result.AvailableValues {
					if actual == required {
						found = true
						break
					}
				}
				if !found {
					result.Errors = append(result.Errors,
						fmt.Sprintf("'%s' field missing required option '%s'", autoDownloadFieldName, required))
				}
			}

			// Warn about extra/unexpected options
			for _, actual := range result.AvailableValues {
				isExpected := false
				for _, required := range requiredOptions {
					if actual == required {
						isExpected = true
						break
					}
				}
				if !isExpected {
					result.Warnings = append(result.Warnings,
						fmt.Sprintf("'%s' field has unexpected option '%s'", autoDownloadFieldName, actual))
				}
			}
		}
	}

	return result, nil
}
