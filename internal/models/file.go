package models

// CloudFile represents a file stored in Rescale cloud storage
type CloudFile struct {
	ID                   string              `json:"id"`
	Name                 string              `json:"name"`
	TypeID               int                 `json:"typeId"`
	IsUploaded           bool                `json:"isUploaded"`
	Owner                string              `json:"owner"`
	Path                 string              `json:"path"`
	EncodedEncryptionKey string              `json:"encodedEncryptionKey,omitempty"`
	IV                   string              `json:"iv,omitempty"` // Initialization vector for decryption
	PathParts            *CloudFilePathParts `json:"pathParts,omitempty"`
	Storage              *CloudFileStorage   `json:"storage,omitempty"`
	DecryptedSize        int64               `json:"decryptedSize,omitempty"`
	FileChecksums        []FileChecksum      `json:"fileChecksums,omitempty"`
}

// CloudFilePathParts represents the storage path for a file
type CloudFilePathParts struct {
	Container string `json:"container"`
	Path      string `json:"path"`
}

// CloudFileStorage represents storage metadata for a file
type CloudFileStorage struct {
	ID                 string             `json:"id"`
	StorageType        string             `json:"storageType"`
	EncryptionType     string             `json:"encryptionType"`
	ConnectionSettings ConnectionSettings `json:"connectionSettings,omitempty"`
}

// FileChecksum represents a file hash
type FileChecksum struct {
	HashFunction string `json:"hashFunction"`
	FileHash     string `json:"fileHash"`
}

// CredentialsPathPartsRequest represents path information for credentials request
// NOTE: Uses camelCase JSON tags to match Python client (Pydantic alias behavior)
type CredentialsPathPartsRequest struct {
	PathParts CloudFilePathParts `json:"pathParts"`
}

// CredentialsStorageRequest represents storage information for credentials request
// NOTE: Uses camelCase JSON tags to match Python client (Pydantic alias behavior)
type CredentialsStorageRequest struct {
	ID          string `json:"id"`
	StorageType string `json:"storageType"`
}

// CredentialsRequest represents a request for storage credentials
// Used to get credentials for specific file storage (e.g., S3 creds for job outputs on Azure account)
type CredentialsRequest struct {
	Storage CredentialsStorageRequest     `json:"storage"`
	Paths   []CredentialsPathPartsRequest `json:"paths"`
}

// CloudFileRequest represents a file registration request
type CloudFileRequest struct {
	TypeID               int                `json:"typeId"`
	Name                 string             `json:"name"`
	CurrentFolderID      string             `json:"currentFolderId"`
	EncodedEncryptionKey string             `json:"encodedEncryptionKey"`
	PathParts            CloudFilePathParts `json:"pathParts"`
	Storage              CloudFileStorage   `json:"storage"`
	IsUploaded           bool               `json:"isUploaded"`
	DecryptedSize        int64              `json:"decryptedSize"`
	FileChecksums        []FileChecksum     `json:"fileChecksums"`
}

// FileListResponse represents the response from file list API
type FileListResponse struct {
	Count   int         `json:"count"`
	Next    *string     `json:"next"`
	Results []CloudFile `json:"results"`
}

// RootFolders represents user's root folders
type RootFolders struct {
	MyJobs    string `json:"myJobs"`
	MyLibrary string `json:"myLibrary"`
}

// UserProfile represents a user's profile
type UserProfile struct {
	Email          string      `json:"email"`
	DefaultStorage StorageInfo `json:"defaultStorage"`
}

// StorageInfo represents storage configuration
type StorageInfo struct {
	ID                 string             `json:"id"`
	StorageType        string             `json:"storageType"` // "S3Storage" or "AzureStorage"
	EncryptionType     string             `json:"encryptionType"`
	ConnectionSettings ConnectionSettings `json:"connectionSettings"`
}

// ConnectionSettings represents storage connection details
type ConnectionSettings struct {
	Region         string `json:"region"`         // AWS region (S3)
	Container      string `json:"container"`      // S3 bucket or Azure container
	PathBase       string `json:"pathBase"`       // Base path for blob storage (Azure: "ag9web", S3: folder prefix)
	PathPartsBase  string `json:"pathPartsBase"`  // Base path for pathParts.path in API (Azure: "", S3: same as PathBase)
	StorageAccount string `json:"storageAccount"` // Legacy field name
	AccountName    string `json:"accountName"`    // Azure storage account name (Azure only) - CORRECT FIELD!
}
