package credentials

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/models"
)

// RescaleCredentialProvider implements AWS SDK's CredentialsProvider interface
// with automatic credential refresh via the credential manager's cache.
//
// This provider works with AWS SDK's CredentialsCache to provide automatic
// credential refresh:
//   - AWS SDK calls Retrieve() when credentials are needed
//   - Returns credentials with 15-minute expiry
//   - CredentialsCache will refresh 5 minutes before expiry (configurable via ExpiryWindow)
//   - No manual refresh logic needed - AWS SDK handles everything
//
// UNIFIED CACHING: Uses the credential manager for all credential fetches,
// ensuring consistent caching across all S3 operations (downloads, uploads, etc.)
//
// If fileInfo is provided, the provider will request credentials for that specific file's storage,
// allowing cross-storage downloads (e.g., Azure user downloading S3 job outputs)
type RescaleCredentialProvider struct {
	credManager *Manager
	fileInfo    *models.CloudFile // Optional: for file-specific credentials
	mu          sync.RWMutex
	creds       aws.Credentials
	lastFetch   time.Time
}

// NewRescaleCredentialProvider creates a new auto-refreshing credential provider
// for use with AWS SDK v2
//
// Uses the credential manager's cache for all credential fetches, ensuring
// consistent caching across all S3 operations.
//
// If fileInfo is nil, requests credentials for user's default storage.
// If fileInfo is provided, requests credentials for that file's specific storage.
//
// Usage:
//
//	provider := NewRescaleCredentialProvider(apiClient, fileInfo)
//	cache := aws.NewCredentialsCache(provider, func(o *aws.CredentialsCacheOptions) {
//	    o.ExpiryWindow = 5 * time.Minute  // Refresh 5 min before expiry
//	})
//	cfg, _ := config.LoadDefaultConfig(ctx,
//	    config.WithCredentialsProvider(cache),
//	)
func NewRescaleCredentialProvider(apiClient *api.Client, fileInfo *models.CloudFile) *RescaleCredentialProvider {
	return &RescaleCredentialProvider{
		credManager: GetManager(apiClient),
		fileInfo:    fileInfo,
	}
}

// Retrieve fetches credentials using the credential manager's cache
// This is called automatically by AWS SDK when credentials are needed or expired
//
// The method is thread-safe and can be called concurrently by multiple S3 clients.
// Credentials are assumed to expire in 15 minutes (conservative estimate).
//
// If fileInfo was provided during construction, requests cached credentials for that file's storage.
// Otherwise, requests cached credentials for user's default storage.
func (p *RescaleCredentialProvider) Retrieve(ctx context.Context) (aws.Credentials, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Fetch credentials using credential manager's cache (keyed by storage ID for file-specific)
	var s3Creds *models.S3Credentials
	var err error

	if p.fileInfo != nil {
		s3Creds, err = p.credManager.GetS3CredentialsForStorage(ctx, p.fileInfo)
	} else {
		s3Creds, err = p.credManager.GetS3Credentials(ctx)
	}
	if err != nil {
		return aws.Credentials{}, fmt.Errorf("failed to get credentials: %w", err)
	}

	if s3Creds == nil {
		return aws.Credentials{}, fmt.Errorf("received nil S3 credentials")
	}

	// Assume 15-minute expiry (conservative, safe assumption)
	// AWS SDK's CredentialsCache will refresh before expiry based on ExpiryWindow
	expiresAt := time.Now().Add(15 * time.Minute)

	p.creds = aws.Credentials{
		AccessKeyID:     s3Creds.AccessKeyID,
		SecretAccessKey: s3Creds.SecretKey,
		SessionToken:    s3Creds.SessionToken,
		Source:          "RescaleCredentialProvider",
		CanExpire:       true,
		Expires:         expiresAt,
	}
	p.lastFetch = time.Now()

	return p.creds, nil
}
