package azure

import (
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/models"
)

// --- buildSASURL tests ---

func TestBuildSASURL_WithAccountName(t *testing.T) {
	storageInfo := &models.StorageInfo{
		ConnectionSettings: models.ConnectionSettings{
			AccountName: "myaccount",
		},
	}
	creds := &models.AzureCredentials{
		SASToken: "sv=2021-06-08&ss=b&sig=abc",
	}

	url, err := buildSASURL(storageInfo, creds)
	if err != nil {
		t.Fatalf("buildSASURL() error = %v", err)
	}

	expected := "https://myaccount.blob.core.windows.net/?sv=2021-06-08&ss=b&sig=abc"
	if url != expected {
		t.Errorf("buildSASURL() = %q, want %q", url, expected)
	}
}

func TestBuildSASURL_FallbackToStorageAccount(t *testing.T) {
	storageInfo := &models.StorageInfo{
		ConnectionSettings: models.ConnectionSettings{
			AccountName:    "",
			StorageAccount: "legacyaccount",
		},
	}
	creds := &models.AzureCredentials{
		SASToken: "sv=2021-06-08&sig=def",
	}

	url, err := buildSASURL(storageInfo, creds)
	if err != nil {
		t.Fatalf("buildSASURL() error = %v", err)
	}

	if !strings.Contains(url, "legacyaccount.blob.core.windows.net") {
		t.Errorf("buildSASURL() should use StorageAccount fallback, got %q", url)
	}
}

func TestBuildSASURL_NoAccountName(t *testing.T) {
	storageInfo := &models.StorageInfo{
		ConnectionSettings: models.ConnectionSettings{
			AccountName:    "",
			StorageAccount: "",
		},
	}
	creds := &models.AzureCredentials{
		SASToken: "sv=2021-06-08&sig=ghi",
	}

	_, err := buildSASURL(storageInfo, creds)
	if err == nil {
		t.Fatal("buildSASURL() should return error when no account name available")
	}

	if !strings.Contains(err.Error(), "account name not found") {
		t.Errorf("buildSASURL() error = %q, want error mentioning account name", err.Error())
	}
}

func TestBuildSASURL_UsesPerFileSASForSharedFile(t *testing.T) {
	storageInfo := &models.StorageInfo{
		ConnectionSettings: models.ConnectionSettings{
			AccountName: "sharedaccount",
		},
	}
	creds := &models.AzureCredentials{
		SASToken: "container-level-sas",
		Paths: []models.AzureCredentialPath{
			{
				Path: "user/abc/shared-output.dat",
				PathParts: &models.CloudFilePathParts{
					Container: "rescale-files",
					Path:      "user/abc/shared-output.dat",
				},
				SASToken: "per-file-sas-for-shared",
			},
		},
	}
	fileInfo := &models.CloudFile{
		PathParts: &models.CloudFilePathParts{
			Container: "rescale-files",
			Path:      "user/abc/shared-output.dat",
		},
	}

	url, err := buildSASURL(storageInfo, creds, fileInfo)
	if err != nil {
		t.Fatalf("buildSASURL() error = %v", err)
	}

	// Should use per-file SAS token, not container-level
	if !strings.Contains(url, "per-file-sas-for-shared") {
		t.Errorf("buildSASURL() should use per-file SAS token for shared files, got %q", url)
	}
	if strings.Contains(url, "container-level-sas") {
		t.Errorf("buildSASURL() should NOT use container-level SAS for shared files, got %q", url)
	}
}

func TestBuildSASURL_FallsBackToContainerSASWhenNoMatch(t *testing.T) {
	storageInfo := &models.StorageInfo{
		ConnectionSettings: models.ConnectionSettings{
			AccountName: "myaccount",
		},
	}
	creds := &models.AzureCredentials{
		SASToken: "container-level-sas",
		Paths: []models.AzureCredentialPath{
			{
				Path: "user/abc/different-file.dat",
				PathParts: &models.CloudFilePathParts{
					Container: "rescale-files",
					Path:      "user/abc/different-file.dat",
				},
				SASToken: "per-file-sas-other",
			},
		},
	}
	fileInfo := &models.CloudFile{
		PathParts: &models.CloudFilePathParts{
			Container: "rescale-files",
			Path:      "user/abc/wanted-file.dat",
		},
	}

	url, err := buildSASURL(storageInfo, creds, fileInfo)
	if err != nil {
		t.Fatalf("buildSASURL() error = %v", err)
	}

	// No match — should fall back to container-level SAS
	if !strings.Contains(url, "container-level-sas") {
		t.Errorf("buildSASURL() should fall back to container-level SAS when no match, got %q", url)
	}
}

// --- GetPerFileSASToken tests ---

func TestGetPerFileSASToken_Match(t *testing.T) {
	creds := &models.AzureCredentials{
		SASToken: "container-level-sas",
		Paths: []models.AzureCredentialPath{
			{
				Path: "user/abc/file1.dat",
				PathParts: &models.CloudFilePathParts{
					Container: "rescale-files",
					Path:      "user/abc/file1.dat",
				},
				SASToken: "per-file-sas-token",
			},
		},
	}

	token := GetPerFileSASToken(creds, "user/abc/file1.dat")
	if token != "per-file-sas-token" {
		t.Errorf("GetPerFileSASToken() = %q, want %q", token, "per-file-sas-token")
	}
}

func TestGetPerFileSASToken_NoMatch(t *testing.T) {
	creds := &models.AzureCredentials{
		SASToken: "container-level-sas",
		Paths: []models.AzureCredentialPath{
			{
				Path: "user/abc/other.dat",
				PathParts: &models.CloudFilePathParts{
					Container: "rescale-files",
					Path:      "user/abc/other.dat",
				},
				SASToken: "per-file-sas-token",
			},
		},
	}

	token := GetPerFileSASToken(creds, "user/abc/wanted.dat")
	if token != "container-level-sas" {
		t.Errorf("GetPerFileSASToken() = %q, want container-level fallback %q", token, "container-level-sas")
	}
}

func TestGetPerFileSASToken_EmptyPaths(t *testing.T) {
	creds := &models.AzureCredentials{
		SASToken: "container-level-sas",
		Paths:    []models.AzureCredentialPath{},
	}

	token := GetPerFileSASToken(creds, "user/abc/file1.dat")
	if token != "container-level-sas" {
		t.Errorf("GetPerFileSASToken() = %q, want container-level fallback %q", token, "container-level-sas")
	}
}
