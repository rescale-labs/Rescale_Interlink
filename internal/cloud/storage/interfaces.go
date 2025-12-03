// Package storage provides common interfaces and utilities for cloud storage operations.
// This package defines the contract that S3 and Azure implementations must follow,
// enabling consistent behavior and easier testing across storage backends.
package storage

import (
	"context"
	"io"
)

// CloudUploader defines the interface for uploading files to cloud storage.
// Both S3Uploader and AzureUploader implement this interface.
//
// Note: progressCallback uses func(float64) instead of a named type to avoid
// import cycles and ensure implementations with their own callback types satisfy this interface.
type CloudUploader interface {
	// UploadEncrypted uploads a file with pre-encryption (legacy mode).
	// Creates an encrypted temp file before uploading.
	// Returns: storagePath, encryptionKey, iv, error
	UploadEncrypted(ctx context.Context, localPath string, progressCallback func(float64), outputWriter io.Writer) (string, []byte, []byte, error)

	// UploadEncryptedStreaming uploads a file with streaming encryption.
	// Encrypts on-the-fly without creating temp files, saving disk space.
	// Returns: storagePath, encryptionKey, iv, error
	UploadEncryptedStreaming(ctx context.Context, localPath string, progressCallback func(float64), outputWriter io.Writer) (string, []byte, []byte, error)
}

// CloudDownloader defines the interface for downloading files from cloud storage.
// Both S3Downloader and AzureDownloader implement this interface.
type CloudDownloader interface {
	// DownloadAndDecrypt downloads and decrypts a file using legacy CBC encryption.
	// objectKey: the cloud storage path (S3 key or Azure blob path)
	// localPath: where to save the decrypted file
	// encryptionKey: the AES-256 key used to decrypt
	// iv: initialization vector for CBC mode
	DownloadAndDecrypt(ctx context.Context, objectKey, localPath string, encryptionKey, iv []byte) error

	// DownloadAndDecryptWithProgress downloads with progress reporting.
	// progressCallback receives values from 0.0 to 1.0
	DownloadAndDecryptWithProgress(ctx context.Context, objectKey, localPath string, encryptionKey, iv []byte, progressCallback func(float64)) error

	// DownloadAndDecryptStreaming downloads and decrypts using streaming format.
	// Uses per-part encryption with master key derivation.
	// masterKey: the 32-byte master key for key derivation
	DownloadAndDecryptStreaming(ctx context.Context, objectKey, localPath string, masterKey []byte, progressCallback func(float64)) error
}

// CredentialRefresher defines the interface for credential management.
// Implementations handle cloud-specific credential refresh logic.
type CredentialRefresher interface {
	// EnsureFreshCredentials refreshes credentials if they're expired or about to expire.
	// This is the Layer 1 (proactive) refresh in our 3-layer strategy.
	EnsureFreshCredentials(ctx context.Context) error
}

// CloudClient combines upload and download capabilities with credential management.
// This is the full interface that a complete cloud storage client should implement.
type CloudClient interface {
	CloudUploader
	CloudDownloader
	CredentialRefresher
}
