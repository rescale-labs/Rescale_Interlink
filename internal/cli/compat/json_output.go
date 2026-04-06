package compat

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/rescale/rescale-int/internal/models"
)

// rescaleCLITimeFormat matches rescale-cli's timestamp format: ISO 8601 with
// millisecond precision and explicit +00:00 offset (Go's time.RFC3339 uses "Z").
const rescaleCLITimeFormat = "2006-01-02T15:04:05.000+00:00"

func formatRescaleTime(t time.Time) string {
	return t.UTC().Format(rescaleCLITimeFormat)
}

// compatFileEntry is a compat-specific DTO shaped exactly to the rescale-cli
// upload/download -e fixture. Fields are declared in fixture order so
// json.Marshal produces matching field ordering.
type compatFileEntry struct {
	Name                 string                   `json:"name"`
	PathParts            *models.CloudFilePathParts `json:"pathParts"`
	Storage              *compatFileStorage       `json:"storage"`
	EncodedEncryptionKey string                   `json:"encodedEncryptionKey"`
	IsUploaded           bool                     `json:"isUploaded"`
	DecryptedSize        int64                    `json:"decryptedSize"`
	TypeID               int                      `json:"typeId"`
	FileChecksums        []models.FileChecksum    `json:"fileChecksums"`
	ID                   string                   `json:"id"`
}

// compatFileStorage mirrors CloudFileStorage but always includes connectionSettings
// (no omitempty) to match rescale-cli output.
type compatFileStorage struct {
	StorageType        string                      `json:"storageType"`
	ID                 string                      `json:"id"`
	EncryptionType     string                      `json:"encryptionType"`
	ConnectionSettings models.ConnectionSettings   `json:"connectionSettings"`
}

func toCompatFileEntry(cf *models.CloudFile) compatFileEntry {
	var storage *compatFileStorage
	if cf.Storage != nil {
		storage = &compatFileStorage{
			StorageType:        cf.Storage.StorageType,
			ID:                 cf.Storage.ID,
			EncryptionType:     cf.Storage.EncryptionType,
			ConnectionSettings: cf.Storage.ConnectionSettings,
		}
	}
	checksums := cf.FileChecksums
	if checksums == nil {
		checksums = []models.FileChecksum{}
	}
	return compatFileEntry{
		Name:                 cf.Name,
		PathParts:            cf.PathParts,
		Storage:              storage,
		EncodedEncryptionKey: cf.EncodedEncryptionKey,
		IsUploaded:           cf.IsUploaded,
		DecryptedSize:        cf.DecryptedSize,
		TypeID:               cf.TypeID,
		FileChecksums:        checksums,
		ID:                   cf.ID,
	}
}

// transferEnvelope is the {success, startTime, endTime, files} wrapper used by
// rescale-cli's upload -e and download-file -e output.
type transferEnvelope struct {
	Success   bool        `json:"success"`
	StartTime string      `json:"startTime"`
	EndTime   string      `json:"endTime"`
	Files     interface{} `json:"files"`
}

func writeTransferEnvelope(w io.Writer, success bool, start, end time.Time, files []compatFileEntry) error {
	env := transferEnvelope{
		Success:   success,
		StartTime: formatRescaleTime(start),
		EndTime:   formatRescaleTime(end),
		Files:     files,
	}
	return writeJSON(w, env)
}

func writeTransferEnvelopeRaw(w io.Writer, success bool, start, end time.Time, files []json.RawMessage) error {
	env := transferEnvelope{
		Success:   success,
		StartTime: formatRescaleTime(start),
		EndTime:   formatRescaleTime(end),
		Files:     files,
	}
	return writeJSON(w, env)
}

// writeJSON writes compact single-line JSON followed by a newline.
func writeJSON(w io.Writer, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}
