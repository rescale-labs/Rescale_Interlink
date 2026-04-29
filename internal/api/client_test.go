package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
)

// TestNewClientRejectsEmptyBaseURL verifies that NewClient fails with a clear error
// when APIBaseURL is empty, instead of creating a broken client that produces
// "unsupported protocol scheme" errors on every request.
func TestNewClientRejectsEmptyBaseURL(t *testing.T) {
	cfg := &config.Config{
		APIBaseURL: "",
		APIKey:     "test-key",
		ProxyMode:  "no-proxy",
	}

	_, err := NewClient(cfg)
	if err == nil {
		t.Fatal("NewClient() should return error for empty APIBaseURL")
	}

	if !strings.Contains(err.Error(), "API base URL is empty") {
		t.Errorf("NewClient() error = %q, want error containing 'API base URL is empty'", err.Error())
	}
}

// TestNewClientAcceptsValidBaseURL verifies NewClient works with a valid config.
func TestNewClientAcceptsValidBaseURL(t *testing.T) {
	cfg := &config.Config{
		APIBaseURL: "https://platform.rescale.com",
		APIKey:     "test-key",
		ProxyMode:  "no-proxy",
	}

	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient() error = %v, want nil", err)
	}
	if client == nil {
		t.Fatal("NewClient() returned nil client")
	}
}

// TestAuthScheme_TokenForLegacyKeys verifies legacy API tokens continue to use "Token".
func TestAuthScheme_TokenForLegacyKeys(t *testing.T) {
	cases := []string{
		"abc123def456",
		"legacy-api-key-no-dots",
		"two.segment.key-but-not-ey-prefixed",
		"",
	}
	for _, k := range cases {
		if got := authScheme(k); got != "Token" {
			t.Errorf("authScheme(%q) = %q, want Token", k, got)
		}
	}
}

// TestAuthScheme_BearerForJWT verifies JWT-shaped keys switch to "Bearer".
// Rescale session JWTs are three dot-separated base64url segments with the
// header starting with "ey" (base64 of '{"').
func TestAuthScheme_BearerForJWT(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.signature"
	if got := authScheme(jwt); got != "Bearer" {
		t.Errorf("authScheme(jwt) = %q, want Bearer", got)
	}
}

// TestDoRequest_AuthorizationHeader verifies the request actually sends the
// correct scheme end-to-end for both key shapes.
func TestDoRequest_AuthorizationHeader(t *testing.T) {
	cases := []struct {
		name   string
		apiKey string
		want   string
	}{
		{"legacy token", "abc123", "Token abc123"},
		{"jwt bearer", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.sig", "Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.sig"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotAuth string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("{}"))
			}))
			defer server.Close()

			client := NewClientForTest(&config.Config{
				APIBaseURL: server.URL,
				APIKey:     tc.apiKey,
				ProxyMode:  "no-proxy",
			})
			_, _ = client.doRequest(context.Background(), "GET", "/api/v3/ping/", nil)

			if gotAuth != tc.want {
				t.Errorf("Authorization header = %q, want %q", gotAuth, tc.want)
			}
		})
	}
}

// newTestClient creates a Client pointing at the given test server URL.
// Delegates to NewClientForTest to avoid duplicating Client construction.
func newTestClient(t *testing.T, serverURL string) *Client {
	t.Helper()
	return NewClientForTest(&config.Config{
		APIBaseURL: serverURL,
		APIKey:     "test-key",
		ProxyMode:  "no-proxy",
	})
}

// TestGetStorageCredentials_AzureSharedFile verifies that the credentials endpoint
// correctly parses a shared-file Azure response with per-file SAS tokens in paths.
func TestGetStorageCredentials_AzureSharedFile(t *testing.T) {
	// Mock the /api/v3/credentials/ endpoint
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/credentials/" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		resp := map[string]interface{}{
			"storageType": "AzureStorage",
			"storageDir":  "user/abc123/",
			"sasToken":    "container-level-sas",
			"expiration":  "2026-01-01T00:00:00.000Z",
			"paths": []map[string]interface{}{
				{
					"path": "user/abc123/output/results.dat",
					"pathParts": map[string]string{
						"container": "rescale-files",
						"path":      "user/abc123/output/results.dat",
					},
					"sasToken": "per-file-sas-token",
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	ctx := context.Background()

	fileInfo := &models.CloudFile{
		ID: "file123",
		Storage: &models.CloudFileStorage{
			ID:          "storage1",
			StorageType: "AzureStorage",
		},
		PathParts: &models.CloudFilePathParts{
			Container: "rescale-files",
			Path:      "user/abc123/output/results.dat",
		},
	}

	_, azureCreds, err := client.GetStorageCredentials(ctx, fileInfo)
	if err != nil {
		t.Fatalf("GetStorageCredentials() error = %v", err)
	}
	if azureCreds == nil {
		t.Fatal("GetStorageCredentials() returned nil azureCreds")
	}
	if azureCreds.SASToken != "container-level-sas" {
		t.Errorf("SASToken = %q, want %q", azureCreds.SASToken, "container-level-sas")
	}
	if len(azureCreds.Paths) != 1 {
		t.Fatalf("len(Paths) = %d, want 1", len(azureCreds.Paths))
	}
	if azureCreds.Paths[0].SASToken != "per-file-sas-token" {
		t.Errorf("Paths[0].SASToken = %q, want %q", azureCreds.Paths[0].SASToken, "per-file-sas-token")
	}
}

// TestGetStorageCredentials_PermissionDenied verifies clear error on 403.
func TestGetStorageCredentials_PermissionDenied(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"detail": "You do not have permission to access this resource."}`))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	ctx := context.Background()

	_, _, err := client.GetStorageCredentials(ctx, nil)
	if err == nil {
		t.Fatal("GetStorageCredentials() should return error for 403")
	}

	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should mention status 403, got %q", err.Error())
	}
}

// TestGetStorageCredentials_MalformedJSON verifies clear error on invalid JSON.
func TestGetStorageCredentials_MalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{invalid json`))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	ctx := context.Background()

	_, _, err := client.GetStorageCredentials(ctx, nil)
	if err == nil {
		t.Fatal("GetStorageCredentials() should return error for malformed JSON")
	}

	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("error should mention decoding, got %q", err.Error())
	}
}

// FileInfo.ToCloudFile() tests

func TestToCloudFile_Complete(t *testing.T) {
	fi := &FileInfo{
		ID:                   "file123",
		Name:                 "test.dat",
		DecryptedSize:        1024,
		EncodedEncryptionKey: "base64key==",
		IV:                   "base64iv==",
		Owner:                "user1",
		Path:                 "/some/path",
		PathParts:            &models.CloudFilePathParts{Container: "bucket", Path: "user/file"},
		Storage:              &models.CloudFileStorage{ID: "stor1", StorageType: "S3"},
		FileChecksums:        []models.FileChecksum{{HashFunction: "md5", FileHash: "abc123"}},
	}
	cf := fi.ToCloudFile()
	if cf == nil {
		t.Fatal("ToCloudFile() returned nil for complete metadata")
	}
	if cf.ID != "file123" {
		t.Errorf("ID = %q, want %q", cf.ID, "file123")
	}
	if cf.Name != "test.dat" {
		t.Errorf("Name = %q, want %q", cf.Name, "test.dat")
	}
	if cf.EncodedEncryptionKey != "base64key==" {
		t.Errorf("EncodedEncryptionKey = %q, want %q", cf.EncodedEncryptionKey, "base64key==")
	}
	if cf.PathParts == nil || cf.PathParts.Container != "bucket" {
		t.Errorf("PathParts.Container = %v, want %q", cf.PathParts, "bucket")
	}
	if cf.Storage == nil || cf.Storage.StorageType != "S3" {
		t.Errorf("Storage.StorageType = %v, want S3", cf.Storage)
	}
	if cf.DecryptedSize != 1024 {
		t.Errorf("DecryptedSize = %d, want 1024", cf.DecryptedSize)
	}
	if len(cf.FileChecksums) != 1 || cf.FileChecksums[0].FileHash != "abc123" {
		t.Errorf("FileChecksums unexpected: %v", cf.FileChecksums)
	}
}

func TestToCloudFile_MissingEncryptionKey(t *testing.T) {
	fi := &FileInfo{
		ID:            "file123",
		PathParts:     &models.CloudFilePathParts{Container: "bucket", Path: "user/file"},
		Storage:       &models.CloudFileStorage{ID: "stor1", StorageType: "S3"},
		// EncodedEncryptionKey is empty
	}
	cf := fi.ToCloudFile()
	if cf != nil {
		t.Errorf("ToCloudFile() should return nil when encryption key is missing, got %+v", cf)
	}
}

func TestToCloudFile_MissingPathParts(t *testing.T) {
	fi := &FileInfo{
		ID:                   "file123",
		EncodedEncryptionKey: "base64key==",
		Storage:              &models.CloudFileStorage{ID: "stor1", StorageType: "S3"},
		// PathParts is nil
	}
	cf := fi.ToCloudFile()
	if cf != nil {
		t.Errorf("ToCloudFile() should return nil when PathParts is missing, got %+v", cf)
	}
}

func TestToCloudFile_MissingStorage(t *testing.T) {
	fi := &FileInfo{
		ID:                   "file123",
		EncodedEncryptionKey: "base64key==",
		PathParts:            &models.CloudFilePathParts{Container: "bucket", Path: "user/file"},
		// Storage is nil
	}
	cf := fi.ToCloudFile()
	if cf != nil {
		t.Errorf("ToCloudFile() should return nil when Storage is missing, got %+v", cf)
	}
}

// ListFolderContentsStreaming tests

// folderContentsPage builds a JSON response matching the API's folder contents format.
func folderContentsPage(folders []map[string]string, files []map[string]interface{}, nextURL string) []byte {
	results := make([]map[string]interface{}, 0, len(folders)+len(files))
	for _, f := range folders {
		results = append(results, map[string]interface{}{
			"type": "folder",
			"item": map[string]interface{}{
				"id":   f["id"],
				"name": f["name"],
			},
		})
	}
	for _, f := range files {
		results = append(results, map[string]interface{}{
			"type": "file",
			"item": f,
		})
	}
	resp := map[string]interface{}{"results": results}
	if nextURL != "" {
		resp["next"] = nextURL
	}
	b, _ := json.Marshal(resp)
	return b
}

func TestListFolderContentsStreaming_EmitsPerPage(t *testing.T) {
	pageCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pageCount++
		w.Header().Set("Content-Type", "application/json")
		switch pageCount {
		case 1:
			// Use relative URL — extractAPIPath handles full URLs, and ListFolderContentsPage
			// passes relative paths through to doRequest which prepends the base URL
			next := "/api/v3/folders/fold1/contents/?page=2&page_size=1000"
			w.Write(folderContentsPage(
				[]map[string]string{{"id": "sub1", "name": "subfolder1"}},
				[]map[string]interface{}{
					{"id": "f1", "name": "file1.txt", "decryptedSize": json.Number("100"),
						"encodedEncryptionKey": "key1", "iv": "iv1",
						"owner": "u1", "path": "/p",
						"storage": map[string]interface{}{"id": "s1", "storageType": "S3"},
						"pathParts": map[string]interface{}{"container": "b", "path": "p"},
					},
				},
				next,
			))
		case 2:
			w.Write(folderContentsPage(
				nil,
				[]map[string]interface{}{
					{"id": "f2", "name": "file2.txt", "decryptedSize": json.Number("200"),
						"encodedEncryptionKey": "key2", "iv": "iv2",
						"owner": "u1", "path": "/p",
						"storage": map[string]interface{}{"id": "s1", "storageType": "S3"},
						"pathParts": map[string]interface{}{"container": "b", "path": "p"},
					},
				},
				"",
			))
		default:
			t.Errorf("unexpected page request %d", pageCount)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)

	var callbacks int
	var totalFolders, totalFiles int
	err := client.ListFolderContentsStreaming(context.Background(), "fold1",
		func(folders []FolderInfo, files []FileInfo) error {
			callbacks++
			totalFolders += len(folders)
			totalFiles += len(files)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("ListFolderContentsStreaming() error = %v", err)
	}
	if callbacks != 2 {
		t.Errorf("callbacks = %d, want 2 (one per page)", callbacks)
	}
	if totalFolders != 1 {
		t.Errorf("totalFolders = %d, want 1", totalFolders)
	}
	if totalFiles != 2 {
		t.Errorf("totalFiles = %d, want 2", totalFiles)
	}
}

func TestListFolderContentsStreaming_CallbackErrorAbortsPagination(t *testing.T) {
	pageCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pageCount++
		w.Header().Set("Content-Type", "application/json")
		next := "/api/v3/folders/fold1/contents/?page=2&page_size=1000"
		w.Write(folderContentsPage(nil,
			[]map[string]interface{}{
				{"id": "f1", "name": "file1.txt", "decryptedSize": json.Number("100"),
					"encodedEncryptionKey": "key1", "iv": "iv1",
					"owner": "u1", "path": "/p",
					"storage": map[string]interface{}{"id": "s1", "storageType": "S3"},
					"pathParts": map[string]interface{}{"container": "b", "path": "p"},
				},
			},
			next,
		))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)

	callbackErr := errors.New("callback error") // arbitrary non-nil error
	err := client.ListFolderContentsStreaming(context.Background(), "fold1",
		func(folders []FolderInfo, files []FileInfo) error {
			return callbackErr
		},
	)
	if err != callbackErr {
		t.Errorf("error = %v, want %v", err, callbackErr)
	}
	if pageCount != 1 {
		t.Errorf("pageCount = %d, want 1 (should stop after callback error)", pageCount)
	}
}

func TestListFolderContentsStreaming_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next := "/api/v3/folders/fold1/contents/?page=2&page_size=1000"
		w.Header().Set("Content-Type", "application/json")
		w.Write(folderContentsPage(nil,
			[]map[string]interface{}{
				{"id": "f1", "name": "file1.txt", "decryptedSize": json.Number("100"),
					"encodedEncryptionKey": "key1", "iv": "iv1",
					"owner": "u1", "path": "/p",
					"storage": map[string]interface{}{"id": "s1", "storageType": "S3"},
					"pathParts": map[string]interface{}{"container": "b", "path": "p"},
				},
			},
			next,
		))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())

	err := client.ListFolderContentsStreaming(ctx, "fold1",
		func(folders []FolderInfo, files []FileInfo) error {
			cancel() // Cancel after first page
			return nil
		},
	)
	if err != context.Canceled {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

// NewClient rejects non-allowlisted platform URLs
func TestNewClient_RejectsInvalidPlatformURL(t *testing.T) {
	cfg := &config.Config{
		APIBaseURL: "https://evil.example.com",
		APIKey:     "test-key",
		ProxyMode:  "no-proxy",
	}
	_, err := NewClient(cfg)
	if err == nil {
		t.Fatal("NewClient() should reject non-allowlisted URL")
	}
	if !strings.Contains(err.Error(), "invalid platform URL") {
		t.Errorf("error = %q, want 'invalid platform URL'", err.Error())
	}
}

func TestNewClient_RejectsLocalhostURL(t *testing.T) {
	cfg := &config.Config{
		APIBaseURL: "http://127.0.0.1:12345",
		APIKey:     "test-key",
		ProxyMode:  "no-proxy",
	}
	_, err := NewClient(cfg)
	if err == nil {
		t.Fatal("NewClient() should reject localhost URL (no exemption)")
	}
	if !strings.Contains(err.Error(), "invalid platform URL") {
		t.Errorf("error = %q, want 'invalid platform URL'", err.Error())
	}
}
