package credentials

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/constants"
	inthttp "github.com/rescale/rescale-int/internal/http"
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
//   - Storage credentials: Refreshed every GlobalCredentialRefreshInterval
//   - Credentials for the storage a file is in: see storageCredsFor
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

	// Credentials for the storage a file is in, for cross-storage and job file
	// downloads. Behind a lock of their own, because mu is held across the
	// requests that refresh everything above, and a download's lookup must not
	// wait for those.
	storageMu    sync.RWMutex
	storageCreds map[string]*storageCreds // by storage, or by storage and path
	storageDirs  map[string]string        // the requester's own folder, by storage
	fetches      map[string]*credFetch    // requests in flight, by the key they answer
}

// storageCreds is one credentials response.
type storageCreds struct {
	s3      *models.S3Credentials
	azure   *models.AzureCredentials
	fetched time.Time
}

func (e *storageCreds) fresh() bool {
	return e != nil && time.Since(e.fetched) <= constants.GlobalCredentialRefreshInterval
}

// storageDir is the requester's own folder the response names, if it names one.
func (e *storageCreds) storageDir() string {
	switch {
	case e.s3 != nil:
		return e.s3.StorageDir
	case e.azure != nil:
		return e.azure.StorageDir
	}
	return ""
}

// credFetch is a credentials request in flight, which lookups of the same key
// wait for rather than send their own.
type credFetch struct {
	done      chan struct{}
	entry     *storageCreds
	err       error
	abandoned bool // the requester's context ended, so the platform never answered
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
			apiClient:    apiClient,
			storageCreds: make(map[string]*storageCreds),
			storageDirs:  make(map[string]string),
			fetches:      make(map[string]*credFetch),
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

	// Warm proxy before credential refresh API call
	inthttp.WarmupProxyIfNeeded(ctx, m.apiClient.GetConfig())

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

	// Warm proxy before credential refresh API call
	inthttp.WarmupProxyIfNeeded(ctx, m.apiClient.GetConfig())

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

// InvalidateS3Credentials drops the cached S3 credential the caller was served
// after the storage backend rejected it, so the next getter call fetches a
// replacement instead of re-serving the rejected one until its cache lifetime
// expires. It reports whether it dropped anything.
//
// Identity decides, not the clock: only the exact credential that was rejected
// is dropped. A burst of parts failing on one credential therefore invalidates
// it once — the first caller drops it and the rest match nothing — and a part
// that reports its rejection after a replacement has already been fetched
// cannot throw that replacement away.
func (m *Manager) InvalidateS3Credentials(rejected *models.S3Credentials) bool {
	if rejected == nil {
		return false
	}

	m.mu.Lock()
	dropped := m.s3Credentials == rejected
	if dropped {
		m.s3Credentials = nil
	}
	m.mu.Unlock()
	return dropped || m.dropStorageCreds(func(e *storageCreds) bool { return e.s3 == rejected })
}

// InvalidateAzureCredentials is InvalidateS3Credentials for Azure: a rejected
// SAS token must not be handed back to the retry that is trying to replace it.
func (m *Manager) InvalidateAzureCredentials(rejected *models.AzureCredentials) bool {
	if rejected == nil {
		return false
	}

	m.mu.Lock()
	dropped := m.azureCredentials == rejected
	if dropped {
		m.azureCredentials = nil
	}
	m.mu.Unlock()
	return dropped || m.dropStorageCreds(func(e *storageCreds) bool { return e.azure == rejected })
}

// dropStorageCreds forgets the storage credential that matches.
func (m *Manager) dropStorageCreds(match func(*storageCreds) bool) bool {
	m.storageMu.Lock()
	defer m.storageMu.Unlock()
	for key, e := range m.storageCreds {
		if match(e) {
			delete(m.storageCreds, key)
			return true
		}
	}
	return false
}

// ForceRefresh forces an immediate credential refresh, bypassing the cache
// Useful for recovering from token expiration errors or when credentials are known to be invalid
func (m *Manager) ForceRefresh(ctx context.Context) error {
	// Warm proxy before credential refresh API call
	inthttp.WarmupProxyIfNeeded(ctx, m.apiClient.GetConfig())

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

// EnsureFresh proactively refreshes credentials if older than
// CredentialFreshnessThreshold. Uses double-checked locking to prevent
// redundant API calls when multiple goroutines detect staleness simultaneously.
// Provider-agnostic: checks both S3 and Azure creds so neither backend
// causes spurious refresh storms.
//
// Uses wall-clock time (via Round(0)) instead of monotonic clock for age checks,
// because Go's monotonic clock may not account for system sleep on all platforms.
// This ensures correct staleness detection after laptop sleep/wake.
func (m *Manager) EnsureFresh(ctx context.Context) error {
	// Fast path: read-lock only, no contention
	// Use wall-clock time via Round(0) to correctly detect staleness after sleep
	m.mu.RLock()
	age := time.Now().Round(0).Sub(m.lastCredsRefresh.Round(0))
	hasCreds := m.s3Credentials != nil || m.azureCredentials != nil
	fresh := age <= constants.CredentialFreshnessThreshold && hasCreds
	m.mu.RUnlock()
	if fresh {
		return nil
	}

	// Slow path: write lock with double-check
	inthttp.WarmupProxyIfNeeded(ctx, m.apiClient.GetConfig())

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check: another goroutine may have refreshed while we waited for the lock
	age = time.Now().Round(0).Sub(m.lastCredsRefresh.Round(0))
	hasCreds = m.s3Credentials != nil || m.azureCredentials != nil
	if age <= constants.CredentialFreshnessThreshold && hasCreds {
		return nil
	}

	s3Creds, azureCreds, err := m.apiClient.GetStorageCredentials(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to proactively refresh credentials: %w", err)
	}

	m.s3Credentials = s3Creds
	m.azureCredentials = azureCreds
	m.lastCredsRefresh = time.Now()
	return nil
}

// WarmAll performs best-effort warming of all credential caches.
// Session-level (EnsureFresh) + provider-level (S3 + Azure) + metadata (profile + folders).
// All calls non-fatal — errors are silently ignored since callers retry on demand.
// Proxy warmup NOT included (needs config.Config, handled separately by callers).
func (m *Manager) WarmAll(ctx context.Context) {
	_ = m.EnsureFresh(ctx)
	_, _ = m.GetS3Credentials(ctx)
	_, _ = m.GetAzureCredentials(ctx)
	_, _ = m.GetUserProfile(ctx)
	_, _ = m.GetRootFolders(ctx)
}

// GetS3CredentialsForStorage returns S3 credentials that cover fileInfo's path in
// the storage it is in, for downloads from a storage other than the default one
// (e.g. job output files). Without a storage or a path it returns the default
// storage's credentials, which is what the request would ask for.
func (m *Manager) GetS3CredentialsForStorage(ctx context.Context, fileInfo *models.CloudFile) (*models.S3Credentials, error) {
	if !inStorage(fileInfo) {
		return m.GetS3Credentials(ctx)
	}
	e, err := m.storageCredsFor(ctx, fileInfo, fileInfo.PathParts.Path)
	if err != nil {
		return nil, err
	}
	return e.s3, nil
}

// GetAzureCredentialsForStorage is GetS3CredentialsForStorage for Azure, where
// the platform names a path by its container and blob.
func (m *Manager) GetAzureCredentialsForStorage(ctx context.Context, fileInfo *models.CloudFile) (*models.AzureCredentials, error) {
	if !inStorage(fileInfo) {
		return m.GetAzureCredentials(ctx)
	}
	e, err := m.storageCredsFor(ctx, fileInfo, fileInfo.PathParts.Container+"/"+fileInfo.PathParts.Path)
	if err != nil {
		return nil, err
	}
	return e.azure, nil
}

// inStorage reports whether fileInfo names a storage and a path in it.
func inStorage(fileInfo *models.CloudFile) bool {
	return fileInfo != nil && fileInfo.Storage != nil && fileInfo.Storage.ID != "" &&
		fileInfo.PathParts != nil && fileInfo.PathParts.Path != ""
}

// storageCredsFor returns a response that covers path in fileInfo's storage,
// fetching one if none is cached.
//
// The platform's credential for a storage always covers the requester's own
// folder there, which every response names as storageDir, and grants a path
// anywhere else on its own. So one response serves every path in that folder,
// and any other path is fetched for itself. Until the first response for a
// storage has named the folder, every path is fetched for itself.
//
// One request is in flight per key: lookups of the same key wait for it, and
// lookups of other keys go ahead, paced by the rate limiter.
func (m *Manager) storageCredsFor(ctx context.Context, fileInfo *models.CloudFile, path string) (*storageCreds, error) {
	storage := fileInfo.Storage.StorageType + ":" + fileInfo.Storage.ID

	m.storageMu.RLock()
	e := m.storageCreds[m.credKeyLocked(storage, path)]
	m.storageMu.RUnlock()
	if e.fresh() {
		return e, nil
	}

	for {
		m.storageMu.Lock()
		key := m.credKeyLocked(storage, path)
		if e := m.storageCreds[key]; e.fresh() {
			m.storageMu.Unlock()
			return e, nil
		}
		if f := m.fetches[key]; f != nil {
			m.storageMu.Unlock()
			select {
			case <-f.done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if f.abandoned && ctx.Err() == nil {
				continue // its requester gave up, not the platform: ask again
			}
			return f.entry, f.err
		}
		f := &credFetch{done: make(chan struct{})}
		m.fetches[key] = f
		m.storageMu.Unlock()

		m.fetchStorageCreds(ctx, f, key, storage, path, fileInfo)
		return f.entry, f.err
	}
}

// fetchStorageCreds sends f's request and files the response under the key it
// covers, which a response naming the requester's folder can change from the
// key it was fetched for.
func (m *Manager) fetchStorageCreds(ctx context.Context, f *credFetch, key, storage, path string, fileInfo *models.CloudFile) {
	inthttp.WarmupProxyIfNeeded(ctx, m.apiClient.GetConfig())
	s3Creds, azureCreds, err := m.apiClient.GetStorageCredentials(ctx, fileInfo)

	m.storageMu.Lock()
	defer m.storageMu.Unlock()
	defer close(f.done)
	delete(m.fetches, key)
	if err != nil {
		f.err = fmt.Errorf("failed to refresh storage-specific credentials: %w", err)
		f.abandoned = ctx.Err() != nil
		return
	}

	// Aged on the wall clock, as EnsureFresh ages the default pair: the monotonic
	// clock stops while a laptop sleeps, and the credential can expire meanwhile.
	f.entry = &storageCreds{s3: s3Creds, azure: azureCreds, fetched: time.Now().Round(0)}
	if dir := f.entry.storageDir(); dir != "" {
		m.storageDirs[storage] = dir
	}
	// A credential past its refresh interval would only be fetched again. The
	// daemon keeps one manager for its life, and would otherwise keep one for
	// every file it ever downloaded from another user's folder.
	for k, old := range m.storageCreds {
		if !old.fresh() {
			delete(m.storageCreds, k)
		}
	}
	m.storageCreds[m.credKeyLocked(storage, path)] = f.entry
}

// credKeyLocked is the cache key of the credential that covers path: the
// storage's, for a path in the requester's own folder there, as the platform
// decides it, and the path's own otherwise. It goes by location alone: a file
// the requester owns can sit in another user's folder. Caller holds storageMu.
func (m *Manager) credKeyLocked(storage, path string) string {
	if dir := m.storageDirs[storage]; dir != "" && (path == dir || strings.HasPrefix(path, dir+"/")) {
		return storage
	}
	return storage + ":" + path
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

	// Warm proxy before profile refresh API call
	inthttp.WarmupProxyIfNeeded(ctx, m.apiClient.GetConfig())

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

	// Warm proxy before folder refresh API call
	inthttp.WarmupProxyIfNeeded(ctx, m.apiClient.GetConfig())

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
