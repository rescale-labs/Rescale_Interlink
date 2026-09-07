package azure

import (
	"context"
	"fmt"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/credentials"
	"github.com/rescale/rescale-int/internal/cloud/providers/testsupport"
	"github.com/rescale/rescale-int/internal/config"
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

// newCountingAzureCredentialsAPI is the credential endpoint with a counter and a
// different SAS token per response, so a test can see how many replacements the
// storage credential actually went through.
func newCountingAzureCredentialsAPI(t *testing.T) (*api.Client, *atomic.Int32) {
	t.Helper()

	var fetches atomic.Int32
	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		generation := fetches.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"storageType":"AzureStorage","sasToken":"sv=2021-06-08&sig=test-%d"}`, generation)
	}))
	t.Cleanup(server.Close)

	return api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"}), &fetches
}

// newCredentialTestAzureClient is an AzureClient whose blob endpoint is a stub:
// these tests drive RetryWithBackoff directly, so the only traffic that matters
// is the credential fetching.
func newCredentialTestAzureClient(t *testing.T, apiClient *api.Client) *AzureClient {
	t.Helper()
	server := httptest.NewTLSServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(nethttp.StatusOK)
	}))
	t.Cleanup(server.Close)

	return &AzureClient{
		storageInfo: &models.StorageInfo{
			StorageType: "AzureStorage",
			ConnectionSettings: models.ConnectionSettings{
				Container:   testContainer,
				AccountName: testAccount,
			},
		},
		credManager: credentials.GetManager(apiClient),
		apiClient:   apiClient,
		httpClient:  testsupport.RedirectingHTTPClient(server.Listener.Addr().String()),
	}
}

// TestLateRejectionLeavesTheReplacementCredentialInPlace is N6 on Azure. The
// retry wrapper invalidated whatever SAS token was current when the rejection
// came back, not the one the attempt actually ran on: a block rejected on
// generation G, released after G had been replaced by H, dropped the healthy H.
func TestLateRejectionLeavesTheReplacementCredentialInPlace(t *testing.T) {
	apiClient, fetches := newCountingAzureCredentialsAPI(t)
	azureClient := newCredentialTestAzureClient(t, apiClient)
	ctx := context.Background()

	// Generation G: what the attempt below runs on.
	if err := azureClient.EnsureFreshCredentials(ctx); err != nil {
		t.Fatalf("failed to install the first credential: %v", err)
	}
	generationG := azureClient.appliedCreds

	replacement := generationG
	rejected := false
	err := azureClient.RetryWithBackoff(ctx, "StageBlock 1", func() error {
		if rejected {
			return nil
		}
		rejected = true

		// While this attempt is in flight, another one replaces G with H.
		azureClient.credManager.InvalidateAzureCredentials(generationG)
		if err := azureClient.EnsureFreshCredentials(ctx); err != nil {
			t.Fatalf("failed to install the replacement credential: %v", err)
		}
		replacement = azureClient.appliedCreds

		// Only now does the service's rejection of generation G arrive.
		return fmt.Errorf("PUT https://testaccount.blob.core.windows.net/: 403 Server failed to authenticate the request, ERROR CODE: AuthenticationFailed")
	})
	if err != nil {
		t.Fatalf("the upload did not recover from a rejected credential: %v", err)
	}
	if replacement == generationG {
		t.Fatal("the test never installed a replacement credential")
	}

	if got := fetches.Load(); got != 2 {
		t.Errorf("credentials were fetched %d time(s), want 2: a rejection of the generation the attempt used must not drop its replacement", got)
	}
	if azureClient.appliedCreds != replacement {
		t.Error("the retry rebuilt the client around a credential other than the replacement that was already installed")
	}
}
