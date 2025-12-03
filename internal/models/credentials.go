package models

// S3Credentials represents S3 storage credentials from /api/v3/credentials/
type S3Credentials struct {
	StorageType  string `json:"storageType"` // "S3Storage"
	StorageDir   string `json:"storageDir"`
	AccessKeyID  string `json:"accessKey"`
	SecretKey    string `json:"secretKey"`
	SessionToken string `json:"sessionToken"`
}

// AzureCredentials represents Azure storage credentials from /api/v3/credentials/
type AzureCredentials struct {
	StorageType string   `json:"storageType"` // "AzureStorage"
	StorageDir  string   `json:"storageDir"`
	SASToken    string   `json:"sasToken"`
	Paths       []string `json:"paths"`
}
