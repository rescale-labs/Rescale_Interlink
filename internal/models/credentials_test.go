package models

import (
	"encoding/json"
	"testing"
)

// TestAzureCredentialsUnmarshal_SharedFile tests parsing a real API response
// for shared file credentials where paths contains objects with per-file SAS tokens.
func TestAzureCredentialsUnmarshal_SharedFile(t *testing.T) {
	// Real API response format for shared-file credential requests
	jsonData := `{
		"storageType": "AzureStorage",
		"storageDir": "user/abc123/",
		"sasToken": "sv=2021-06-08&ss=b&srt=sco&sp=r&se=2026-01-01T00:00:00Z&sig=container-level-sig",
		"expiration": "2026-01-01T00:00:00.000Z",
		"paths": [
			{
				"path": "user/abc123/output/results.dat",
				"pathParts": {
					"container": "rescale-files",
					"path": "user/abc123/output/results.dat"
				},
				"sasToken": "sv=2021-06-08&sr=b&sp=r&se=2026-01-01T00:00:00Z&sig=per-file-sig"
			}
		]
	}`

	var creds AzureCredentials
	err := json.Unmarshal([]byte(jsonData), &creds)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if creds.StorageType != "AzureStorage" {
		t.Errorf("StorageType = %q, want %q", creds.StorageType, "AzureStorage")
	}
	if creds.SASToken == "" {
		t.Error("SASToken should not be empty")
	}
	if len(creds.Paths) != 1 {
		t.Fatalf("len(Paths) = %d, want 1", len(creds.Paths))
	}

	p := creds.Paths[0]
	if p.SASToken == "" {
		t.Error("Paths[0].SASToken should not be empty")
	}
	if p.SASToken == creds.SASToken {
		t.Error("Per-file SASToken should differ from container-level SASToken")
	}
	if p.PathParts == nil {
		t.Fatal("Paths[0].PathParts should not be nil")
	}
	if p.PathParts.Container != "rescale-files" {
		t.Errorf("PathParts.Container = %q, want %q", p.PathParts.Container, "rescale-files")
	}
	if p.PathParts.Path != "user/abc123/output/results.dat" {
		t.Errorf("PathParts.Path = %q, want %q", p.PathParts.Path, "user/abc123/output/results.dat")
	}
}

// TestAzureCredentialsUnmarshal_EmptyPaths tests that an empty paths array parses correctly.
func TestAzureCredentialsUnmarshal_EmptyPaths(t *testing.T) {
	jsonData := `{
		"storageType": "AzureStorage",
		"storageDir": "user/abc123/",
		"sasToken": "sv=2021-06-08&ss=b&srt=sco&sp=r&se=2026-01-01T00:00:00Z&sig=abc",
		"paths": []
	}`

	var creds AzureCredentials
	err := json.Unmarshal([]byte(jsonData), &creds)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(creds.Paths) != 0 {
		t.Errorf("len(Paths) = %d, want 0", len(creds.Paths))
	}
}

// TestAzureCredentialsUnmarshal_NoPaths tests that a missing paths field parses correctly.
func TestAzureCredentialsUnmarshal_NoPaths(t *testing.T) {
	jsonData := `{
		"storageType": "AzureStorage",
		"storageDir": "user/abc123/",
		"sasToken": "sv=2021-06-08&ss=b&srt=sco&sp=r&se=2026-01-01T00:00:00Z&sig=abc"
	}`

	var creds AzureCredentials
	err := json.Unmarshal([]byte(jsonData), &creds)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if creds.Paths != nil {
		t.Errorf("Paths should be nil when field is missing, got %v", creds.Paths)
	}
}

// TestAzureCredentialsUnmarshal_MultiplePaths tests parsing multiple per-file credential entries.
func TestAzureCredentialsUnmarshal_MultiplePaths(t *testing.T) {
	jsonData := `{
		"storageType": "AzureStorage",
		"storageDir": "user/abc123/",
		"sasToken": "container-sas",
		"paths": [
			{
				"path": "user/abc123/file1.dat",
				"pathParts": {"container": "rescale-files", "path": "user/abc123/file1.dat"},
				"sasToken": "sas-for-file1"
			},
			{
				"path": "user/abc123/file2.dat",
				"pathParts": {"container": "rescale-files", "path": "user/abc123/file2.dat"},
				"sasToken": "sas-for-file2"
			}
		]
	}`

	var creds AzureCredentials
	err := json.Unmarshal([]byte(jsonData), &creds)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(creds.Paths) != 2 {
		t.Fatalf("len(Paths) = %d, want 2", len(creds.Paths))
	}
	if creds.Paths[0].SASToken != "sas-for-file1" {
		t.Errorf("Paths[0].SASToken = %q, want %q", creds.Paths[0].SASToken, "sas-for-file1")
	}
	if creds.Paths[1].SASToken != "sas-for-file2" {
		t.Errorf("Paths[1].SASToken = %q, want %q", creds.Paths[1].SASToken, "sas-for-file2")
	}
}
