// Package s3 provides an S3 implementation of the CloudTransfer interface.
// This file implements the StreamingConcurrentUploader, StreamingConcurrentDownloader,
// and StreamingPartDownloader interfaces for concurrent streaming uploads/downloads.
//
// CBC chaining format for Rescale platform compatibility.
// Upload metadata uses `iv` field (like legacy format) instead of formatVersion/fileId/partSize.
// Download supports both legacy and HKDF formats for backward compatibility.
package s3

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/cloud/transfer"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/crypto" // package name is 'encryption'
	"github.com/rescale/rescale-int/internal/resources"
)

// Verify that Provider implements StreamingConcurrentUploader, StreamingConcurrentDownloader,
// and StreamingPartDownloader interfaces
var _ transfer.StreamingConcurrentUploader = (*Provider)(nil)
var _ transfer.StreamingConcurrentDownloader = (*Provider)(nil)
var _ transfer.StreamingPartDownloader = (*Provider)(nil)

// UploadLimits reports S3's multipart ceilings. Part numbers stop at 10,000 and
// a single part may not exceed 5 GB; the planner works in plaintext, so the size
// it reports leaves room for the padding CBC adds to the final part.
func (p *Provider) UploadLimits() resources.UploadLimits {
	return resources.UploadLimits{
		StorageType: p.StorageType(),
		MaxParts:    constants.MaxS3UploadParts,
		MaxPartSize: constants.MaxS3PlaintextPartSize,
	}
}

// InitStreamingUpload initializes a multipart upload with streaming encryption.
// Uses CBC chaining format compatible with Rescale platform.
// Metadata stores `iv` (base64) for Rescale decryption compatibility.
func (p *Provider) InitStreamingUpload(ctx context.Context, params transfer.StreamingUploadInitParams) (*transfer.StreamingUpload, error) {
	// Get or create S3 client
	s3Client, err := p.getOrCreateS3Client(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get S3 client: %w", err)
	}

	// Generate random suffix for object key
	randomSuffix, err := encryption.GenerateSecureRandomString(22)
	if err != nil {
		return nil, fmt.Errorf("failed to generate random suffix: %w", err)
	}

	// Build object key
	filename := filepath.Base(params.LocalPath)
	objectName := fmt.Sprintf("%s-%s", filename, randomSuffix)
	objectKey := fmt.Sprintf("%s/%s", s3Client.PathBase(), objectName)

	// Part size comes from the caller's upload plan, which keeps the part count
	// under MaxS3UploadParts as well as within the memory budget.
	partSize, err := params.PartSize(p.UploadLimits())
	if err != nil {
		return nil, err
	}

	// Create streaming encryption state (CBC chaining)
	encryptState, err := transfer.NewStreamingEncryptionState(partSize)
	if err != nil {
		return nil, fmt.Errorf("failed to create encryption state: %w", err)
	}

	// Create multipart upload on S3 with retry.
	// Metadata uses `iv` field for Rescale compatibility.
	// `streamingformat: cbc` enables streaming download (no temp file).
	var createResp *s3.CreateMultipartUploadOutput
	err = s3Client.RetryWithBackoff(ctx, "CreateMultipartUpload", func() error {
		var err error
		createResp, err = s3Client.Client().CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
			Bucket: aws.String(s3Client.Bucket()),
			Key:    aws.String(objectKey),
			Metadata: map[string]string{
				"iv":              encryption.EncodeBase64(encryptState.GetInitialIV()),
				"streamingformat": "cbc",                       // Marks file as CBC-chained streaming
				"partsize":        fmt.Sprintf("%d", partSize), // Required for correct download decryption
			},
		})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create multipart upload: %w", err)
	}

	// Calculate total parts
	totalParts := transfer.CalculateTotalParts(params.FileSize, partSize)

	// Note: "Initialized streaming upload" message removed to prevent visual artifacts
	// during concurrent multi-file uploads. The message was low-value information
	// that caused ghost progress bar copies when interleaved with mpb output.
	_ = params.OutputWriter // Suppress unused warning - writer still used for other messages

	return &transfer.StreamingUpload{
		UploadID:     *createResp.UploadId,
		StoragePath:  objectKey,
		MasterKey:    encryptState.GetKey(),
		InitialIV:    encryptState.GetInitialIV(),
		EncryptState: encryptState,
		FileID:       nil, // Not used in CBC format
		PartSize:     partSize,
		LocalPath:    params.LocalPath,
		TotalSize:    params.FileSize,
		TotalParts:   totalParts,
		RandomSuffix: randomSuffix,
		ProviderData: &s3ProviderData{
			bucket:       s3Client.Bucket(),
			encryptState: encryptState,
			s3Client:     s3Client,
		},
	}, nil
}

// s3ProviderData contains S3-specific data for the upload.
type s3ProviderData struct {
	bucket       string
	encryptState *transfer.StreamingEncryptionState
	s3Client     *S3Client
}

// EncryptStreamingPart encrypts plaintext and returns ciphertext.
// Must be called sequentially due to CBC chaining constraint.
// Separated from upload to enable pipelining.
func (p *Provider) EncryptStreamingPart(ctx context.Context, uploadState *transfer.StreamingUpload, partIndex int64, plaintext []byte) ([]byte, error) {
	providerData, ok := uploadState.ProviderData.(*s3ProviderData)
	if !ok {
		return nil, fmt.Errorf("invalid provider data for S3 streaming upload")
	}

	// Determine if this is the final part
	isFinal := (partIndex == uploadState.TotalParts-1)

	// Encrypt this part with CBC chaining
	ciphertext, err := providerData.encryptState.EncryptPart(plaintext, isFinal)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt part %d: %w", partIndex, err)
	}

	return ciphertext, nil
}

// UploadCiphertext uploads already-encrypted data to cloud storage.
// Can be called concurrently with EncryptStreamingPart (pipelining).
// Separated from encryption to enable pipelining.
// The body is an io.ReadSeeker so the AWS SDK can rewind the stream on
// transient errors (fixes "stream not seekable" failures), and comes from the
// attempt tracker so an outer retry does not report the same bytes twice.
func (p *Provider) UploadCiphertext(ctx context.Context, uploadState *transfer.StreamingUpload, partIndex int64, ciphertext []byte) (*transfer.PartResult, error) {
	providerData, ok := uploadState.ProviderData.(*s3ProviderData)
	if !ok {
		return nil, fmt.Errorf("invalid provider data for S3 streaming upload")
	}

	// S3 uses 1-based part numbers
	partNumber := int32(partIndex + 1)

	partCtx, cancel := context.WithTimeout(ctx, constants.PartOperationTimeout)
	defer cancel()

	// Add HTTP tracing if DEBUG_HTTP is enabled
	partCtx = TraceContext(partCtx, fmt.Sprintf("UploadPart %d", partNumber))

	// Upload the part using S3Client.
	var uploadResp *s3.UploadPartOutput
	attempt := transfer.NewUploadAttemptProgress(uploadState.ByteProgressCallback)
	err := providerData.s3Client.RetryWithBackoff(partCtx, fmt.Sprintf("UploadPart %d", partNumber), func() error {
		// Fresh reader per attempt, because the SDK cannot re-send a drained
		// body. Getting it from the attempt tracker is what withdraws the
		// previous attempt's reported bytes: nothing else can, since that
		// reader is gone by now.
		var err error
		uploadResp, err = providerData.s3Client.Client().UploadPart(partCtx, &s3.UploadPartInput{
			Bucket:        aws.String(providerData.bucket),
			Key:           aws.String(uploadState.StoragePath),
			PartNumber:    aws.Int32(partNumber),
			UploadId:      aws.String(uploadState.UploadID),
			Body:          attempt.NewReader(ciphertext),
			ContentLength: aws.Int64(int64(len(ciphertext))),
		})
		return err
	})

	if err != nil {
		// The part is not going up: its last attempt's bytes have to come back
		// out of the total.
		attempt.Rollback()
		return nil, fmt.Errorf("failed to upload part %d: %w", partNumber, err)
	}

	return &transfer.PartResult{
		PartIndex:  partIndex,
		PartNumber: partNumber,
		ETag:       *uploadResp.ETag,
		Size:       int64(len(ciphertext)), // Note: ciphertext size, not plaintext
	}, nil
}

// CompleteStreamingUpload completes the multipart upload.
// Returns IV for Rescale-compatible format (FormatVersion=0).
func (p *Provider) CompleteStreamingUpload(ctx context.Context, uploadState *transfer.StreamingUpload, parts []*transfer.PartResult) (*cloud.UploadResult, error) {
	providerData, ok := uploadState.ProviderData.(*s3ProviderData)
	if !ok {
		return nil, fmt.Errorf("invalid provider data for S3 streaming upload")
	}

	// Convert parts to S3 format
	completedParts := make([]types.CompletedPart, len(parts))
	for i, part := range parts {
		completedParts[i] = types.CompletedPart{
			ETag:       aws.String(part.ETag),
			PartNumber: aws.Int32(part.PartNumber),
		}
	}

	// Complete the multipart upload using S3Client
	err := providerData.s3Client.RetryWithBackoff(ctx, "CompleteMultipartUpload", func() error {
		_, err := providerData.s3Client.Client().CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
			Bucket:   aws.String(providerData.bucket),
			Key:      aws.String(uploadState.StoragePath),
			UploadId: aws.String(uploadState.UploadID),
			MultipartUpload: &types.CompletedMultipartUpload{
				Parts: completedParts,
			},
		})
		return err
	})

	if err != nil {
		return nil, fmt.Errorf("failed to complete multipart upload: %w", err)
	}

	// Return IV for Rescale-compatible format
	return &cloud.UploadResult{
		StoragePath:   uploadState.StoragePath,
		EncryptionKey: uploadState.MasterKey,
		IV:            uploadState.InitialIV, // IV for Rescale compatibility
		FormatVersion: 0,                     // Legacy format (uses IV in metadata)
		FileID:        "",                    // Not used in CBC format
		PartSize:      uploadState.PartSize,
	}, nil
}

// AbortStreamingUpload aborts a streaming upload and cleans up resources.
func (p *Provider) AbortStreamingUpload(ctx context.Context, uploadState *transfer.StreamingUpload) error {
	providerData, ok := uploadState.ProviderData.(*s3ProviderData)
	if !ok {
		return fmt.Errorf("invalid provider data for S3 streaming upload")
	}

	_, err := providerData.s3Client.Client().AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(providerData.bucket),
		Key:      aws.String(uploadState.StoragePath),
		UploadId: aws.String(uploadState.UploadID),
	})

	if err != nil {
		return fmt.Errorf("failed to abort multipart upload: %w", err)
	}

	return nil
}

// AbortUploadByID discards a multipart upload addressed only by what a resume
// state records about it. AbortStreamingUpload needs a handle, and building one
// takes the encryption parameters a resume needs — which a state damaged enough
// to be abandoned may not have. S3 needs neither to drop an upload: the object
// key and the upload ID are the whole identity.
func (p *Provider) AbortUploadByID(ctx context.Context, uploadID, storagePath string) error {
	if uploadID == "" || storagePath == "" {
		// A state that names no multipart upload has nothing on the backend to
		// retire — an upload that never got past creation, or an Azure state.
		return nil
	}

	s3Client, err := p.getOrCreateS3Client(ctx)
	if err != nil {
		return fmt.Errorf("failed to get S3 client: %w", err)
	}

	_, err = s3Client.Client().AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(s3Client.Bucket()),
		Key:      aws.String(storagePath),
		UploadId: aws.String(uploadID),
	})
	if err != nil {
		if isNoSuchUpload(err) {
			// Already gone, which is the state we were asking for.
			return nil
		}
		return fmt.Errorf("failed to abort multipart upload: %w", err)
	}

	return nil
}

// InitStreamingUploadFromState resumes a streaming upload with existing encryption params.
// Uses CBC chaining with InitialIV and CurrentIV for resume support.
func (p *Provider) InitStreamingUploadFromState(ctx context.Context, params transfer.StreamingUploadResumeParams) (*transfer.StreamingUpload, error) {
	// Get or create S3 client
	s3Client, err := p.getOrCreateS3Client(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get S3 client: %w", err)
	}

	// Create encryption state from existing keys using CBC chaining with InitialIV and CurrentIV
	var encryptState *transfer.StreamingEncryptionState
	if params.InitialIV != nil && params.CurrentIV != nil {
		// CBC format resume
		encryptState, err = transfer.NewStreamingEncryptionStateFromKey(
			params.MasterKey, params.InitialIV, params.CurrentIV, params.PartSize)
	} else {
		// Cannot resume legacy HKDF format with new code - start fresh
		return nil, fmt.Errorf("cannot resume legacy HKDF upload with v3.2.0; please restart upload")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create encryption state from resume: %w", err)
	}

	// Calculate total parts
	totalParts := transfer.CalculateTotalParts(params.FileSize, params.PartSize)

	// params.CompletedParts is deliberately unused: S3 addresses a staged part
	// by its part number, so the parts the first attempt uploaded are already
	// where CompleteMultipartUpload will look for them. Azure, which commits a
	// list of identifiers it was given, does have to restore them.

	if params.OutputWriter != nil {
		fmt.Fprintf(params.OutputWriter, "Resuming streaming upload: %d parts of %d MB\n",
			totalParts, params.PartSize/(1024*1024))
	}

	return &transfer.StreamingUpload{
		UploadID:     params.UploadID,
		StoragePath:  params.StoragePath,
		MasterKey:    params.MasterKey,
		InitialIV:    params.InitialIV,
		EncryptState: encryptState,
		FileID:       nil, // Not used in CBC format
		PartSize:     params.PartSize,
		LocalPath:    params.LocalPath,
		TotalSize:    params.FileSize,
		TotalParts:   totalParts,
		RandomSuffix: params.RandomSuffix,
		ProviderData: &s3ProviderData{
			bucket:       s3Client.Bucket(),
			encryptState: encryptState,
			s3Client:     s3Client,
		},
	}, nil
}

// ValidateStreamingUploadExists checks if a streaming upload can be resumed.
// For S3: calls ListParts to verify multipart upload still exists.
// Returns (exists, error) where exists=false means upload expired and should start fresh.
func (p *Provider) ValidateStreamingUploadExists(ctx context.Context, uploadID, storagePath string) (bool, error) {
	// Get or create S3 client
	s3Client, err := p.getOrCreateS3Client(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get S3 client: %w", err)
	}

	// Try to list parts - if the upload doesn't exist, S3 returns NoSuchUpload error
	_, err = s3Client.Client().ListParts(ctx, &s3.ListPartsInput{
		Bucket:   aws.String(s3Client.Bucket()),
		Key:      aws.String(storagePath),
		UploadId: aws.String(uploadID),
	})

	if err != nil {
		if isNoSuchUpload(err) {
			return false, nil // Upload doesn't exist, but this isn't an error condition
		}
		// Some other error occurred
		return false, fmt.Errorf("failed to validate multipart upload: %w", err)
	}

	return true, nil
}

// apiErrorCode is the code-carrying part of the SDK's error types. It is
// declared here rather than pulled from smithy-go because the code is all this
// needs, and both the modelled errors and the generic fallback expose it.
type apiErrorCode interface {
	ErrorCode() string
}

// isNoSuchUpload reports the one answer that is not a failure: the multipart
// upload is not there any more, so there is nothing to resume or abort.
//
// The code has to be read, not just the type. Only the operations whose model
// declares NoSuchUpload deserialize it into *types.NoSuchUpload — AbortMultipartUpload
// does, ListParts does not, and reports exactly the same condition as a generic
// API error carrying the code. Matching only the type meant the resume check
// turned a vanished upload into a hard error, which failed the upload instead of
// retiring the state — so every later attempt failed the same way.
func isNoSuchUpload(err error) bool {
	var noSuchUpload *types.NoSuchUpload
	if errors.As(err, &noSuchUpload) {
		return true
	}
	var apiErr apiErrorCode
	return errors.As(err, &apiErr) && apiErr.ErrorCode() == "NoSuchUpload"
}

// =============================================================================
// StreamingConcurrentDownloader Interface Implementation
// Supports both legacy (IV in metadata) and HKDF (formatVersion/fileId/partSize) formats.
// =============================================================================

// DetectFormat detects the encryption format from S3 object metadata.
// Returns: formatVersion (0=legacy, 1=HKDF streaming, 2=CBC streaming), fileId (base64), partSize, iv, error
// Both new uploads (IV/CBC) and old uploads (HKDF) are supported for download.
func (p *Provider) DetectFormat(ctx context.Context, remotePath string) (int, string, int64, []byte, error) {
	s3Client, err := p.getOrCreateS3Client(ctx)
	if err != nil {
		return 0, "", 0, nil, fmt.Errorf("failed to get S3 client: %w", err)
	}

	headResp, err := s3Client.HeadObject(ctx, remotePath)
	if err != nil {
		return 0, "", 0, nil, fmt.Errorf("failed to get object metadata: %w", err)
	}

	format, err := transfer.ParseObjectFormat(transfer.NormalizeMetadata(headResp.Metadata))
	if err != nil {
		return 0, "", 0, nil, err
	}
	return format.Version, format.FileID, format.PartSize, format.IV, nil
}

// DownloadStreaming downloads and decrypts a file using HKDF streaming format (v1).
// This is for backward compatibility with files uploaded before v3.2.0.
// Format metadata (fileId, partSize) is read from S3 object metadata.
func (p *Provider) DownloadStreaming(ctx context.Context, remotePath, localPath string, masterKey []byte, progressCallback cloud.ProgressCallback) error {
	s3Client, err := p.getOrCreateS3Client(ctx)
	if err != nil {
		return fmt.Errorf("failed to get S3 client: %w", err)
	}

	return transfer.DownloadHKDFStream(ctx, transfer.HKDFStreamParams{
		LocalPath:        localPath,
		MasterKey:        masterKey,
		Retry:            s3Client.RetryWithBackoff,
		Refresh:          s3Client.EnsureFreshCredentials,
		ProgressCallback: progressCallback,
		Stat: func(statCtx context.Context) (int64, map[string]string, error) {
			headResp, err := s3Client.HeadObject(statCtx, remotePath)
			if err != nil {
				return 0, nil, fmt.Errorf("failed to get object metadata: %w", err)
			}
			return *headResp.ContentLength, transfer.NormalizeMetadata(headResp.Metadata), nil
		},
		// GetObjectRangeOnce is the non-retrying variant: the shared driver owns
		// the retry loop and the per-attempt timeout.
		//
		// Every range has to come back carrying the same ETag, so an object
		// replaced while this download runs aborts it instead of writing a file
		// stitched from two versions. The pin starts empty and the first range
		// sets it, rather than being seeded from the Stat above: the driver
		// refreshes credentials before it stats, and doing our own HEAD first
		// would move that call ahead of the refresh. The window that leaves —
		// a replacement between the Stat and the first range — is not silent
		// anyway, because the fileId this format derives its part keys from
		// comes from the Stat, and the new object's parts will not decrypt
		// under the old one's.
		Open: transfer.PinObjectVersion(rangeReaderWithETag(s3Client, remotePath), ""),
	})
}

// =============================================================================
// StreamingPartDownloader Interface Implementation
// These methods enable concurrent streaming downloads by allowing the orchestrator
// to download individual encrypted parts in parallel.
// =============================================================================

// GetEncryptedSize returns the total encrypted size of the file in S3, and the
// ETag of the object it measured, which the parts of that download are pinned to.
// This is used by the concurrent download orchestrator to calculate the number of parts.
func (p *Provider) GetEncryptedSize(ctx context.Context, remotePath string) (int64, string, error) {
	// Get or create S3 client
	s3Client, err := p.getOrCreateS3Client(ctx)
	if err != nil {
		return 0, "", fmt.Errorf("failed to get S3 client: %w", err)
	}

	// Ensure fresh credentials
	if err := s3Client.EnsureFreshCredentials(ctx); err != nil {
		return 0, "", fmt.Errorf("failed to refresh credentials: %w", err)
	}

	// Get object metadata
	headResp, err := s3Client.HeadObject(ctx, remotePath)
	if err != nil {
		return 0, "", fmt.Errorf("failed to get object metadata: %w", err)
	}

	etag := ""
	if headResp.ETag != nil {
		etag = *headResp.ETag
	}

	return *headResp.ContentLength, etag, nil
}

// DownloadEncryptedRange downloads a specific byte range of the encrypted file from S3.
// This is used by the concurrent download orchestrator to download individual parts.
// The range is inclusive: [offset, offset+length).
// progressCallback (optional) is called with bytes downloaded for smooth progress.
// Wraps request+read+close in single retry with progress rollback on failure.
func (p *Provider) DownloadEncryptedRange(ctx context.Context, remotePath string, offset, length int64, version string, progressCallback func(int64)) ([]byte, error) {
	// Get or create S3 client
	s3Client, err := p.getOrCreateS3Client(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get S3 client: %w", err)
	}

	// GetObjectRangeOnce is the non-retrying variant: FetchRangeWithRetry owns
	// the retry loop, the per-attempt timeout, and the progress rollback.
	//
	// The range has to come back carrying the version the caller pinned to, so
	// an object replaced part way through a download aborts it instead of
	// having parts of two objects decrypted into one file. An empty version
	// leaves the range unpinned, for a backend that reports no ETag at all.
	return transfer.FetchRangeWithRetry(ctx, s3Client.RetryWithBackoff, offset, length, progressCallback,
		transfer.PinObjectVersion(rangeReaderWithETag(s3Client, remotePath), version))
}
