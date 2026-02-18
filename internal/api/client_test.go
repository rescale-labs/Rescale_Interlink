package api

import (
	"context"
	"encoding/json"
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

// newTestClient creates a Client pointing at the given test server URL.
func newTestClient(t *testing.T, serverURL string) *Client {
	t.Helper()
	cfg := &config.Config{
		APIBaseURL: serverURL,
		APIKey:     "test-key",
		ProxyMode:  "no-proxy",
	}
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
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
