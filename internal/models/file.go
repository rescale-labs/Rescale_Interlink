package models

import "strings"

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
	FileChecksums        FileChecksums       `json:"fileChecksums,omitempty"`
	Tags                 []string            `json:"userTags,omitempty"`
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

// FileChecksums is the list of hashes the API reports for a file.
type FileChecksums []FileChecksum

// SHA512 returns the SHA-512 among them, however the algorithm is spelled
// ("sha512", "SHA-512", "sha-512", ...), or "" if there is none. An entry with
// no hash is not a checksum.
func (c FileChecksums) SHA512() string {
	for _, cs := range c {
		if cs.FileHash != "" && strings.EqualFold(strings.ReplaceAll(cs.HashFunction, "-", ""), "sha512") {
			return cs.FileHash
		}
	}
	return ""
}

// Algorithms names the checksums that carry a hash.
func (c FileChecksums) Algorithms() []string {
	var names []string
	for _, cs := range c {
		if cs.FileHash != "" {
			names = append(names, cs.HashFunction)
		}
	}
	return names
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

// RootFolders represents user's root folders
type RootFolders struct {
	MyJobs    string `json:"myJobs"`
	MyLibrary string `json:"myLibrary"`
}

// MetaFolders represents the workspace folder roots returned by
// GET /api/v3/meta/folders/. Used by workspace-folder auto-download to
// enumerate jobs across shared folders.
type MetaFolders struct {
	Home                MetaFolder `json:"home"`
	SharedWithWorkspace MetaFolder `json:"sharedWithWorkspace"`
	SharedWithMe        MetaFolder `json:"sharedWithMe"`
}

// MetaFolder is a node in the workspace folder tree. Children are the folders
// inside it, as deep as the response nests them.
type MetaFolder struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	WorkspaceID string       `json:"workspaceId"`
	IsArchived  bool         `json:"isArchived"`
	Children    []MetaFolder `json:"children,omitempty"`
}

// UserProfile represents a user's profile
type UserProfile struct {
	Email          string        `json:"email"`
	FullName       string        `json:"fullName"`
	Company        CompanyInfo   `json:"company"`
	Workspace      WorkspaceInfo `json:"workspace"`
	DefaultStorage StorageInfo   `json:"defaultStorage"`
}

// CompanyInfo represents organization/company details
type CompanyInfo struct {
	Code string `json:"code"` // Organization code used in API paths (e.g., "rescale")
}

// WorkspaceInfo represents workspace/organization details
type WorkspaceInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
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
	PathBase       string `json:"pathBase"`       // Base path for blob storage (Azure: "example", S3: folder prefix)
	PathPartsBase  string `json:"pathPartsBase"`  // Base path for pathParts.path in API (Azure: "", S3: same as PathBase)
	StorageAccount string `json:"storageAccount"` // Legacy field name
	AccountName    string `json:"accountName"`    // Azure storage account name (Azure only) - CORRECT FIELD!
}
