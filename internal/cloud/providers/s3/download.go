// Package s3 provides an S3 implementation of the CloudTransfer interface.
// This file implements the LegacyDownloader interface for legacy (v0) format downloads.
//
// Uses S3Client directly for all operations.
// Note: StreamingConcurrentDownloader (DetectFormat, DownloadStreaming) is in
// streaming_concurrent.go.
package s3

import (
	"context"
	"fmt"
	"io"

	"github.com/rescale/rescale-int/internal/cloud/transfer"
	"github.com/rescale/rescale-int/internal/constants"
	internaltransfer "github.com/rescale/rescale-int/internal/transfer"
)

// Verify that Provider implements LegacyDownloader
var _ transfer.LegacyDownloader = (*Provider)(nil)

// DownloadEncryptedFile downloads an encrypted file (legacy v0 format) to a local path.
// This implements the LegacyDownloader interface.
// The orchestrator handles decryption; this method just downloads the encrypted bytes.
// Uses S3Client with file-specific credentials for cross-storage downloads.
func (p *Provider) DownloadEncryptedFile(ctx context.Context, params transfer.LegacyDownloadParams) error {
	// Get or create S3 client with file-specific credentials (for cross-storage downloads)
	s3Client, err := p.getOrCreateS3ClientForFile(ctx, params.FileInfo)
	if err != nil {
		return fmt.Errorf("failed to get S3 client: %w", err)
	}

	// Ensure fresh credentials
	if err := s3Client.EnsureFreshCredentials(ctx); err != nil {
		return fmt.Errorf("failed to refresh credentials: %w", err)
	}

	// Get file size from params, or fetch from S3 if not provided
	fileSize := params.FileSize
	if fileSize == 0 {
		// Fetch file size from S3 metadata
		headResp, err := s3Client.HeadObject(ctx, params.RemotePath)
		if err != nil {
			return fmt.Errorf("failed to get object metadata: %w", err)
		}
		fileSize = *headResp.ContentLength
	}

	// Choose download method based on file size and transfer handle
	if fileSize > constants.MultipartThreshold {
		// Use chunked download for large files
		if params.TransferHandle != nil && params.TransferHandle.GetThreads() > 1 {
			// Concurrent chunked download
			return p.downloadChunkedConcurrent(
				ctx,
				s3Client,
				params.RemotePath,
				params.EncryptedPath,
				fileSize,
				params.ProgressCallback,
				params.TransferHandle,
			)
		}
		// Sequential chunked download
		return p.downloadChunkedWithProgress(
			ctx,
			s3Client,
			params.RemotePath,
			params.EncryptedPath,
			fileSize,
			params.ProgressCallback,
		)
	}

	// Small file: single download
	return p.downloadSingleWithProgress(
		ctx,
		s3Client,
		params.RemotePath,
		params.EncryptedPath,
		params.ProgressCallback,
		fileSize,
	)
}

// downloadSingleWithProgress downloads a file in a single GET request with progress callback.
// Uses S3Client directly.
func (p *Provider) downloadSingleWithProgress(ctx context.Context, s3Client *S3Client, objectKey, localPath string, progressCallback func(float64), totalSize int64) error {
	// Get object
	resp, err := s3Client.GetObject(ctx, objectKey)
	if err != nil {
		return fmt.Errorf("failed to get object: %w", err)
	}
	defer resp.Body.Close()

	return transfer.WriteBodyWithProgress(resp.Body, localPath, progressCallback, totalSize)
}

// downloadChunkedWithProgress downloads a file in chunks with progress callback.
// Uses S3Client directly.
// Wraps request+read+close in single retry to handle mid-transfer proxy failures.
func (p *Provider) downloadChunkedWithProgress(ctx context.Context, s3Client *S3Client, objectKey, localPath string, totalSize int64, progressCallback func(float64)) error {
	// GetObjectRangeOnce is the non-retrying variant, so the shared helper's
	// per-chunk retry is the only retry.
	return transfer.DownloadChunkedToFile(ctx, s3Client.RetryWithBackoff, localPath, totalSize, progressCallback,
		func(attemptCtx context.Context, offset, length int64) (io.ReadCloser, error) {
			resp, err := s3Client.GetObjectRangeOnce(attemptCtx, objectKey, offset, offset+length-1, "")
			if err != nil {
				return nil, err
			}
			return resp.Body, nil
		})
}

// downloadChunkedConcurrent downloads a file using concurrent range requests.
// The shared driver owns the chunking, resume state, worker pool and writes; this
// wrapper supplies only the S3 calls.
func (p *Provider) downloadChunkedConcurrent(
	ctx context.Context,
	s3Client *S3Client,
	objectKey, localPath string,
	totalSize int64,
	progressCallback func(float64),
	transferHandle *internaltransfer.Transfer,
) error {
	// If no transfer handle provided or only 1 thread, fall back to sequential
	if transferHandle == nil || transferHandle.GetThreads() <= 1 {
		return p.downloadChunkedWithProgress(ctx, s3Client, objectKey, localPath, totalSize, progressCallback)
	}
	defer transferHandle.Complete()

	// The ETag pins the object for resume validation: a resume state naming a
	// different one is discarded. Range requests do not send it (see below).
	var etag string
	headResp, err := s3Client.HeadObject(ctx, objectKey)
	if err != nil {
		return fmt.Errorf("failed to get object metadata: %w", err)
	}
	if headResp.ETag != nil {
		etag = *headResp.ETag
	}

	return transfer.DownloadChunkedConcurrent(ctx, transfer.ChunkedConcurrentParams{
		RemotePath:       objectKey,
		LocalPath:        localPath,
		TotalSize:        totalSize,
		Concurrency:      transferHandle.GetThreads(),
		StorageType:      "S3Storage",
		ObjectETag:       etag,
		Retry:            s3Client.RetryWithBackoff,
		ProgressCallback: progressCallback,
		// GetObjectRangeOnce is the non-retrying variant, so the driver's
		// per-chunk retry is the only retry.
		Open: func(attemptCtx context.Context, offset, length int64) (io.ReadCloser, error) {
			// Per-request If-Match is deliberately not sent: intercepting
			// proxies (see the open ITAR issue) can mangle ETag headers into
			// spurious 412s. Stale objects are still caught by the resume-state
			// ETag validation above and the checksum gate after download.
			resp, err := s3Client.GetObjectRangeOnce(attemptCtx, objectKey, offset, offset+length-1, "")
			if err != nil {
				return nil, err
			}
			return resp.Body, nil
		},
	})
}
