// Package models defines data structures for the PUR application.
package models

import "time"

// JobSpec represents a complete job specification from CSV
type JobSpec struct {
	Directory             string
	JobName               string
	AnalysisCode          string
	AnalysisVersion       string
	Command               string
	CoreType              string
	CoresPerSlot          int
	WalltimeHours         float64
	Slots                 int
	LicenseSettings       string // JSON string
	ExtraInputFileIDs     string // Comma-separated file IDs
	NoDecompress          bool
	SubmitMode            string // "yes", "no", or "draft"
	IsLowPriority         bool
	OnDemandLicenseSeller string
	Tags                  []string // Job tags (added in v1.0.0)
	ProjectID             string   // Project ID to assign job to (added in v1.0.0)
}

// JobState represents the state of a job in the pipeline
type JobState struct {
	Index        int
	JobName      string
	Directory    string
	TarPath      string
	TarStatus    string // "pending", "success", "failed"
	FileID       string
	UploadStatus string // "pending", "success", "failed"
	JobID        string
	SubmitStatus string // "pending", "success", "failed", "skipped"
	ExtraFileIDs string
	ErrorMessage string
	LastUpdated  time.Time
}

// JobRequest represents a Rescale API v3 job creation request
type JobRequest struct {
	Name          string               `json:"name"`
	JobAnalyses   []JobAnalysisRequest `json:"jobanalyses"`
	IsLowPriority bool                 `json:"isLowPriority"`
	Tags          []string             `json:"tags,omitempty"`      // Added in v1.0.0
	ProjectID     string               `json:"projectId,omitempty"` // Added in v1.0.0
}

// JobAnalysisRequest represents an analysis within a job
type JobAnalysisRequest struct {
	Command                    string             `json:"command"`
	Analysis                   AnalysisRequest    `json:"analysis"`
	Hardware                   HardwareRequest    `json:"hardware"`
	InputFiles                 []InputFileRequest `json:"inputFiles,omitempty"`
	EnvVars                    map[string]string  `json:"envVars,omitempty"`
	UseRescaleLicense          bool               `json:"useRescaleLicense"`
	OnDemandLicenseSeller      *string            `json:"onDemandLicenseSeller"`
	UserDefinedLicenseSettings *string            `json:"userDefinedLicenseSettings"`
}

// AnalysisRequest represents software analysis configuration
type AnalysisRequest struct {
	Code    string `json:"code"`
	Version string `json:"version,omitempty"`
}

// HardwareRequest represents hardware configuration
type HardwareRequest struct {
	CoreType     CoreTypeRequest `json:"coreType"`
	CoresPerSlot int             `json:"coresPerSlot"`
	Slots        int             `json:"slots,omitempty"`
	Walltime     int             `json:"walltime,omitempty"` // in seconds
}

// CoreTypeRequest represents core type in v3 format
type CoreTypeRequest struct {
	Code string `json:"code"`
}

// InputFileRequest represents an input file
type InputFileRequest struct {
	ID         string `json:"id"`
	Decompress bool   `json:"decompress"`
}

// JobResponse represents a job from the API
type JobResponse struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	JobStatus JobStatusContent `json:"jobStatus"`
	CreatedAt string           `json:"dateInserted"`
	Owner     string           `json:"owner"`
}

// JobStatusContent represents job status
// Note: This struct is used for the /jobs/ list endpoint, which returns jobStatus.
// The /jobs/{id}/ GET endpoint does NOT include jobStatus, so use GetJobStatuses() instead.
type JobStatusContent struct {
	Status  string `json:"content"`                // API returns status in "content" field (for list endpoint)
	Content string `json:"statusReason,omitempty"` // Status reason/details
}

// JobSubmitRequest represents a job submission request (v2 API)
type JobSubmitRequest struct {
	JobID string `json:"job"`
}

// CoreType represents a hardware core type from the API.
// Added in v1.0.0 (October 7, 2025) for core type validation feature.
// Note: All core types returned by the API are available for use.
type CoreType struct {
	Code         string `json:"code"`
	Name         string `json:"name"`
	DisplayOrder int    `json:"displayOrder"`
	IsActive     bool   `json:"isActive"`
}

// Analysis represents software analysis/application available on Rescale.
type Analysis struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	VendorName  string `json:"vendorName,omitempty"`
	Versions    []struct {
		ID               string   `json:"id"`
		Version          string   `json:"version,omitempty"`
		VersionCode      string   `json:"versionCode,omitempty"`
		AllowedCoreTypes []string `json:"allowedCoreTypes,omitempty"`
	} `json:"versions,omitempty"`
	Industries []struct {
		Name string `json:"name"`
		Icon string `json:"icon,omitempty"`
	} `json:"industries,omitempty"`
	DisplayOrder int    `json:"displayOrder,omitempty"`
	Thumbnail    string `json:"thumbnail,omitempty"`
}

// JobStatusEntry represents a job status update entry
type JobStatusEntry struct {
	Status       string `json:"status"`
	StatusDate   string `json:"statusDate"`
	StatusReason string `json:"statusReason,omitempty"`
}

// JobFile represents an output file from a job
// 2025-11-20: Enhanced to include full metadata from v2 endpoint
// This allows downloading without separate GetFileInfo API call
type JobFile struct {
	ID                   string              `json:"id"`
	Name                 string              `json:"name"`
	TypeID               int                 `json:"typeId"`
	DateUploaded         string              `json:"dateUploaded"`
	RelativePath         string              `json:"relativePath,omitempty"`
	Path                 string              `json:"path"` // S3/Azure path
	EncodedEncryptionKey string              `json:"encodedEncryptionKey"`
	DownloadURL          string              `json:"downloadUrl,omitempty"`
	DecryptedSize        int64               `json:"decryptedSize"`
	PathParts            *CloudFilePathParts `json:"pathParts,omitempty"`
	Storage              *CloudFileStorage   `json:"storage,omitempty"`
	FileChecksums        []FileChecksum      `json:"fileChecksums,omitempty"`
}

// ToCloudFile converts a JobFile to a CloudFile for download operations.
// This allows using job file metadata directly without calling GetFileInfo API.
// 2025-11-20: Added to optimize job downloads by eliminating GetFileInfo call
func (jf *JobFile) ToCloudFile() *CloudFile {
	return &CloudFile{
		ID:                   jf.ID,
		Name:                 jf.Name,
		TypeID:               jf.TypeID,
		Path:                 jf.Path,
		EncodedEncryptionKey: jf.EncodedEncryptionKey,
		DecryptedSize:        jf.DecryptedSize,
		PathParts:            jf.PathParts,
		Storage:              jf.Storage,
		FileChecksums:        jf.FileChecksums,
		IsUploaded:           true, // Job output files are always uploaded
	}
}
