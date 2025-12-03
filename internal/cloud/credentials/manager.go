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
}

// Global singleton instance shared across all upload/download operations
var (
	globalManager     *Manager
	globalManagerOnce sync.Once
	globalManagerMu   sync.Mutex
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
			apiClient: apiClient,
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
