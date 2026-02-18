package credentials

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/models"
)

// Manager manages storage credentials and API metadata globally across all concurrent operations
// This ensures credentials and metadata are shared and refreshed centrally, avoiding redundant API calls
//
// The manager uses a double-checked locking pattern for thread-safe caching:
//   - Fast path: Read lock to check if refresh is needed
//   - Slow path: Write lock to refresh data
//   - Second check: Avoid redundant refreshes if another goroutine already refreshed
//
// Cached data:
//   - Storage credentials: Refreshed every 10 minutes (5-minute safety margin before 15-min expiry)
//   - Storage-specific credentials: Same refresh interval, keyed by storage ID (for cross-storage downloads)
//   - User profile: Refreshed every 5 minutes (rarely changes, but refresh to catch updates)
//   - Root folders: Refreshed every 5 minutes (rarely changes)
type Manager struct {
	apiClient          *api.Client
	s3Credentials      *models.S3Credentials
	azureCredentials   *models.AzureCredentials
	lastCredsRefresh   time.Time
	userProfile        *models.UserProfile
	lastProfileRefresh time.Time
	rootFolders        *models.RootFolders
	lastFoldersRefresh time.Time
	mu                 sync.RWMutex

	// Storage-specific credential caches (for cross-storage/job file downloads)
	// Keyed by storage ID to share credentials across files from the same storage
	storageS3Creds      map[string]*models.S3Credentials
	storageAzureCreds   map[string]*models.AzureCredentials
	storageCredsRefresh map[string]time.Time
}

// Global singleton instance shared across all upload/download operations
var (
	globalManager   *Manager
	globalManagerMu sync.Mutex
)

// GetManager returns the singleton credential manager for the given API client
// This is thread-safe and ensures only one manager exists per API client
//
// If the API client changes between calls (e.g., configuration update), the manager
// will be recreated to use the new client.
func GetManager(apiClient *api.Client) *Manager {
	globalManagerMu.Lock()
	defer globalManagerMu.Unlock()

	// Check if we need to create or replace the manager
	// (in case API client changes between sessions)
	if globalManager == nil || globalManager.apiClient != apiClient {
		globalManager = &Manager{
			apiClient:           apiClient,
			storageS3Creds:      make(map[string]*models.S3Credentials),
			storageAzureCreds:   make(map[string]*models.AzureCredentials),
			storageCredsRefresh: make(map[string]time.Time),
		}
	}

	return globalManager
}

// GetS3Credentials returns cached S3 credentials, refreshing if needed
// Thread-safe for concurrent access from multiple operations
//
// Refresh logic:
//   - If credentials are less than 10 minutes old: Return cached (fast path)
//   - If credentials are older or nil: Fetch new ones (slow path)
//   - Double-check after acquiring write lock to avoid redundant refreshes
func (m *Manager) GetS3Credentials(ctx context.Context) (*models.S3Credentials, error) {
	// Fast path: check if refresh is needed (read lock only)
	m.mu.RLock()
	needsRefresh := time.Since(m.lastCredsRefresh) > constants.GlobalCredentialRefreshInterval || m.s3Credentials == nil
	if !needsRefresh {
		creds := m.s3Credentials
		m.mu.RUnlock()
		return creds, nil
	}
	m.mu.RUnlock()

	// Slow path: refresh needed (write lock)
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check: another goroutine might have refreshed while we waited
	if time.Since(m.lastCredsRefresh) <= constants.GlobalCredentialRefreshInterval && m.s3Credentials != nil {
		return m.s3Credentials, nil
	}

	// Fetch new credentials from Rescale API (for user's default storage)
	s3Creds, azureCreds, err := m.apiClient.GetStorageCredentials(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh credentials: %w", err)
	}

	// Update cached credentials
	m.s3Credentials = s3Creds
	m.azureCredentials = azureCreds
	m.lastCredsRefresh = time.Now()

	return m.s3Credentials, nil
}

// GetAzureCredentials returns cached Azure credentials, refreshing if needed
// Thread-safe for concurrent access from multiple operations
//
// Uses the same double-checked locking pattern as GetS3Credentials
func (m *Manager) GetAzureCredentials(ctx context.Context) (*models.AzureCredentials, error) {
	// Fast path: check if refresh is needed (read lock only)
	m.mu.RLock()
	needsRefresh := time.Since(m.lastCredsRefresh) > constants.GlobalCredentialRefreshInterval || m.azureCredentials == nil
	if !needsRefresh {
		creds := m.azureCredentials
		m.mu.RUnlock()
		return creds, nil
	}
	m.mu.RUnlock()

	// Slow path: refresh needed (write lock)
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check: another goroutine might have refreshed while we waited
	if time.Since(m.lastCredsRefresh) <= constants.GlobalCredentialRefreshInterval && m.azureCredentials != nil {
		return m.azureCredentials, nil
	}

	// Fetch new credentials from Rescale API (for user's default storage)
	s3Creds, azureCreds, err := m.apiClient.GetStorageCredentials(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh credentials: %w", err)
	}

	// Update cached credentials
	m.s3Credentials = s3Creds
	m.azureCredentials = azureCreds
	m.lastCredsRefresh = time.Now()

	return m.azureCredentials, nil
}

// ForceRefresh forces an immediate credential refresh, bypassing the cache
// Useful for recovering from token expiration errors or when credentials are known to be invalid
func (m *Manager) ForceRefresh(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s3Creds, azureCreds, err := m.apiClient.GetStorageCredentials(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to force refresh credentials: %w", err)
	}

	m.s3Credentials = s3Creds
	m.azureCredentials = azureCreds
	m.lastCredsRefresh = time.Now()

	return nil
}

// GetAge returns the duration since the last credential refresh
// Useful for debugging, monitoring, and logging credential freshness
func (m *Manager) GetAge() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return time.Since(m.lastCredsRefresh)
}

// GetS3CredentialsForStorage returns cached S3 credentials for a specific storage, refreshing if needed.
// This is used for cross-storage downloads (e.g., downloading job output files from a different storage).
// The cache is keyed by storage ID to share credentials across files from the same storage.
// Thread-safe with same double-checked locking pattern as GetS3Credentials.
//
// If fileInfo is nil or has no storage info, falls back to GetS3Credentials for user's default storage.
func (m *Manager) GetS3CredentialsForStorage(ctx context.Context, fileInfo *models.CloudFile) (*models.S3Credentials, error) {
	// If no file info or storage info, fall back to default credentials
	if fileInfo == nil || fileInfo.Storage == nil {
		return m.GetS3Credentials(ctx)
	}

	storageID := fileInfo.Storage.ID
	if storageID == "" {
		return m.GetS3Credentials(ctx)
	}

	// Fast path: check if refresh is needed (read lock only)
	m.mu.RLock()
	lastRefresh := m.storageCredsRefresh[storageID]
	creds := m.storageS3Creds[storageID]
	needsRefresh := time.Since(lastRefresh) > constants.GlobalCredentialRefreshInterval || creds == nil
	if !needsRefresh {
		m.mu.RUnlock()
		return creds, nil
	}
	m.mu.RUnlock()

	// Slow path: refresh needed (write lock)
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check: another goroutine might have refreshed while we waited
	lastRefresh = m.storageCredsRefresh[storageID]
	creds = m.storageS3Creds[storageID]
	if time.Since(lastRefresh) <= constants.GlobalCredentialRefreshInterval && creds != nil {
		return creds, nil
	}

	// Fetch new credentials for this specific storage
	s3Creds, azureCreds, err := m.apiClient.GetStorageCredentials(ctx, fileInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh storage-specific credentials: %w", err)
	}

	// Update cached credentials for this storage
	m.storageS3Creds[storageID] = s3Creds
	m.storageAzureCreds[storageID] = azureCreds
	m.storageCredsRefresh[storageID] = time.Now()

	return s3Creds, nil
}

// GetAzureCredentialsForStorage returns cached Azure credentials for a specific storage, refreshing if needed.
// This is used for cross-storage downloads (e.g., downloading job output files from a different storage).
// The cache is keyed by storage ID to share credentials across files from the same storage.
// Thread-safe with same double-checked locking pattern as GetAzureCredentials.
//
// If fileInfo is nil or has no storage info, falls back to GetAzureCredentials for user's default storage.
func (m *Manager) GetAzureCredentialsForStorage(ctx context.Context, fileInfo *models.CloudFile) (*models.AzureCredentials, error) {
	// If no file info or storage info, fall back to default credentials
	if fileInfo == nil || fileInfo.Storage == nil {
		return m.GetAzureCredentials(ctx)
	}

	storageID := fileInfo.Storage.ID
	if storageID == "" {
		return m.GetAzureCredentials(ctx)
	}

	// v4.6.6: For shared-file credential requests, the API returns per-file SAS tokens
	// scoped to a specific blob path. Include the file path in the cache key so that
	// each file gets credentials with its own per-file SAS token, rather than sharing
	// a cached response whose per-file token only matches a different file.
	cacheKey := storageID
	if fileInfo.PathParts != nil && fileInfo.PathParts.Path != "" {
		cacheKey = storageID + ":" + fileInfo.PathParts.Path
	}

	// Fast path: check if refresh is needed (read lock only)
	m.mu.RLock()
	lastRefresh := m.storageCredsRefresh[cacheKey]
	creds := m.storageAzureCreds[cacheKey]
	needsRefresh := time.Since(lastRefresh) > constants.GlobalCredentialRefreshInterval || creds == nil
	if !needsRefresh {
		m.mu.RUnlock()
		return creds, nil
	}
	m.mu.RUnlock()

	// Slow path: refresh needed (write lock)
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check: another goroutine might have refreshed while we waited
	lastRefresh = m.storageCredsRefresh[cacheKey]
	creds = m.storageAzureCreds[cacheKey]
	if time.Since(lastRefresh) <= constants.GlobalCredentialRefreshInterval && creds != nil {
		return creds, nil
	}

	// Fetch new credentials for this specific storage
	s3Creds, azureCreds, err := m.apiClient.GetStorageCredentials(ctx, fileInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh storage-specific credentials: %w", err)
	}

	// Update cached credentials for this storage+file
	m.storageS3Creds[cacheKey] = s3Creds
	m.storageAzureCreds[cacheKey] = azureCreds
	m.storageCredsRefresh[cacheKey] = time.Now()

	return azureCreds, nil
}

// ForceRefreshForStorage forces an immediate credential refresh for a specific storage.
// Useful for recovering from token expiration errors on cross-storage operations.
func (m *Manager) ForceRefreshForStorage(ctx context.Context, fileInfo *models.CloudFile) error {
	if fileInfo == nil || fileInfo.Storage == nil || fileInfo.Storage.ID == "" {
		return m.ForceRefresh(ctx)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	storageID := fileInfo.Storage.ID

	s3Creds, azureCreds, err := m.apiClient.GetStorageCredentials(ctx, fileInfo)
	if err != nil {
		return fmt.Errorf("failed to force refresh storage-specific credentials: %w", err)
	}

	m.storageS3Creds[storageID] = s3Creds
	m.storageAzureCreds[storageID] = azureCreds
	m.storageCredsRefresh[storageID] = time.Now()

	return nil
}

// GetUserProfile returns cached user profile, refreshing if needed
// Thread-safe for concurrent access from multiple operations
// Profile is refreshed every 5 minutes to catch account updates
func (m *Manager) GetUserProfile(ctx context.Context) (*models.UserProfile, error) {
	// Fast path: check if refresh is needed (read lock only)
	m.mu.RLock()
	needsRefresh := time.Since(m.lastProfileRefresh) > 5*time.Minute || m.userProfile == nil
	if !needsRefresh {
		profile := m.userProfile
		m.mu.RUnlock()
		return profile, nil
	}
	m.mu.RUnlock()

	// Slow path: refresh needed (write lock)
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check: another goroutine might have refreshed while we waited
	if time.Since(m.lastProfileRefresh) <= 5*time.Minute && m.userProfile != nil {
		return m.userProfile, nil
	}

	// Fetch new profile from Rescale API
	profile, err := m.apiClient.GetUserProfile(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh user profile: %w", err)
	}

	// Update cached profile
	m.userProfile = profile
	m.lastProfileRefresh = time.Now()

	return m.userProfile, nil
}

// GetRootFolders returns cached root folders, refreshing if needed
// Thread-safe for concurrent access from multiple operations
// Folders are refreshed every 5 minutes to catch folder structure updates
func (m *Manager) GetRootFolders(ctx context.Context) (*models.RootFolders, error) {
	// Fast path: check if refresh is needed (read lock only)
	m.mu.RLock()
	needsRefresh := time.Since(m.lastFoldersRefresh) > 5*time.Minute || m.rootFolders == nil
	if !needsRefresh {
		folders := m.rootFolders
		m.mu.RUnlock()
		return folders, nil
	}
	m.mu.RUnlock()

	// Slow path: refresh needed (write lock)
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check: another goroutine might have refreshed while we waited
	if time.Since(m.lastFoldersRefresh) <= 5*time.Minute && m.rootFolders != nil {
		return m.rootFolders, nil
	}

	// Fetch new folders from Rescale API
	folders, err := m.apiClient.GetRootFolders(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh root folders: %w", err)
	}

	// Update cached folders
	m.rootFolders = folders
	m.lastFoldersRefresh = time.Now()

	return m.rootFolders, nil
}
