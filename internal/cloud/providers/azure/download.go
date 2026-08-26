// Package azure provides an Azure implementation of the CloudTransfer interface.
// This file implements the LegacyDownloader interface for legacy (v0) format downloads.
//
// Uses AzureClient directly for all operations.
// Note: StreamingConcurrentDownloader (DetectFormat, DownloadStreaming) is in
// streaming_concurrent.go.
package azure

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
// Uses AzureClient with file-aware credential handling.
func (p *Provider) DownloadEncryptedFile(ctx context.Context, params transfer.LegacyDownloadParams) error {
	// Get or create Azure client (with file-aware credential handling)
	azureClient, err := p.getOrCreateAzureClientForFile(ctx, params.FileInfo)
	if err != nil {
		return fmt.Errorf("failed to get Azure client: %w", err)
	}

	// Ensure fresh credentials
	if err := azureClient.EnsureFreshCredentials(ctx); err != nil {
		return fmt.Errorf("failed to refresh credentials: %w", err)
	}

	// Get file size from params, or fetch from Azure if not provided
	fileSize := params.FileSize
	if fileSize == 0 {
		// Fetch file size from Azure metadata
		props, err := azureClient.GetBlobProperties(ctx, params.RemotePath)
		if err != nil {
			return fmt.Errorf("failed to get blob properties: %w", err)
		}
		fileSize = props.ContentLength
	}

	azureClient.StartPeriodicRefresh(ctx)
	defer azureClient.StopPeriodicRefresh()

	// Choose download method based on file size and transfer handle
	if fileSize > constants.MultipartThreshold {
		// Use chunked download for large files
		if params.TransferHandle != nil && params.TransferHandle.GetThreads() > 1 {
			// Concurrent chunked download
			return p.downloadChunkedConcurrent(
				ctx,
				azureClient,
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
			azureClient,
			params.RemotePath,
			params.EncryptedPath,
			fileSize,
			params.ProgressCallback,
		)
	}

	// Small file: single download
	return p.downloadSingleWithProgress(
		ctx,
		azureClient,
		params.RemotePath,
		params.EncryptedPath,
		params.ProgressCallback,
		fileSize,
	)
}

// downloadSingleWithProgress downloads a blob in a single GET request with progress callback.
// Uses AzureClient directly.
func (p *Provider) downloadSingleWithProgress(ctx context.Context, azureClient *AzureClient, remotePath, localPath string, progressCallback func(float64), totalSize int64) error {
	// Report 0% at start
	if progressCallback != nil {
		progressCallback(0.0)
	}

	// Download blob using AzureClient
	resp, err := azureClient.DownloadStream(ctx, remotePath, nil)
	if err != nil {
		return fmt.Errorf("failed to download blob: %w", err)
	}
	defer resp.Body.Close()

	return transfer.WriteBodyWithProgress(resp.Body, localPath, progressCallback, totalSize)
}

// downloadChunkedWithProgress downloads a blob in chunks with progress callback.
// Uses AzureClient directly.
// Wraps request+read+close in single retry to handle mid-transfer proxy failures.
func (p *Provider) downloadChunkedWithProgress(ctx context.Context, azureClient *AzureClient, remotePath, localPath string, totalSize int64, progressCallback func(float64)) error {
	// Report 0% at start
	if progressCallback != nil {
		progressCallback(0.0)
	}

	// DownloadRangeOnce is the non-retrying variant, so the shared helper's
	// per-chunk retry is the only retry.
	return transfer.DownloadChunkedToFile(ctx, azureClient.RetryWithBackoff, localPath, totalSize, progressCallback,
		func(attemptCtx context.Context, offset, length int64) (io.ReadCloser, error) {
			resp, err := azureClient.DownloadRangeOnce(attemptCtx, remotePath, offset, length, "")
			if err != nil {
				return nil, err
			}
			return resp.Body, nil
		})
}

// downloadChunkedConcurrent downloads a blob using concurrent range requests.
// The shared driver owns the chunking, resume state, worker pool and writes; this
// wrapper supplies only the Azure calls.
func (p *Provider) downloadChunkedConcurrent(ctx context.Context, azureClient *AzureClient, remotePath, localPath string, totalSize int64, progressCallback func(float64), transferHandle *internaltransfer.Transfer) error {
	// The ETag pins the blob for resume validation: a resume state naming a
	// different one is discarded. Range requests do not send it (see below).
	props, err := azureClient.GetBlobProperties(ctx, remotePath)
	if err != nil {
		return fmt.Errorf("failed to get blob properties: %w", err)
	}

	concurrency := 4
	if transferHandle != nil && transferHandle.GetThreads() > 0 {
		concurrency = transferHandle.GetThreads()
	}

	return transfer.DownloadChunkedConcurrent(ctx, transfer.ChunkedConcurrentParams{
		RemotePath:       remotePath,
		LocalPath:        localPath,
		TotalSize:        totalSize,
		Concurrency:      concurrency,
		StorageType:      "AzureStorage",
		ObjectETag:       props.ETag,
		Retry:            azureClient.RetryWithBackoff,
		ProgressCallback: progressCallback,
		// DownloadRangeOnce is the non-retrying variant, so the driver's
		// per-chunk retry is the only retry.
		Open: func(attemptCtx context.Context, offset, length int64) (io.ReadCloser, error) {
			// Per-request If-Match deliberately omitted (proxy ETag-mangling
			// risk); staleness is caught by resume-state validation + checksums.
			resp, err := azureClient.DownloadRangeOnce(attemptCtx, remotePath, offset, length, "")
			if err != nil {
				return nil, err
			}
			return resp.Body, nil
		},
	})
}
