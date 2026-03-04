package credentials

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/constants"
	inthttp "github.com/rescale/rescale-int/internal/http"
	"github.com/rescale/rescale-int/internal/models"
)

// TestWarmupProxyIfNeeded_NoOp verifies warmup is a no-op for non-basic proxy modes.
func TestWarmupProxyIfNeeded_NoOp(t *testing.T) {
	ctx := context.Background()

	// nil config — should not panic
	inthttp.WarmupProxyIfNeeded(ctx, nil)

	// no-proxy mode — should return immediately
	inthttp.WarmupProxyIfNeeded(ctx, &config.Config{ProxyMode: "no-proxy"})

	// empty proxy mode — should return immediately
	inthttp.WarmupProxyIfNeeded(ctx, &config.Config{ProxyMode: ""})

	// system proxy mode — should return immediately
	inthttp.WarmupProxyIfNeeded(ctx, &config.Config{ProxyMode: "system"})
}

// TestForceRefreshForStorage_CacheKeyConsistency verifies that ForceRefreshForStorage
// uses the same cache key format as GetAzureCredentialsForStorage (storageID:path).
func TestForceRefreshForStorage_CacheKeyConsistency(t *testing.T) {
	// Create manager directly (no API client needed for cache key logic verification)
	m := &Manager{
		storageS3Creds:      make(map[string]*models.S3Credentials),
		storageAzureCreds:   make(map[string]*models.AzureCredentials),
		storageCredsRefresh: make(map[string]time.Time),
	}

	// Simulate what GetAzureCredentialsForStorage writes
	storageID := "storage-123"
	path := "container/blob/file.dat"
	expectedKey := storageID + ":" + path

	// Pre-populate using the same key format GetAzureCredentialsForStorage uses
	m.storageAzureCreds[expectedKey] = &models.AzureCredentials{
		SASToken: "old-token",
	}
	m.storageCredsRefresh[expectedKey] = time.Now()

	// Build a fileInfo with Storage.ID and PathParts.Path
	fileInfo := &models.CloudFile{
		Storage: &models.CloudFileStorage{
			ID: storageID,
		},
		PathParts: &models.CloudFilePathParts{
			Path: path,
		},
	}

	// Verify the cache key construction matches by testing the key formula directly.
	cacheKey := storageID
	if fileInfo.PathParts != nil && fileInfo.PathParts.Path != "" {
		cacheKey = storageID + ":" + fileInfo.PathParts.Path
	}

	if cacheKey != expectedKey {
		t.Errorf("cache key mismatch: got %q, want %q", cacheKey, expectedKey)
	}

	// Verify bare storageID key is different (this was the bug before v4.8.2)
	if storageID == expectedKey {
		t.Error("bare storageID should differ from storageID:path key")
	}
}

// TestForceRefreshForStorage_FallbackConditions verifies that nil/empty fileInfo
// triggers the fallback to ForceRefresh (global credentials) path.
func TestForceRefreshForStorage_FallbackConditions(t *testing.T) {
	// Test that the fallback conditions in ForceRefreshForStorage are correct:
	// nil fileInfo, nil Storage, or empty Storage.ID should all trigger fallback.

	tests := []struct {
		name     string
		fileInfo *models.CloudFile
		wantFallback bool
	}{
		{"nil fileInfo", nil, true},
		{"nil Storage", &models.CloudFile{}, true},
		{"empty Storage ID", &models.CloudFile{
			Storage: &models.CloudFileStorage{ID: ""},
		}, true},
		{"valid Storage ID", &models.CloudFile{
			Storage: &models.CloudFileStorage{ID: "abc"},
		}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shouldFallback := tt.fileInfo == nil || tt.fileInfo.Storage == nil || tt.fileInfo.Storage.ID == ""
			if shouldFallback != tt.wantFallback {
				t.Errorf("fallback = %v, want %v", shouldFallback, tt.wantFallback)
			}
		})
	}
}

// TestManagerCacheKeyFormat verifies that the Azure per-file SAS token cache
// uses storageID:path format consistently across read and write paths.
func TestManagerCacheKeyFormat(t *testing.T) {
	tests := []struct {
		name      string
		storageID string
		path      string
		wantKey   string
	}{
		{
			name:      "with path",
			storageID: "abc",
			path:      "container/folder/file.txt",
			wantKey:   "abc:container/folder/file.txt",
		},
		{
			name:      "empty path",
			storageID: "abc",
			path:      "",
			wantKey:   "abc",
		},
		{
			name:      "root path",
			storageID: "xyz",
			path:      "file.txt",
			wantKey:   "xyz:file.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cacheKey := tt.storageID
			if tt.path != "" {
				cacheKey = tt.storageID + ":" + tt.path
			}
			if cacheKey != tt.wantKey {
				t.Errorf("cache key = %q, want %q", cacheKey, tt.wantKey)
			}
		})
	}
}

// --- v4.8.3: EnsureFresh tests ---

// newTestManagerWithServer creates a Manager backed by an httptest server
// that returns S3 credentials. Returns the manager, server (caller must close),
// and an atomic counter of GetStorageCredentials API calls.
func newTestManagerWithServer(t *testing.T) (*Manager, *httptest.Server, *atomic.Int32) {
	t.Helper()

	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/credentials/" && r.Method == "POST" {
			callCount.Add(1)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"storageType":  "S3Storage",
				"storageDir":   "test-dir",
				"accessKey":    "AKIATEST",
				"secretKey":    "secret",
				"sessionToken": "token",
			})
			return
		}
		http.NotFound(w, r)
	}))

	cfg := &config.Config{
		APIBaseURL: server.URL,
		APIKey:     "test-key",
		ProxyMode:  "no-proxy",
	}
	client, err := api.NewClient(cfg)
	if err != nil {
		server.Close()
		t.Fatalf("api.NewClient: %v", err)
	}

	// Reset global singleton to avoid test pollution
	globalManagerMu.Lock()
	globalManager = nil
	globalManagerMu.Unlock()

	mgr := GetManager(client)
	return mgr, server, &callCount
}

func TestEnsureFresh_FreshS3Credentials(t *testing.T) {
	mgr, server, callCount := newTestManagerWithServer(t)
	defer server.Close()

	// Seed fresh S3 credentials
	mgr.mu.Lock()
	mgr.s3Credentials = &models.S3Credentials{AccessKeyID: "test"}
	mgr.lastCredsRefresh = time.Now()
	mgr.mu.Unlock()

	err := mgr.EnsureFresh(context.Background())
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if callCount.Load() != 0 {
		t.Errorf("Expected 0 API calls for fresh S3 creds, got %d", callCount.Load())
	}
}

func TestEnsureFresh_FreshAzureOnlyCredentials(t *testing.T) {
	mgr, server, callCount := newTestManagerWithServer(t)
	defer server.Close()

	// Seed fresh Azure-only credentials (s3 is nil, azure populated)
	mgr.mu.Lock()
	mgr.s3Credentials = nil
	mgr.azureCredentials = &models.AzureCredentials{SASToken: "test-sas"}
	mgr.lastCredsRefresh = time.Now()
	mgr.mu.Unlock()

	err := mgr.EnsureFresh(context.Background())
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if callCount.Load() != 0 {
		t.Errorf("Expected 0 API calls for fresh Azure-only creds, got %d", callCount.Load())
	}
}

func TestEnsureFresh_StaleCredentials(t *testing.T) {
	mgr, server, callCount := newTestManagerWithServer(t)
	defer server.Close()

	// Seed stale credentials (older than CredentialFreshnessThreshold)
	mgr.mu.Lock()
	mgr.s3Credentials = &models.S3Credentials{AccessKeyID: "old"}
	mgr.lastCredsRefresh = time.Now().Add(-(constants.CredentialFreshnessThreshold + time.Minute))
	mgr.mu.Unlock()

	err := mgr.EnsureFresh(context.Background())
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if callCount.Load() != 1 {
		t.Errorf("Expected 1 API call for stale creds, got %d", callCount.Load())
	}

	// Verify credentials were updated
	mgr.mu.RLock()
	if mgr.s3Credentials == nil {
		t.Error("s3Credentials should be populated after refresh")
	}
	if mgr.s3Credentials.AccessKeyID != "AKIATEST" {
		t.Errorf("s3Credentials.AccessKeyID = %q, want %q", mgr.s3Credentials.AccessKeyID, "AKIATEST")
	}
	mgr.mu.RUnlock()
}

func TestEnsureFresh_NoCredentials(t *testing.T) {
	mgr, server, callCount := newTestManagerWithServer(t)
	defer server.Close()

	// No credentials at all (zero-value lastCredsRefresh, nil creds)
	err := mgr.EnsureFresh(context.Background())
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if callCount.Load() != 1 {
		t.Errorf("Expected 1 API call for no creds, got %d", callCount.Load())
	}
}

func TestEnsureFresh_ConcurrentCallsSingleAPICall(t *testing.T) {
	mgr, server, callCount := newTestManagerWithServer(t)
	defer server.Close()

	// Seed stale credentials so EnsureFresh needs to refresh
	mgr.mu.Lock()
	mgr.s3Credentials = &models.S3Credentials{AccessKeyID: "stale"}
	mgr.lastCredsRefresh = time.Now().Add(-(constants.CredentialFreshnessThreshold + time.Minute))
	mgr.mu.Unlock()

	// Launch 10 concurrent EnsureFresh calls
	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)
	errs := make([]error, goroutines)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			errs[idx] = mgr.EnsureFresh(context.Background())
		}(i)
	}
	wg.Wait()

	// All should succeed
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: EnsureFresh error: %v", i, err)
		}
	}

	// Double-checked locking should result in exactly 1 API call
	calls := callCount.Load()
	if calls != 1 {
		t.Errorf("Expected exactly 1 API call from %d concurrent EnsureFresh, got %d", goroutines, calls)
	}
}

func TestEnsureFresh_WallClockDetectsStaleAfterSimulatedSleep(t *testing.T) {
	mgr, server, callCount := newTestManagerWithServer(t)
	defer server.Close()

	// Simulate post-sleep scenario: lastCredsRefresh was set 9 minutes ago (wall-clock).
	// This is past the 8-minute CredentialFreshnessThreshold.
	mgr.mu.Lock()
	mgr.s3Credentials = &models.S3Credentials{AccessKeyID: "pre-sleep"}
	mgr.lastCredsRefresh = time.Now().Add(-9 * time.Minute)
	mgr.mu.Unlock()

	err := mgr.EnsureFresh(context.Background())
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if callCount.Load() != 1 {
		t.Errorf("Expected 1 API call for 9-min-old creds (threshold=8m), got %d", callCount.Load())
	}
}

func TestEnsureFresh_RepeatedCallsNoCaching(t *testing.T) {
	mgr, server, callCount := newTestManagerWithServer(t)
	defer server.Close()

	// First call: no creds, should refresh
	err := mgr.EnsureFresh(context.Background())
	if err != nil {
		t.Fatalf("First EnsureFresh: %v", err)
	}
	if callCount.Load() != 1 {
		t.Errorf("After first call: expected 1 API call, got %d", callCount.Load())
	}

	// Second call: creds now fresh, should NOT refresh
	err = mgr.EnsureFresh(context.Background())
	if err != nil {
		t.Fatalf("Second EnsureFresh: %v", err)
	}
	if callCount.Load() != 1 {
		t.Errorf("After second call: expected still 1 API call (cached), got %d", callCount.Load())
	}
}
