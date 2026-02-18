package models

// S3Credentials represents S3 storage credentials from /api/v3/credentials/
type S3Credentials struct {
	StorageType  string `json:"storageType"` // "S3Storage"
	StorageDir   string `json:"storageDir"`
	AccessKeyID  string `json:"accessKey"`
	SecretKey    string `json:"secretKey"`
	SessionToken string `json:"sessionToken"`
}

// AzureCredentialPath represents a per-file credential entry in the Azure credential response.
// Populated for shared-file requests where the API returns blob-level SAS tokens.
type AzureCredentialPath struct {
	Path      string              `json:"path"`
	PathParts *CloudFilePathParts `json:"pathParts"`
	SASToken  string              `json:"sasToken"`
}

// AzureCredentials represents Azure storage credentials from /api/v3/credentials/
type AzureCredentials struct {
	StorageType string                `json:"storageType"` // "AzureStorage"
	StorageDir  string                `json:"storageDir"`
	SASToken    string                `json:"sasToken"`
	Expiration  string                `json:"expiration"`
	Paths       []AzureCredentialPath `json:"paths"`
}
