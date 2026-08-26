package azure

import (
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/models"
)

// credsWithPath builds credentials holding one per-file SAS entry for path.
func credsWithPath(containerSAS, path, perFileSAS string) *models.AzureCredentials {
	return &models.AzureCredentials{
		SASToken: containerSAS,
		Paths: []models.AzureCredentialPath{
			{
				Path:      path,
				PathParts: &models.CloudFilePathParts{Container: "rescale-files", Path: path},
				SASToken:  perFileSAS,
			},
		},
	}
}

func TestBuildSASURL(t *testing.T) {
	tests := []struct {
		name        string
		accountName string
		storageAcct string
		creds       *models.AzureCredentials
		fileInfo    *models.CloudFile
		wantErr     string   // when set, buildSASURL must fail and name this
		wantExact   string   // when set, the URL must match exactly
		wantSubstr  []string // fragments the URL must contain
		wantAbsent  []string // fragments the URL must not contain
	}{
		{
			name:        "account name from connection settings",
			accountName: "myaccount",
			creds:       &models.AzureCredentials{SASToken: "sv=2021-06-08&ss=b&sig=abc"},
			wantExact:   "https://myaccount.blob.core.windows.net/?sv=2021-06-08&ss=b&sig=abc",
		},
		{
			name:        "falls back to StorageAccount",
			storageAcct: "legacyaccount",
			creds:       &models.AzureCredentials{SASToken: "sv=2021-06-08&sig=def"},
			wantSubstr:  []string{"legacyaccount.blob.core.windows.net"},
		},
		{
			name:    "no account name at all",
			creds:   &models.AzureCredentials{SASToken: "sv=2021-06-08&sig=ghi"},
			wantErr: "account name not found",
		},
		{
			// A shared job's file carries its own SAS; the container-level one
			// would not grant access to it.
			name:        "per-file SAS wins for a shared file",
			accountName: "sharedaccount",
			creds:       credsWithPath("container-level-sas", "user/abc/shared-output.dat", "per-file-sas-for-shared"),
			fileInfo: &models.CloudFile{
				PathParts: &models.CloudFilePathParts{Container: "rescale-files", Path: "user/abc/shared-output.dat"},
			},
			wantSubstr: []string{"per-file-sas-for-shared"},
			wantAbsent: []string{"container-level-sas"},
		},
		{
			name:        "falls back to container SAS when no path matches",
			accountName: "myaccount",
			creds:       credsWithPath("container-level-sas", "user/abc/different-file.dat", "per-file-sas-other"),
			fileInfo: &models.CloudFile{
				PathParts: &models.CloudFilePathParts{Container: "rescale-files", Path: "user/abc/wanted-file.dat"},
			},
			wantSubstr: []string{"container-level-sas"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storageInfo := &models.StorageInfo{
				ConnectionSettings: models.ConnectionSettings{
					AccountName:    tt.accountName,
					StorageAccount: tt.storageAcct,
				},
			}

			var url string
			var err error
			if tt.fileInfo != nil {
				url, err = buildSASURL(storageInfo, tt.creds, tt.fileInfo)
			} else {
				url, err = buildSASURL(storageInfo, tt.creds)
			}

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("buildSASURL() = %q, want error", url)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("buildSASURL() error = %q, want mention of %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildSASURL() error = %v", err)
			}
			if tt.wantExact != "" && url != tt.wantExact {
				t.Errorf("buildSASURL() = %q, want %q", url, tt.wantExact)
			}
			for _, want := range tt.wantSubstr {
				if !strings.Contains(url, want) {
					t.Errorf("buildSASURL() = %q, should contain %q", url, want)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(url, absent) {
					t.Errorf("buildSASURL() = %q, should not contain %q", url, absent)
				}
			}
		})
	}
}

func TestGetPerFileSASToken(t *testing.T) {
	tests := []struct {
		name    string
		creds   *models.AzureCredentials
		lookup  string
		wantSAS string
	}{
		{
			name:    "path match returns the per-file token",
			creds:   credsWithPath("container-level-sas", "user/abc/file1.dat", "per-file-sas-token"),
			lookup:  "user/abc/file1.dat",
			wantSAS: "per-file-sas-token",
		},
		{
			name:    "no match falls back to the container token",
			creds:   credsWithPath("container-level-sas", "user/abc/other.dat", "per-file-sas-token"),
			lookup:  "user/abc/wanted.dat",
			wantSAS: "container-level-sas",
		},
		{
			name:    "empty path list falls back to the container token",
			creds:   &models.AzureCredentials{SASToken: "container-level-sas", Paths: []models.AzureCredentialPath{}},
			lookup:  "user/abc/file1.dat",
			wantSAS: "container-level-sas",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetPerFileSASToken(tt.creds, tt.lookup); got != tt.wantSAS {
				t.Errorf("GetPerFileSASToken() = %q, want %q", got, tt.wantSAS)
			}
		})
	}
}
