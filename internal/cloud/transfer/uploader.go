// Package transfer provides unified upload and download orchestration.
// This file defines the interfaces and types used by upload orchestration.
// The actual upload entry point is in internal/cloud/upload/upload.go.
//
// Version: 3.2.0 (Rescale-Compatible CBC Chaining)
// Date: 2025-12-02
package transfer

import (
	"context"
	"io"

	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/transfer"
)

// StreamingConcurrentUploader extends CloudTransfer with concurrent streaming upload support.
// Providers that support concurrent streaming uploads implement this interface.
type StreamingConcurrentUploader interface {
	cloud.CloudTransfer

	// InitStreamingUpload initializes a multipart upload with streaming encryption.
	// Returns a StreamingUpload handle that tracks the upload state.
	InitStreamingUpload(ctx context.Context, params StreamingUploadInitParams) (*StreamingUpload, error)

	// InitStreamingUploadFromState resumes a streaming upload with existing encryption params.
	// Uses key/IV from resume state to continue CBC-chained encryption.
	// This allows resuming an interrupted streaming upload.
	InitStreamingUploadFromState(ctx context.Context, params StreamingUploadResumeParams) (*StreamingUpload, error)

	// ValidateStreamingUploadExists checks if a streaming upload can be resumed.
	// For S3: calls ListParts to verify multipart upload still exists.
	// For Azure: blocks auto-expire after 7 days, so validates state age.
	// Returns (exists, error) where exists=false means upload expired and should start fresh.
	ValidateStreamingUploadExists(ctx context.Context, uploadID, storagePath string) (bool, error)

	// UploadStreamingPart encrypts and uploads a single part.
	// partIndex is 0-based, plaintext is the raw data for this part.
	// v3.2.0: Parts must be encrypted sequentially due to CBC chaining.
	UploadStreamingPart(ctx context.Context, upload *StreamingUpload, partIndex int64, plaintext []byte) (*PartResult, error)

	// CompleteStreamingUpload completes the multipart upload.
	// parts must contain results for all uploaded parts in order.
	CompleteStreamingUpload(ctx context.Context, upload *StreamingUpload, parts []*PartResult) (*cloud.UploadResult, error)

	// AbortStreamingUpload aborts a streaming upload and cleans up resources.
	AbortStreamingUpload(ctx context.Context, upload *StreamingUpload) error
}

// StreamingUploadInitParams contains parameters for initializing a streaming upload.
type StreamingUploadInitParams struct {
	LocalPath    string    // Path to the file to upload
	FileSize     int64     // Size of the file in bytes
	FolderID     string    // Target folder ID (empty = MyLibrary)
	OutputWriter io.Writer // Optional output for status messages
}

// StreamingUploadResumeParams contains parameters for resuming a streaming upload.
// Used by InitStreamingUploadFromState to resume an interrupted streaming upload.
// v3.2.0: Updated for CBC chaining - uses InitialIV and CurrentIV instead of FileID.
type StreamingUploadResumeParams struct {
	LocalPath    string    // Original source file path
	FileSize     int64     // Size of the file in bytes
	StoragePath  string    // Existing storage path from resume state
	UploadID     string    // S3 upload ID from resume state (empty for Azure)
	MasterKey    []byte    // Encryption key from resume state (v3.2.0: actual key, not master)
	InitialIV    []byte    // Initial IV from resume state (v3.2.0: for metadata)
	CurrentIV    []byte    // Current IV from resume state (v3.2.0: last ciphertext block)
	FileID       []byte    // DEPRECATED: File identifier from legacy resume state (v3.1.x)
	PartSize     int64     // Part size from resume state
	RandomSuffix string    // Random suffix from resume state
	OutputWriter io.Writer // Optional output for status messages
}

// StreamingUpload represents an in-progress streaming multipart upload.
// v3.2.0: Updated for CBC chaining - added InitialIV for Rescale-compatible format.
type StreamingUpload struct {
	// Upload identifiers
	UploadID    string // S3 upload ID or Azure block blob path
	StoragePath string // Path in cloud storage

	// Encryption state (v3.2.0: CBC chaining format)
	MasterKey []byte // Encryption key (v3.2.0: actual key used for CBC)
	InitialIV []byte // Initial IV for CBC chaining (v3.2.0: stored in metadata)
	FileID    []byte // DEPRECATED: File identifier for legacy HKDF derivation
	PartSize  int64  // Size of each part in bytes

	// File info
	LocalPath    string
	TotalSize    int64
	TotalParts   int64
	RandomSuffix string

	// Provider-specific data (for S3 bucket, Azure container, etc.)
	ProviderData interface{}
}

// PartResult contains the result of uploading a single part.
type PartResult struct {
	PartIndex  int64  // 0-based part index
	PartNumber int32  // 1-based part number (for S3 compatibility)
	ETag       string // S3 ETag or Azure block ID
	Size       int64  // Size of plaintext data uploaded
}

// PreEncryptUploader extends CloudTransfer with pre-encrypted upload support.
// Providers that support pre-encrypted uploads implement this interface.
// Sprint 7B: This interface allows the transfer orchestrator to handle encryption
// while delegating the actual upload to the provider.
type PreEncryptUploader interface {
	cloud.CloudTransfer

	// UploadEncryptedFile uploads an already-encrypted file.
	// The file at EncryptedPath is the pre-encrypted data.
	// Handles multipart upload with optional concurrency via TransferHandle.
	// Resume state is managed by the provider using LocalPath as the resume key.
	UploadEncryptedFile(ctx context.Context, params EncryptedFileUploadParams) (*cloud.UploadResult, error)
}

// EncryptedFileUploadParams contains parameters for uploading a pre-encrypted file.
type EncryptedFileUploadParams struct {
	LocalPath        string             // Original file path (for path generation and resume key)
	EncryptedPath    string             // Path to the encrypted temp file
	EncryptionKey    []byte             // Encryption key (for resume state)
	IV               []byte             // IV (for cloud metadata)
	RandomSuffix     string             // Pre-generated random suffix for storage path
	OriginalSize     int64              // Original file size (for resume state)
	TransferHandle   *transfer.Transfer // For concurrency
	ProgressCallback func(float64)      // Progress reporting
	OutputWriter     io.Writer          // Status messages
}
