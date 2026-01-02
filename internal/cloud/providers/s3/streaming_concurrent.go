// Package s3 provides an S3 implementation of the CloudTransfer interface.
// This file implements the StreamingConcurrentUploader, StreamingConcurrentDownloader,
// and StreamingPartDownloader interfaces for concurrent streaming uploads/downloads.
//
// CBC chaining format for Rescale platform compatibility.
// Upload metadata uses `iv` field (like legacy format) instead of formatVersion/fileId/partSize.
// Download supports both legacy and HKDF formats for backward compatibility.
package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

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

// progressReader wraps an io.Reader and reports bytes read to a callback.
// v3.6.3: Enables real-time progress tracking during S3 uploads.
// Uses threshold-based reporting to avoid jumpy progress from many small reads.
type progressReader struct {
	reader      io.Reader
	callback    func(bytesRead int64)
	accumulated int64 // Bytes accumulated since last callback
	threshold   int64 // Report every N bytes (1MB default)
}

// progressReaderThreshold is the minimum bytes to accumulate before reporting.
// 1MB provides smooth progress without excessive updates.
const progressReaderThreshold = 1 * 1024 * 1024 // 1MB

func (pr *progressReader) Read(p []byte) (n int, err error) {
	n, err = pr.reader.Read(p)
	if n > 0 && pr.callback != nil {
		pr.accumulated += int64(n)
		// Report when threshold reached OR on EOF (to flush remaining)
		if pr.accumulated >= pr.threshold || err == io.EOF {
			pr.callback(pr.accumulated)
			pr.accumulated = 0
		}
	}
	return n, err
}

// InitStreamingUpload initializes a multipart upload with streaming encryption.
// Uses CBC chaining format compatible with Rescale platform.
// Metadata stores `iv` (base64) for Rescale decryption compatibility.
func (p *Provider) InitStreamingUpload(ctx context.Context, params transfer.StreamingUploadInitParams) (*transfer.StreamingUpload, error) {
	fileName := filepath.Base(params.LocalPath)
	initStart := time.Now()

	// Get or create S3 client
	t1 := time.Now()
	s3Client, err := p.getOrCreateS3Client(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get S3 client: %w", err)
	}
	log.Printf("[DEBUG] %s: getOrCreateS3Client took %v", fileName, time.Since(t1))
	_ = initStart // use later

	// Generate random suffix for object key
	randomSuffix, err := encryption.GenerateSecureRandomString(22)
	if err != nil {
		return nil, fmt.Errorf("failed to generate random suffix: %w", err)
	}

	// Build object key
	filename := filepath.Base(params.LocalPath)
	objectName := fmt.Sprintf("%s-%s", filename, randomSuffix)
	objectKey := fmt.Sprintf("%s/%s", s3Client.PathBase(), objectName)

	// Calculate part size dynamically based on file size and available threads
	// This optimizes chunk size for memory constraints and throughput
	numThreads := constants.MaxThreadsPerFile // Default thread count for large files
	partSize := resources.CalculateDynamicChunkSize(params.FileSize, numThreads)

	// Create streaming encryption state (CBC chaining)
	encryptState, err := transfer.NewStreamingEncryptionState(partSize)
	if err != nil {
		return nil, fmt.Errorf("failed to create encryption state: %w", err)
	}

	// Create multipart upload on S3 with retry.
	// Metadata uses `iv` field for Rescale compatibility.
	// `streamingformat: cbc` enables streaming download (no temp file).
	t2 := time.Now()
	var createResp *s3.CreateMultipartUploadOutput
	err = s3Client.RetryWithBackoff(ctx, "CreateMultipartUpload", func() error {
		var err error
		createResp, err = s3Client.Client().CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
			Bucket: aws.String(s3Client.Bucket()),
			Key:    aws.String(objectKey),
			Metadata: map[string]string{
				"iv":              encryption.EncodeBase64(encryptState.GetInitialIV()),
				"streamingformat": "cbc",                       // Marks file as CBC-chained streaming
				"partsize":        fmt.Sprintf("%d", partSize), // v4.0.0: Store part size for correct download decryption
			},
		})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create multipart upload: %w", err)
	}
	log.Printf("[DEBUG] %s: CreateMultipartUpload took %v", fileName, time.Since(t2))
	log.Printf("[DEBUG] %s: InitStreamingUpload total took %v", fileName, time.Since(initStart))

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

// UploadStreamingPart encrypts and uploads a single part.
// Uses CBC chaining - parts MUST be uploaded sequentially.
// The orchestrator already calls this sequentially (see upload.go:217).
func (p *Provider) UploadStreamingPart(ctx context.Context, uploadState *transfer.StreamingUpload, partIndex int64, plaintext []byte) (*transfer.PartResult, error) {
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

	// S3 uses 1-based part numbers
	partNumber := int32(partIndex + 1)

	// Create context with timeout (v4.0.4: use centralized constant)
	partCtx, cancel := context.WithTimeout(ctx, constants.PartOperationTimeout)
	defer cancel()

	// Add HTTP tracing if DEBUG_HTTP is enabled
	partCtx = TraceContext(partCtx, fmt.Sprintf("UploadPart %d", partNumber))

	// Upload the part using S3Client
	var uploadResp *s3.UploadPartOutput
	err = providerData.s3Client.RetryWithBackoff(partCtx, fmt.Sprintf("UploadPart %d", partNumber), func() error {
		var err error
		uploadResp, err = providerData.s3Client.Client().UploadPart(partCtx, &s3.UploadPartInput{
			Bucket:        aws.String(providerData.bucket),
			Key:           aws.String(uploadState.StoragePath),
			PartNumber:    aws.Int32(partNumber),
			UploadId:      aws.String(uploadState.UploadID),
			Body:          bytes.NewReader(ciphertext),
			ContentLength: aws.Int64(int64(len(ciphertext))),
		})
		return err
	})

	if err != nil {
		return nil, fmt.Errorf("failed to upload part %d: %w", partNumber, err)
	}

	return &transfer.PartResult{
		PartIndex:  partIndex,
		PartNumber: partNumber,
		ETag:       *uploadResp.ETag,
		Size:       int64(len(plaintext)),
	}, nil
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
// v3.6.3: Now uses progressReader to track bytes in real-time via ByteProgressCallback.
func (p *Provider) UploadCiphertext(ctx context.Context, uploadState *transfer.StreamingUpload, partIndex int64, ciphertext []byte) (*transfer.PartResult, error) {
	providerData, ok := uploadState.ProviderData.(*s3ProviderData)
	if !ok {
		return nil, fmt.Errorf("invalid provider data for S3 streaming upload")
	}

	// S3 uses 1-based part numbers
	partNumber := int32(partIndex + 1)
	uploadStart := time.Now()
	fileName := filepath.Base(uploadState.LocalPath)

	// Create context with timeout (v4.0.4: use centralized constant)
	partCtx, cancel := context.WithTimeout(ctx, constants.PartOperationTimeout)
	defer cancel()

	// Add HTTP tracing if DEBUG_HTTP is enabled
	partCtx = TraceContext(partCtx, fmt.Sprintf("UploadPart %d", partNumber))

	// v3.6.3: Create body reader with progress tracking if callback is set.
	// This enables real-time progress updates during the S3 upload, not just at completion.
	var bodyReader io.Reader = bytes.NewReader(ciphertext)
	if uploadState.ByteProgressCallback != nil {
		bodyReader = &progressReader{
			reader:    bytes.NewReader(ciphertext),
			callback:  uploadState.ByteProgressCallback,
			threshold: progressReaderThreshold,
		}
	}

	// Upload the part using S3Client
	var uploadResp *s3.UploadPartOutput
	err := providerData.s3Client.RetryWithBackoff(partCtx, fmt.Sprintf("UploadPart %d", partNumber), func() error {
		var err error
		uploadResp, err = providerData.s3Client.Client().UploadPart(partCtx, &s3.UploadPartInput{
			Bucket:        aws.String(providerData.bucket),
			Key:           aws.String(uploadState.StoragePath),
			PartNumber:    aws.Int32(partNumber),
			UploadId:      aws.String(uploadState.UploadID),
			Body:          bodyReader,
			ContentLength: aws.Int64(int64(len(ciphertext))),
		})
		return err
	})

	if err != nil {
		return nil, fmt.Errorf("failed to upload part %d: %w", partNumber, err)
	}

	// Log part upload timing for first few parts to diagnose slowness
	if partNumber <= 3 {
		elapsed := time.Since(uploadStart)
		sizeMB := float64(len(ciphertext)) / (1024 * 1024)
		speedMBps := sizeMB / elapsed.Seconds()
		log.Printf("[DEBUG] %s: UploadPart %d completed in %v (%.1f MB at %.1f MB/s)",
			fileName, partNumber, elapsed, sizeMB, speedMBps)
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

	if params.OutputWriter != nil {
		fmt.Fprintf(params.OutputWriter, "Resuming streaming upload: %d parts of %d MB\n",
			totalParts, params.PartSize/(1024*1024))
	}

	return &transfer.StreamingUpload{
		UploadID:     params.UploadID,
		StoragePath:  params.StoragePath,
		MasterKey:    params.MasterKey,
		InitialIV:    params.InitialIV,
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
		// Check if this is a "NoSuchUpload" error (upload expired or doesn't exist)
		// AWS SDK v2 uses smithy error types
		var noSuchUpload *types.NoSuchUpload
		if ok := errors.As(err, &noSuchUpload); ok {
			return false, nil // Upload doesn't exist, but this isn't an error condition
		}
		// Some other error occurred
		return false, fmt.Errorf("failed to validate multipart upload: %w", err)
	}

	return true, nil
}

// =============================================================================
// StreamingConcurrentDownloader Interface Implementation
// Supports both legacy (IV in metadata) and HKDF (formatVersion/fileId/partSize) formats.
// =============================================================================

// DetectFormat detects the encryption format from S3 object metadata.
// Returns: formatVersion (0=legacy, 1=HKDF streaming, 2=CBC streaming), fileId (base64), partSize, iv, error
// Both new uploads (IV/CBC) and old uploads (HKDF) are supported for download.
func (p *Provider) DetectFormat(ctx context.Context, remotePath string) (int, string, int64, []byte, error) {
	// Get or create S3 client
	s3Client, err := p.getOrCreateS3Client(ctx)
	if err != nil {
		return 0, "", 0, nil, fmt.Errorf("failed to get S3 client: %w", err)
	}

	// Get object metadata using S3Client
	var headResp *s3.HeadObjectOutput
	err = s3Client.RetryWithBackoff(ctx, "HeadObject", func() error {
		var err error
		headResp, err = s3Client.Client().HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: aws.String(s3Client.Bucket()),
			Key:    aws.String(remotePath),
		})
		return err
	})
	if err != nil {
		return 0, "", 0, nil, fmt.Errorf("failed to get object metadata: %w", err)
	}

	// Check for HKDF streaming format metadata (S3 lowercases all metadata keys)
	// This is for backward compatibility with files uploaded before v3.2.0
	if fv, ok := headResp.Metadata["formatversion"]; ok && fv == "1" {
		fileId := headResp.Metadata["fileid"]
		if fileId == "" {
			return 0, "", 0, nil, fmt.Errorf("streaming format missing fileId in metadata")
		}

		partSizeStr := headResp.Metadata["partsize"]
		if partSizeStr == "" {
			return 0, "", 0, nil, fmt.Errorf("streaming format missing partSize in metadata")
		}

		var partSize int64
		if _, err := fmt.Sscanf(partSizeStr, "%d", &partSize); err != nil {
			return 0, "", 0, nil, fmt.Errorf("invalid partSize in metadata: %s", partSizeStr)
		}

		return 1, fileId, partSize, nil, nil
	}

	// Check for CBC streaming format (v3.2.4+)
	// This indicates the file was uploaded with rescale-int using CBC chaining
	// and can be downloaded without a temp file using sequential part decryption
	var iv []byte
	if ivStr, ok := headResp.Metadata["iv"]; ok && ivStr != "" {
		iv, err = encryption.DecodeBase64(ivStr)
		if err != nil {
			// IV decode failed - might be provided via FileInfo instead
			iv = nil
		}
	}

	if sf, ok := headResp.Metadata["streamingformat"]; ok && sf == "cbc" {
		// CBC streaming format - uploaded by rescale-int v3.2.4+
		// Can use streaming download (no temp file) with sequential part decryption
		// v4.0.0: Read partSize from metadata. Return 0 if not present so downloader
		// can calculate the correct size from file size (backward compatibility).
		var partSize int64 = 0 // 0 means "calculate from file size"
		if ps, ok := headResp.Metadata["partsize"]; ok && ps != "" {
			if parsed, parseErr := strconv.ParseInt(ps, 10, 64); parseErr == nil && parsed > 0 {
				partSize = parsed
			}
		}
		return 2, "", partSize, iv, nil
	}

	// Legacy format - file uploaded by Rescale platform or older rescale-int
	// Must use downloadLegacy() with temp file
	return 0, "", 0, iv, nil
}

// DownloadStreaming downloads and decrypts a file using HKDF streaming format (v1).
// This is for backward compatibility with files uploaded before v3.2.0.
// Format metadata (fileId, partSize) is read from S3 object metadata.
func (p *Provider) DownloadStreaming(ctx context.Context, remotePath, localPath string, masterKey []byte, progressCallback cloud.ProgressCallback) error {
	// Get or create S3 client
	s3Client, err := p.getOrCreateS3Client(ctx)
	if err != nil {
		return fmt.Errorf("failed to get S3 client: %w", err)
	}

	// Report 0% at start
	if progressCallback != nil {
		progressCallback(0.0)
	}

	// Ensure fresh credentials
	if err := s3Client.EnsureFreshCredentials(ctx); err != nil {
		return fmt.Errorf("failed to refresh credentials: %w", err)
	}

	// Get object metadata for format detection and size
	headResp, err := s3Client.HeadObject(ctx, remotePath)
	if err != nil {
		return fmt.Errorf("failed to get object metadata: %w", err)
	}

	// Get HKDF streaming format metadata (S3 lowercases keys)
	fileIdStr := headResp.Metadata["fileid"]
	if fileIdStr == "" {
		return fmt.Errorf("streaming format missing fileId in metadata")
	}
	fileId, err := encryption.DecodeBase64(fileIdStr)
	if err != nil {
		return fmt.Errorf("failed to decode fileId: %w", err)
	}

	partSizeStr := headResp.Metadata["partsize"]
	if partSizeStr == "" {
		return fmt.Errorf("streaming format missing partSize in metadata")
	}
	var partSize int64
	if _, err := fmt.Sscanf(partSizeStr, "%d", &partSize); err != nil {
		return fmt.Errorf("invalid partSize in metadata: %s", partSizeStr)
	}

	encryptedSize := *headResp.ContentLength

	// Create HKDF streaming decryptor (legacy format)
	decryptor, err := encryption.NewStreamingDecryptor(masterKey, fileId, partSize)
	if err != nil {
		return fmt.Errorf("failed to create streaming decryptor: %w", err)
	}

	// Calculate encrypted part size (includes PKCS7 padding overhead per part for HKDF format)
	encryptedPartSize := encryption.CalculateEncryptedPartSize(partSize)

	// Calculate number of parts
	numParts := (encryptedSize + encryptedPartSize - 1) / encryptedPartSize

	// Create output file (plaintext, no temp file needed!)
	outFile, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer outFile.Close()

	// Download and decrypt each part
	var downloadedBytes int64 = 0
	for partIndex := int64(0); partIndex < numParts; partIndex++ {
		// Calculate byte range for this encrypted part
		startByte := partIndex * encryptedPartSize
		endByte := startByte + encryptedPartSize - 1
		if endByte >= encryptedSize {
			endByte = encryptedSize - 1
		}

		// Download this part's ciphertext
		resp, err := s3Client.GetObjectRange(ctx, remotePath, startByte, endByte)
		if err != nil {
			return fmt.Errorf("failed to download part %d: %w", partIndex, err)
		}

		// Read encrypted part data
		ciphertext, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("failed to read part %d: %w", partIndex, err)
		}

		// Decrypt this part (HKDF format - each part has own key/IV)
		plaintext, err := decryptor.DecryptPart(partIndex, ciphertext)
		if err != nil {
			return fmt.Errorf("failed to decrypt part %d: %w", partIndex, err)
		}

		// Write plaintext to output file
		if _, err := outFile.Write(plaintext); err != nil {
			return fmt.Errorf("failed to write part %d: %w", partIndex, err)
		}

		downloadedBytes += int64(len(ciphertext))
		if progressCallback != nil && encryptedSize > 0 {
			progressCallback(float64(downloadedBytes) / float64(encryptedSize))
		}
	}

	// Report 100% at end
	if progressCallback != nil {
		progressCallback(1.0)
	}

	return nil
}

// =============================================================================
// StreamingPartDownloader Interface Implementation
// These methods enable concurrent streaming downloads by allowing the orchestrator
// to download individual encrypted parts in parallel.
// =============================================================================

// GetEncryptedSize returns the total encrypted size of the file in S3.
// This is used by the concurrent download orchestrator to calculate the number of parts.
func (p *Provider) GetEncryptedSize(ctx context.Context, remotePath string) (int64, error) {
	// Get or create S3 client
	s3Client, err := p.getOrCreateS3Client(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get S3 client: %w", err)
	}

	// Ensure fresh credentials
	if err := s3Client.EnsureFreshCredentials(ctx); err != nil {
		return 0, fmt.Errorf("failed to refresh credentials: %w", err)
	}

	// Get object metadata
	headResp, err := s3Client.HeadObject(ctx, remotePath)
	if err != nil {
		return 0, fmt.Errorf("failed to get object metadata: %w", err)
	}

	return *headResp.ContentLength, nil
}

// DownloadEncryptedRange downloads a specific byte range of the encrypted file from S3.
// This is used by the concurrent download orchestrator to download individual parts.
// The range is inclusive: [offset, offset+length).
// v4.0.0: progressCallback (optional) is called with bytes downloaded for smooth progress.
func (p *Provider) DownloadEncryptedRange(ctx context.Context, remotePath string, offset, length int64, progressCallback func(int64)) ([]byte, error) {
	// Get or create S3 client
	s3Client, err := p.getOrCreateS3Client(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get S3 client: %w", err)
	}

	// Download the specified range
	resp, err := s3Client.GetObjectRange(ctx, remotePath, offset, offset+length-1)
	if err != nil {
		return nil, fmt.Errorf("failed to download range [%d-%d]: %w", offset, offset+length-1, err)
	}
	defer resp.Body.Close()

	// v4.0.0: Wrap response body with progress tracking for smooth download progress.
	// This matches upload behavior where progressReader reports bytes as they stream.
	var reader io.Reader = resp.Body
	if progressCallback != nil {
		reader = &progressReader{
			reader:    resp.Body,
			callback:  progressCallback,
			threshold: progressReaderThreshold,
		}
	}

	// Read the data (progress reported during read if callback provided)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read range data: %w", err)
	}

	return data, nil
}
