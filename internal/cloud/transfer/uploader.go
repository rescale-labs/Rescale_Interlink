// Package transfer provides unified upload and download orchestration.
// This file defines the interfaces and types used by upload orchestration.
// The actual upload entry point is in internal/cloud/upload/upload.go.
package transfer

import (
	"context"
	"fmt"
	"io"

	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/resources"
	"github.com/rescale/rescale-int/internal/transfer"
)

// StreamingConcurrentUploader extends CloudTransfer with concurrent streaming upload support.
// Providers that support concurrent streaming uploads implement this interface.
type StreamingConcurrentUploader interface {
	cloud.CloudTransfer

	// UploadLimits reports the backend's hard multipart ceilings so the
	// orchestrator can size parts before it opens an upload against it.
	UploadLimits() resources.UploadLimits

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

	// EncryptStreamingPart encrypts plaintext and returns ciphertext.
	// Must be called sequentially due to CBC chaining constraint.
	// Separated from upload to enable pipelining.
	EncryptStreamingPart(ctx context.Context, upload *StreamingUpload, partIndex int64, plaintext []byte) ([]byte, error)

	// UploadCiphertext uploads already-encrypted data to cloud storage.
	// Can be called concurrently with EncryptStreamingPart (pipelining).
	// Separated from encryption to enable pipelining.
	UploadCiphertext(ctx context.Context, upload *StreamingUpload, partIndex int64, ciphertext []byte) (*PartResult, error)

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

	// Plan is the pipeline geometry the caller reserved for this upload.
	// Providers take PartSize from it. Nil means the caller did not plan; see
	// PartSize below.
	Plan *resources.UploadPlan
}

// PartSize is the plaintext part size this upload must use. It comes from the
// caller's plan when there is a usable one, and otherwise from a plan the
// provider makes on the spot, so no path can reach a backend without a
// part-count guard. A plan carrying no part size is treated as no plan: a zero
// would divide by zero in CalculateTotalParts, and this fallback exists to catch
// exactly that kind of caller mistake.
func (p StreamingUploadInitParams) PartSize(limits resources.UploadLimits) (int64, error) {
	if p.Plan != nil && p.Plan.PartSize > 0 {
		return p.Plan.PartSize, nil
	}
	plan, err := resources.PlanUpload(resources.UploadPlanRequest{
		FileSize: p.FileSize,
		Threads:  constants.MaxThreadsPerFile,
		Limits:   limits,
	})
	if err != nil {
		return 0, err
	}
	return plan.PartSize, nil
}

// StreamingUploadResumeParams contains parameters for resuming a streaming upload.
// Used by InitStreamingUploadFromState to resume an interrupted streaming upload.
// Uses CBC chaining with InitialIV and CurrentIV.
type StreamingUploadResumeParams struct {
	LocalPath    string    // Original source file path
	FileSize     int64     // Size of the file in bytes
	StoragePath  string    // Existing storage path from resume state
	UploadID     string    // S3 upload ID from resume state (empty for Azure)
	MasterKey    []byte    // Encryption key from resume state
	InitialIV    []byte    // Initial IV from resume state (for metadata)
	CurrentIV    []byte    // Current IV from resume state (last ciphertext block)
	FileID       []byte    // DEPRECATED: File identifier from legacy resume state
	PartSize     int64     // Part size from resume state
	RandomSuffix string    // Random suffix from resume state
	OutputWriter io.Writer // Optional output for status messages
}

// StreamingUpload represents an in-progress streaming multipart upload.
// Uses CBC chaining with InitialIV for Rescale-compatible format.
type StreamingUpload struct {
	// Upload identifiers
	UploadID    string // S3 upload ID or Azure block blob path
	StoragePath string // Path in cloud storage

	// Encryption state (CBC chaining format)
	MasterKey []byte // Encryption key used for CBC
	InitialIV []byte // Initial IV for CBC chaining (stored in metadata)
	FileID    []byte // DEPRECATED: File identifier for legacy HKDF derivation
	PartSize  int64  // Size of each part in bytes

	// File info
	LocalPath    string
	TotalSize    int64
	TotalParts   int64
	RandomSuffix string

	// Provider-specific data (for S3 bucket, Azure container, etc.)
	ProviderData interface{}

	// Progress callback for real-time byte tracking during uploads.
	// Called with bytes sent so far for each part being uploaded.
	// This enables progress updates during part uploads, not just at completion.
	ByteProgressCallback func(bytesUploaded int64)
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
// This interface allows the transfer orchestrator to handle encryption
// while delegating the actual upload to the provider.
type PreEncryptUploader interface {
	cloud.CloudTransfer

	// UploadLimits reports the backend's hard multipart ceilings so the
	// orchestrator can size parts before it opens an upload against it.
	UploadLimits() resources.UploadLimits

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

	// Plan is the pipeline geometry the caller reserved for this upload, sized
	// against the ENCRYPTED file rather than the original. Providers take part
	// size, worker cap and queue depth from it. Nil means the caller did not
	// plan; see UploadPlan below.
	Plan *resources.UploadPlan
}

// UploadPlan is the geometry this upload must run with. It comes from the
// caller's plan when there is a usable one, and otherwise from a plan the
// provider makes on the spot, so no path can reach a backend without a
// part-count guard. A plan carrying no part size is treated as no plan: a zero
// would divide by zero in CalculateTotalParts, and this fallback exists to catch
// exactly that kind of caller mistake.
//
// encryptedSize, not the original file size, is what the fallback plans against:
// the parts the backend counts are ciphertext.
func (p EncryptedFileUploadParams) UploadPlan(encryptedSize int64, limits resources.UploadLimits) (resources.UploadPlan, error) {
	if p.Plan != nil && p.Plan.PartSize > 0 {
		return *p.Plan, nil
	}
	return resources.PlanUpload(resources.UploadPlanRequest{
		FileSize: encryptedSize,
		Threads:  constants.MaxThreadsPerFile,
		Limits:   limits,
	})
}

// VerifyUploadComplete reports whether an upload holds every byte and every part
// of the file it is about to assemble. Both backends assemble whatever subset of
// parts they are handed and report success, so a reader that stopped early would
// otherwise register a truncated object as the whole file. Call this immediately
// before CompleteMultipartUpload / CommitBlockList and abort instead of
// committing when it fails.
func VerifyUploadComplete(uploadedBytes, expectedBytes, partCount, expectedParts int64) error {
	if uploadedBytes != expectedBytes || partCount != expectedParts {
		return fmt.Errorf("upload incomplete: holding %d of %d bytes in %d of %d parts",
			uploadedBytes, expectedBytes, partCount, expectedParts)
	}
	return nil
}

// VerifyPartSequence reports whether partNumbers, in the order given, is exactly
// 1..len(partNumbers). S3 assembles an object in part-number order, so a gap, a
// duplicate or an out-of-order list changes the stored bytes without failing.
func VerifyPartSequence(partNumbers []int32) error {
	for i, num := range partNumbers {
		if num != int32(i+1) {
			return fmt.Errorf("part numbers are not contiguous: position %d holds part %d", i+1, num)
		}
	}
	return nil
}

// VerifyBlockList reports whether every slot of an ordered Azure block list was
// filled. An unfilled slot is a block that was never staged; committing the list
// without it silently drops that block's bytes from the blob.
func VerifyBlockList(blockIDs []string) error {
	for i, id := range blockIDs {
		if id == "" {
			return fmt.Errorf("block %d of %d was never staged", i+1, len(blockIDs))
		}
	}
	return nil
}
