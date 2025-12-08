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
// with automatic credential refresh from Rescale API.
//
// This provider works with AWS SDK's CredentialsCache to provide automatic
// credential refresh:
//   - AWS SDK calls Retrieve() when credentials are needed
//   - Returns credentials with 15-minute expiry
//   - CredentialsCache will refresh 5 minutes before expiry (configurable via ExpiryWindow)
//   - No manual refresh logic needed - AWS SDK handles everything
//
// If fileInfo is provided, the provider will request credentials for that specific file's storage,
// allowing cross-storage downloads (e.g., Azure user downloading S3 job outputs)
type RescaleCredentialProvider struct {
	apiClient *api.Client
	fileInfo  *models.CloudFile // Optional: for file-specific credentials
	mu        sync.RWMutex
	creds     aws.Credentials
	lastFetch time.Time
}

// NewRescaleCredentialProvider creates a new auto-refreshing credential provider
// for use with AWS SDK v2
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
		apiClient: apiClient,
		fileInfo:  fileInfo,
	}
}

// Retrieve fetches credentials from Rescale API
// This is called automatically by AWS SDK when credentials are needed or expired
//
// The method is thread-safe and can be called concurrently by multiple S3 clients.
// Credentials are assumed to expire in 15 minutes (conservative estimate).
//
// If fileInfo was provided during construction, requests credentials for that file's storage.
// Otherwise, requests credentials for user's default storage.
func (p *RescaleCredentialProvider) Retrieve(ctx context.Context) (aws.Credentials, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Fetch credentials for file's specific storage (if fileInfo provided) or user's default storage (if nil)
	s3Creds, _, err := p.apiClient.GetStorageCredentials(ctx, p.fileInfo)
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
