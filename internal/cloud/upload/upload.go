package upload

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/credentials"
	"github.com/rescale/rescale-int/internal/crypto"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/transfer"
)

// ProgressCallback is called during upload to report progress (0.0 to 1.0)
type ProgressCallback func(progress float64)

// UploadFile uploads a file to Rescale cloud storage (to MyLibrary folder).
// For uploading to a specific folder, use UploadFileToFolder.
func UploadFile(ctx context.Context, localPath string, apiClient *api.Client, progressCallback ProgressCallback, outputWriter io.Writer) (*models.CloudFile, error) {
	return UploadFileToFolder(ctx, localPath, "", apiClient, progressCallback, outputWriter)
}

// UploadFileToFolder uploads a file to a specific folder in Rescale cloud storage using the correct workflow:
// 1. Get user profile and storage credentials
// 2. Encrypt file locally
// 3. Upload encrypted file to S3/Azure
// 4. Register file with Rescale API (with correct currentFolderId)
// If folderID is empty, uploads to MyLibrary
// progressCallback is optional and called with progress from 0.0 to 1.0
func UploadFileToFolder(ctx context.Context, localPath string, folderID string, apiClient *api.Client, progressCallback ProgressCallback, outputWriter io.Writer) (*models.CloudFile, error) {
	// Get the global credential manager (caches user profile, credentials, and folders)
	credManager := credentials.GetManager(apiClient)

	// Get user profile to determine storage type (cached for 5 minutes)
	profile, err := credManager.GetUserProfile(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get user profile: %w", err)
	}

	// Get root folders (for currentFolderId in file registration) (cached for 5 minutes)
	folders, err := credManager.GetRootFolders(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get root folders: %w", err)
	}

	// Get temporary storage credentials from cache (refreshed every 10 minutes)
	var s3Creds *models.S3Credentials
	var azureCreds *models.AzureCredentials
	if profile.DefaultStorage.StorageType == "S3Storage" {
		s3Creds, err = credManager.GetS3Credentials(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get S3 credentials: %w", err)
		}
	} else if profile.DefaultStorage.StorageType == "AzureStorage" {
		azureCreds, err = credManager.GetAzureCredentials(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get Azure credentials: %w", err)
		}
	}

	// Get file size
	fileInfo, err := os.Stat(localPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	// Calculate SHA-512 hash of original file
	fileHash, err := encryption.CalculateSHA512(localPath)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate file hash: %w", err)
	}

	var storagePath string
	var encryptionKey []byte
	var iv []byte

	// Upload based on storage type
	switch profile.DefaultStorage.StorageType {
	case "S3Storage":
		if s3Creds == nil {
			return nil, fmt.Errorf("S3 credentials not provided")
		}
		uploader, err := NewS3Uploader(&profile.DefaultStorage, s3Creds, apiClient)
		if err != nil {
			return nil, fmt.Errorf("failed to create S3 uploader: %w", err)
		}
		storagePath, encryptionKey, iv, err = uploader.UploadEncrypted(ctx, localPath, progressCallback, outputWriter)
		if err != nil {
			return nil, fmt.Errorf("S3 upload failed: %w", err)
		}

	case "AzureStorage":
		if azureCreds == nil {
			return nil, fmt.Errorf("Azure credentials not provided")
		}
		uploader, err := NewAzureUploader(&profile.DefaultStorage, azureCreds, apiClient)
		if err != nil {
			return nil, fmt.Errorf("failed to create Azure uploader: %w", err)
		}
		storagePath, encryptionKey, iv, err = uploader.UploadEncrypted(ctx, localPath, progressCallback, outputWriter)
		if err != nil {
			return nil, fmt.Errorf("Azure upload failed: %w", err)
		}

	default:
		return nil, fmt.Errorf("unsupported storage type: %s", profile.DefaultStorage.StorageType)
	}

	// Extract container and path from storagePath
	// storagePath format: "pathBase/objectName"
	filename := filepath.Base(localPath)

	// Determine target folder
	targetFolder := folders.MyLibrary
	if folderID != "" {
		targetFolder = folderID
	}

	// Build file registration request
	fileReq := &models.CloudFileRequest{
		TypeID:               1, // INPUT_FILE
		Name:                 filename,
		CurrentFolderID:      targetFolder,
		EncodedEncryptionKey: encryption.EncodeBase64(encryptionKey),
		PathParts: models.CloudFilePathParts{
			Container: profile.DefaultStorage.ConnectionSettings.Container,
			Path:      storagePath,
		},
		Storage: models.CloudFileStorage{
			ID:             profile.DefaultStorage.ID,
			StorageType:    profile.DefaultStorage.StorageType,
			EncryptionType: profile.DefaultStorage.EncryptionType,
		},
		IsUploaded:    true,
		DecryptedSize: fileInfo.Size(),
		FileChecksums: []models.FileChecksum{
			{
				HashFunction: "sha512",
				FileHash:     fileHash,
			},
		},
	}

	// Register file with Rescale
	cloudFile, err := apiClient.RegisterFile(ctx, fileReq)
	if err != nil {
		// Provide helpful context based on error type
		fileName := filepath.Base(localPath)
		if strings.Contains(err.Error(), "TLS handshake timeout") {
			return nil, fmt.Errorf("failed to register file %s (connection pool exhausted - try reducing --max-concurrent): %w",
				fileName, err)
		}
		if strings.Contains(err.Error(), "rate limiter") {
			return nil, fmt.Errorf("failed to register file %s (rate limited - this is temporary): %w",
				fileName, err)
		}
		if strings.Contains(err.Error(), "timeout") {
			return nil, fmt.Errorf("failed to register file %s (API timeout - check network): %w",
				fileName, err)
		}
		return nil, fmt.Errorf("failed to register file %s: %w", fileName, err)
	}

	// Suppress unused warning
	_ = iv

	return cloudFile, nil
}

// UploadFileToFolderWithTransfer uploads a file with concurrent support using a transfer handle
// If transferHandle is nil, uses sequential upload (same as UploadFileToFolder)
// If transferHandle specifies multiple threads, uses concurrent part/block uploads
func UploadFileToFolderWithTransfer(ctx context.Context, localPath string, folderID string, apiClient *api.Client, progressCallback ProgressCallback, transferHandle *transfer.Transfer, outputWriter io.Writer) (*models.CloudFile, error) {
	// Get the global credential manager (caches user profile, credentials, and folders)
	credManager := credentials.GetManager(apiClient)

	// Get user profile to determine storage type (cached for 5 minutes)
	profile, err := credManager.GetUserProfile(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get user profile: %w", err)
	}

	// Get root folders (for currentFolderId in file registration) (cached for 5 minutes)
	folders, err := credManager.GetRootFolders(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get root folders: %w", err)
	}

	// Get temporary storage credentials from cache (refreshed every 10 minutes)
	var s3Creds *models.S3Credentials
	var azureCreds *models.AzureCredentials
	if profile.DefaultStorage.StorageType == "S3Storage" {
		s3Creds, err = credManager.GetS3Credentials(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get S3 credentials: %w", err)
		}
	} else if profile.DefaultStorage.StorageType == "AzureStorage" {
		azureCreds, err = credManager.GetAzureCredentials(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get Azure credentials: %w", err)
		}
	}

	// Get file size
	fileInfo, err := os.Stat(localPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	// Calculate SHA-512 hash of original file
	fileHash, err := encryption.CalculateSHA512(localPath)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate file hash: %w", err)
	}

	var storagePath string
	var encryptionKey []byte
	var iv []byte

	// Upload based on storage type
	switch profile.DefaultStorage.StorageType {
	case "S3Storage":
		if s3Creds == nil {
			return nil, fmt.Errorf("S3 credentials not provided")
		}
		uploader, err := NewS3Uploader(&profile.DefaultStorage, s3Creds, apiClient)
		if err != nil {
			return nil, fmt.Errorf("failed to create S3 uploader: %w", err)
		}
		storagePath, encryptionKey, iv, err = uploader.UploadEncryptedWithTransfer(ctx, localPath, progressCallback, transferHandle, outputWriter)
		if err != nil {
			return nil, fmt.Errorf("S3 upload failed: %w", err)
		}

	case "AzureStorage":
		if azureCreds == nil {
			return nil, fmt.Errorf("Azure credentials not provided")
		}
		uploader, err := NewAzureUploader(&profile.DefaultStorage, azureCreds, apiClient)
		if err != nil {
			return nil, fmt.Errorf("failed to create Azure uploader: %w", err)
		}
		storagePath, encryptionKey, iv, err = uploader.UploadEncryptedWithTransfer(ctx, localPath, progressCallback, transferHandle, outputWriter)
		if err != nil {
			return nil, fmt.Errorf("Azure upload failed: %w", err)
		}

	default:
		return nil, fmt.Errorf("unsupported storage type: %s", profile.DefaultStorage.StorageType)
	}

	// Extract container and path from storagePath
	// storagePath format: "pathBase/objectName"
	filename := filepath.Base(localPath)

	// Determine target folder
	targetFolder := folders.MyLibrary
	if folderID != "" {
		targetFolder = folderID
	}

	// Build file registration request
	fileReq := &models.CloudFileRequest{
		TypeID:               1, // INPUT_FILE
		Name:                 filename,
		CurrentFolderID:      targetFolder,
		EncodedEncryptionKey: encryption.EncodeBase64(encryptionKey),
		PathParts: models.CloudFilePathParts{
			Container: profile.DefaultStorage.ConnectionSettings.Container,
			Path:      storagePath,
		},
		Storage: models.CloudFileStorage{
			ID:             profile.DefaultStorage.ID,
			StorageType:    profile.DefaultStorage.StorageType,
			EncryptionType: profile.DefaultStorage.EncryptionType,
		},
		IsUploaded:    true,
		DecryptedSize: fileInfo.Size(),
		FileChecksums: []models.FileChecksum{
			{
				HashFunction: "sha512",
				FileHash:     fileHash,
			},
		},
	}

	// Register file with Rescale
	cloudFile, err := apiClient.RegisterFile(ctx, fileReq)
	if err != nil {
		// Provide helpful context based on error type
		fileName := filepath.Base(localPath)
		if strings.Contains(err.Error(), "TLS handshake timeout") {
			return nil, fmt.Errorf("failed to register file %s (connection pool exhausted - try reducing --max-concurrent): %w",
				fileName, err)
		}
		if strings.Contains(err.Error(), "rate limiter") {
			return nil, fmt.Errorf("failed to register file %s (rate limited - this is temporary): %w",
				fileName, err)
		}
		if strings.Contains(err.Error(), "timeout") {
			return nil, fmt.Errorf("failed to register file %s (API timeout - check network): %w",
				fileName, err)
		}
		return nil, fmt.Errorf("failed to register file %s: %w", fileName, err)
	}

	// Suppress unused warning
	_ = iv

	return cloudFile, nil
}
