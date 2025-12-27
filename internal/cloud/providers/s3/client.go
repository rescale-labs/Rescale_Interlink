// Package s3 provides an S3 implementation of the CloudTransfer interface.
// This file contains the S3 client factory with auto-refreshing credentials.
//
// This file contains the core S3 client logic. The provider files use this
// directly for all S3 operations.
package s3

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	nethttp "net/http"
	"net/http/httptrace"
	"os"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awscreds "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/credentials"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/http"
	"github.com/rescale/rescale-int/internal/models"
)

// S3Client wraps the AWS S3 client with auto-refreshing credentials and connection pooling.
// This is the core S3 client used by all provider operations (streaming, pre-encrypt, download).
//
// Thread-safe: All operations are safe for concurrent use.
//
// Added fileInfo and apiClient fields for cross-bucket credential refresh.
// When fileInfo is set, EnsureFreshCredentials uses file-specific credentials instead
// of the global credential manager's default storage credentials.
type S3Client struct {
	client      *s3.Client
	storageInfo *models.StorageInfo
	credManager *credentials.Manager
	apiClient   *api.Client         // For file-specific credential refresh
	fileInfo    *models.CloudFile   // For cross-bucket credential fetching (nil for uploads)
	httpClient  *nethttp.Client     // Shared HTTP client for connection reuse
	clientMu    sync.Mutex          // Protects client updates during credential refresh
}

// NewS3Client creates a new S3 client with auto-refreshing credentials.
// This is the replacement for upload.NewS3Uploader() client creation logic.
//
// The client:
//   - Uses the global credential manager for auto-refresh (shared across operations)
//   - Maintains a connection pool via the shared HTTP client
//   - Is thread-safe for concurrent operations
//
// Parameters:
//   - ctx: Context for cancellation and timeout control
//   - storageInfo: S3 storage configuration (bucket, region, path base)
//   - apiClient: Rescale API client for credential refresh
//   - fileInfo: Optional file info for cross-storage downloads (nil for uploads)
func NewS3Client(ctx context.Context, storageInfo *models.StorageInfo, apiClient *api.Client, fileInfo *models.CloudFile) (*S3Client, error) {
	clientStart := time.Now()
	if storageInfo == nil {
		return nil, fmt.Errorf("storageInfo is required")
	}
	if apiClient == nil {
		return nil, fmt.Errorf("apiClient is required")
	}

	// Create shared optimized HTTP client with proxy support
	// IMPORTANT: Reuse this client across credential refreshes to maintain connection pool
	t1 := time.Now()
	purCfg := apiClient.GetConfig()
	httpClient, err := http.CreateOptimizedClient(purCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP client: %w", err)
	}
	log.Printf("[DEBUG] NewS3Client: CreateOptimizedClient took %v", time.Since(t1))

	// Create auto-refreshing credential provider
	// For uploads: fileInfo is nil, so uses user's default storage
	// For downloads: fileInfo may be provided for cross-storage access
	credProvider := credentials.NewRescaleCredentialProvider(apiClient, fileInfo)

	// Wrap with credentials cache for automatic refresh
	credCache := aws.NewCredentialsCache(credProvider, func(o *aws.CredentialsCacheOptions) {
		// Refresh 5 minutes before expiry (credentials expire at ~15 min)
		o.ExpiryWindow = 5 * time.Minute
	})

	// Load AWS config with custom HTTP client and auto-refreshing credentials
	t2 := time.Now()
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(storageInfo.ConnectionSettings.Region),
		config.WithHTTPClient(httpClient),
		config.WithCredentialsProvider(credCache),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	log.Printf("[DEBUG] NewS3Client: LoadDefaultConfig took %v", time.Since(t2))

	client := s3.NewFromConfig(cfg)

	// Get the global credential manager
	credManager := credentials.GetManager(apiClient)
	log.Printf("[DEBUG] NewS3Client: total took %v", time.Since(clientStart))

	return &S3Client{
		client:      client,
		storageInfo: storageInfo,
		credManager: credManager,
		apiClient:   apiClient,  // Store for file-specific credential refresh
		fileInfo:    fileInfo,   // Store for cross-bucket downloads
		httpClient:  httpClient,
	}, nil
}

// Client returns the underlying S3 client.
// Thread-safe: Returns the current client under mutex protection.
func (c *S3Client) Client() *s3.Client {
	c.clientMu.Lock()
	defer c.clientMu.Unlock()
	return c.client
}

// StorageInfo returns the storage configuration.
func (c *S3Client) StorageInfo() *models.StorageInfo {
	return c.storageInfo
}

// Bucket returns the S3 bucket name.
func (c *S3Client) Bucket() string {
	return c.storageInfo.ConnectionSettings.Container
}

// PathBase returns the path prefix for object keys.
func (c *S3Client) PathBase() string {
	return c.storageInfo.ConnectionSettings.PathBase
}

// EnsureFreshCredentials refreshes S3 credentials.
// Uses the credential manager's cache for BOTH user's default storage AND file-specific storage.
// This prevents redundant API calls during concurrent downloads from the same storage.
// Thread-safe and shares credentials across all concurrent operations.
// IMPORTANT: Reuses the existing HTTP client to maintain connection pool.
func (c *S3Client) EnsureFreshCredentials(ctx context.Context) error {
	var s3Creds *models.S3Credentials
	var err error

	// Use credential manager for all credential fetching (both default and file-specific)
	// The manager handles caching keyed by storage ID, avoiding redundant API calls
	if c.fileInfo != nil {
		// Get cached credentials for the specific file's storage
		s3Creds, err = c.credManager.GetS3CredentialsForStorage(ctx, c.fileInfo)
		if err != nil {
			return fmt.Errorf("failed to get file-specific credentials: %w", err)
		}
	} else {
		// Get cached credentials for user's default storage
		s3Creds, err = c.credManager.GetS3Credentials(ctx)
		if err != nil {
			return fmt.Errorf("failed to get credentials: %w", err)
		}
	}

	if s3Creds == nil {
		return fmt.Errorf("received nil S3 credentials")
	}

	// Update S3 client with fresh credentials
	// Lock to prevent concurrent client updates
	c.clientMu.Lock()
	defer c.clientMu.Unlock()

	// IMPORTANT: Reuse existing HTTP client instead of creating new one
	// This preserves the connection pool and prevents TLS handshake overhead
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(c.storageInfo.ConnectionSettings.Region),
		config.WithHTTPClient(c.httpClient), // Reuse existing HTTP client!
		config.WithCredentialsProvider(awscreds.NewStaticCredentialsProvider(
			s3Creds.AccessKeyID,
			s3Creds.SecretKey,
			s3Creds.SessionToken,
		)),
	)
	if err != nil {
		return fmt.Errorf("failed to load AWS config: %w", err)
	}

	c.client = s3.NewFromConfig(cfg)

	return nil
}

// RetryWithBackoff executes a function with exponential backoff retry logic.
// Uses the shared retry package for consistent retry behavior across all operations.
func (c *S3Client) RetryWithBackoff(ctx context.Context, operation string, fn func() error) error {
	retryConfig := http.Config{
		MaxRetries:   constants.MaxRetries,
		InitialDelay: constants.RetryInitialDelay,
		MaxDelay:     constants.RetryMaxDelay,
		CredentialRefresh: func(ctx context.Context) error {
			return c.EnsureFreshCredentials(ctx)
		},
		OnRetry: func(attempt int, err error, errorType http.ErrorType) {
			// Log retry attempts for debugging
			if os.Getenv("DEBUG_RETRY") == "true" {
				log.Printf("[RETRY] %s: attempt %d/%d, error type: %s, error: %v",
					operation, attempt, constants.MaxRetries, http.ErrorTypeName(errorType), err)
			}
		},
	}

	return http.ExecuteWithRetry(ctx, retryConfig, fn)
}

// TraceContext adds HTTP connection tracing when DEBUG_HTTP=true.
// This is useful for debugging connection reuse and TLS handshake overhead.
func TraceContext(ctx context.Context, operation string) context.Context {
	if os.Getenv("DEBUG_HTTP") != "true" {
		return ctx
	}

	var handshakeStart time.Time
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			if info.Reused {
				log.Printf("[HTTP] %s: reused connection", operation)
			} else {
				log.Printf("[HTTP] %s: NEW connection", operation)
			}
		},
		TLSHandshakeStart: func() {
			handshakeStart = time.Now()
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, _ error) {
			log.Printf("[HTTP] %s: TLS handshake took %v", operation, time.Since(handshakeStart))
		},
	})
}

// =============================================================================
// Download Operations (Phase 7F)
// =============================================================================

// HeadObject retrieves object metadata (size, etc.) from S3.
// Uses retry logic with credential refresh.
func (c *S3Client) HeadObject(ctx context.Context, objectKey string) (*s3.HeadObjectOutput, error) {
	var headResp *s3.HeadObjectOutput
	err := c.RetryWithBackoff(ctx, "HeadObject", func() error {
		resp, err := c.Client().HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: aws.String(c.Bucket()),
			Key:    aws.String(objectKey),
		})
		headResp = resp
		return err
	})
	return headResp, err
}

// GetObject downloads an entire object from S3.
// Uses retry logic with credential refresh.
func (c *S3Client) GetObject(ctx context.Context, objectKey string) (*s3.GetObjectOutput, error) {
	var resp *s3.GetObjectOutput
	err := c.RetryWithBackoff(ctx, "GetObject", func() error {
		r, err := c.Client().GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(c.Bucket()),
			Key:    aws.String(objectKey),
		})
		resp = r
		return err
	})
	return resp, err
}

// GetObjectRange downloads a range of bytes from an S3 object.
// Uses retry logic with credential refresh.
func (c *S3Client) GetObjectRange(ctx context.Context, objectKey string, startByte, endByte int64) (*s3.GetObjectOutput, error) {
	rangeHeader := fmt.Sprintf("bytes=%d-%d", startByte, endByte)
	var resp *s3.GetObjectOutput
	err := c.RetryWithBackoff(ctx, fmt.Sprintf("GetObject range %d-%d", startByte, endByte), func() error {
		r, err := c.Client().GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(c.Bucket()),
			Key:    aws.String(objectKey),
			Range:  aws.String(rangeHeader),
		})
		resp = r
		return err
	})
	return resp, err
}
