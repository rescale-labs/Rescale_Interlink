package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/ratelimit"
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

func TestListFilesPage_NormalizesFullNextURLAndCursor(t *testing.T) {
	var server *httptest.Server
	fullNext := ""
	seenPage2 := false
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/files/" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Query().Get("page") {
		case "":
			fullNext = server.URL + "/api/v3/files/?page=2&page_size=25"
			json.NewEncoder(w).Encode(map[string]interface{}{
				"count":    2,
				"next":     fullNext,
				"previous": "",
				"results":  []map[string]interface{}{},
			})
		case "2":
			seenPage2 = true
			if got := r.URL.Query().Get("page_size"); got != "25" {
				t.Errorf("page_size = %q, want 25", got)
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"count":    2,
				"next":     "",
				"previous": "",
				"results":  []map[string]interface{}{},
			})
		default:
			http.Error(w, "unexpected page", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	page, err := client.ListFilesPage(context.Background(), "", 25)
	if err != nil {
		t.Fatalf("ListFilesPage(first page) error = %v", err)
	}
	if page.NextURL != "/api/v3/files/?page=2&page_size=25" {
		t.Fatalf("NextURL = %q, want normalized API path", page.NextURL)
	}

	if _, err := client.ListFilesPage(context.Background(), fullNext, 25); err != nil {
		t.Fatalf("ListFilesPage(full cursor) error = %v", err)
	}
	if !seenPage2 {
		t.Fatal("server did not receive normalized page 2 request")
	}
}

// TestListFilesPageWithOptions_BuildsQuery covers the filtered request shape and,
// crucially, pins the unfiltered one: passing no options must produce exactly the
// request every pre-existing caller made, so adding filters cannot change what an
// unfiltered file listing returns.
func TestListFilesPageWithOptions_BuildsQuery(t *testing.T) {
	tests := []struct {
		name    string
		options *FileListOptions
		// wantURI is the exact request URI when it must be byte-identical;
		// wantParams is used when only individual parameters matter.
		wantURI    string
		wantParams map[string]string
		wantAbsent []string
	}{
		{
			name:    "nil options is byte-identical to the unfiltered listing",
			options: nil,
			wantURI: "/api/v3/files/?page_size=25&ordering=-dateUploaded",
		},
		{
			name:    "empty options is byte-identical to the unfiltered listing",
			options: &FileListOptions{},
			wantURI: "/api/v3/files/?page_size=25&ordering=-dateUploaded",
		},
		{
			name:       "owner my-files",
			options:    &FileListOptions{OwnerFilter: "1"},
			wantParams: map[string]string{"owner": "1", "ordering": "-dateUploaded", "page_size": "25"},
			wantAbsent: []string{"search"},
		},
		{
			name:       "owner shared-with-me",
			options:    &FileListOptions{OwnerFilter: "2"},
			wantParams: map[string]string{"owner": "2"},
		},
		{
			name:       "search term is percent-encoded",
			options:    &FileListOptions{SearchQuery: "my mesh & stuff/v2"},
			wantParams: map[string]string{"search": "my mesh & stuff/v2"},
		},
		{
			name:       "custom ordering replaces the default",
			options:    &FileListOptions{Ordering: "-decryptedSize"},
			wantParams: map[string]string{"ordering": "-decryptedSize"},
			wantAbsent: []string{"owner", "search"},
		},
		{
			name:       "owner, search and ordering combined",
			options:    &FileListOptions{OwnerFilter: "1", SearchQuery: "mesh", Ordering: "name"},
			wantParams: map[string]string{"owner": "1", "search": "mesh", "ordering": "name", "page_size": "25"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotURI string
			var gotRawQuery string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v3/files/" {
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
				gotURI = r.URL.RequestURI()
				gotRawQuery = r.URL.RawQuery
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"count":    0,
					"next":     "",
					"previous": "",
					"results":  []map[string]interface{}{},
				})
			}))
			defer server.Close()

			client := newTestClient(t, server.URL)
			if _, err := client.ListFilesPageWithOptions(context.Background(), "", 25, tc.options); err != nil {
				t.Fatalf("ListFilesPageWithOptions() error = %v", err)
			}

			if tc.wantURI != "" && gotURI != tc.wantURI {
				t.Fatalf("request URI = %q, want %q", gotURI, tc.wantURI)
			}

			q, err := neturl.ParseQuery(gotRawQuery)
			if err != nil {
				t.Fatalf("ParseQuery(%q) error = %v", gotRawQuery, err)
			}
			for key, want := range tc.wantParams {
				if got := q.Get(key); got != want {
					t.Errorf("query %s = %q, want %q", key, got, want)
				}
			}
			for _, key := range tc.wantAbsent {
				if _, ok := q[key]; ok {
					t.Errorf("query %s = %q, want absent", key, q.Get(key))
				}
			}
		})
	}
}

// TestListFilesPage_UnfilteredRequestMatchesWithOptions verifies the convenience
// wrapper and the options-aware call issue the same unfiltered request.
func TestListFilesPage_UnfilteredRequestMatchesWithOptions(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/files/" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		seen = append(seen, r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"count": 0, "next": "", "previous": "", "results": []map[string]interface{}{},
		})
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	if _, err := client.ListFilesPage(context.Background(), "", 0); err != nil {
		t.Fatalf("ListFilesPage() error = %v", err)
	}
	if _, err := client.ListFilesPageWithOptions(context.Background(), "", 0, nil); err != nil {
		t.Fatalf("ListFilesPageWithOptions() error = %v", err)
	}

	if len(seen) != 2 {
		t.Fatalf("saw %d requests, want 2", len(seen))
	}
	if seen[0] != seen[1] {
		t.Errorf("requests differ:\n  ListFilesPage            = %q\n  ListFilesPageWithOptions = %q", seen[0], seen[1])
	}
	if seen[0] != "/api/v3/files/?page_size=25&ordering=-dateUploaded" {
		t.Errorf("request URI = %q, want the historical unfiltered shape", seen[0])
	}
}

func TestListTrashBinPage_ParsesFilesymlinkID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/users/me/folders/trash-bin/" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if got := r.URL.Query().Get("page_size"); got != "50" {
			t.Errorf("page_size = %q, want 50", got)
		}
		w.Header().Set("Content-Type", "application/json")
		// Mirrors a real captured trash-bin response: filesymlink entries carry
		// only {type, item} with no top-level id. The id used for recover/delete
		// (SymlinkID) is item.id.
		json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []map[string]interface{}{
				{
					"type": "filesymlink",
					"item": map[string]interface{}{
						"id":            "tqmdnn",
						"name":          "result.dat",
						"decryptedSize": "12345",
						"dateInserted":  "2026-05-01T12:00:00Z",
					},
				},
				{
					"type": "folder",
					"item": map[string]interface{}{
						"id":          "folder-789",
						"name":        "job-output",
						"dateCreated": "2026-05-02T12:00:00Z",
					},
				},
			},
			"next": nil,
		})
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	contents, err := client.ListTrashBinPage(context.Background(), "", 50)
	if err != nil {
		t.Fatalf("ListTrashBinPage() error = %v", err)
	}
	if len(contents.Files) != 1 {
		t.Fatalf("len(Files) = %d, want 1", len(contents.Files))
	}
	if contents.Files[0].ID != "tqmdnn" {
		t.Errorf("Files[0].ID = %q, want tqmdnn", contents.Files[0].ID)
	}
	if contents.Files[0].SymlinkID != "tqmdnn" {
		t.Errorf("Files[0].SymlinkID = %q, want tqmdnn", contents.Files[0].SymlinkID)
	}
	if len(contents.Folders) != 1 || contents.Folders[0].ID != "folder-789" {
		t.Fatalf("Folders = %#v, want folder-789", contents.Folders)
	}
}

func TestPostTrashBinAction_SendsMixedPayload(t *testing.T) {
	var gotPayload map[string][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v3/users/me/folders/trash-bin/recover/" {
			t.Errorf("path = %s, want recover trash endpoint", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	err := client.PostTrashBinAction(
		context.Background(),
		"recover",
		[]string{"filesymlink-123"},
		[]string{"folder-789"},
	)
	if err != nil {
		t.Fatalf("PostTrashBinAction() error = %v", err)
	}
	if got := gotPayload["filesymlink_ids"]; len(got) != 1 || got[0] != "filesymlink-123" {
		t.Errorf("filesymlink_ids = %#v, want filesymlink-123", got)
	}
	if got := gotPayload["folderIds"]; len(got) != 1 || got[0] != "folder-789" {
		t.Errorf("folderIds = %#v, want folder-789", got)
	}
}

func TestPostTrashBinAction_RejectsInvalidAction(t *testing.T) {
	client := newTestClient(t, "https://platform.rescale.com")
	err := client.PostTrashBinAction(context.Background(), "empty", nil, nil)
	if err == nil {
		t.Fatal("PostTrashBinAction() should reject invalid actions")
	}
	if !strings.Contains(err.Error(), "invalid trash-bin action") {
		t.Errorf("error = %q, want invalid action message", err.Error())
	}
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
		ID:        "file123",
		PathParts: &models.CloudFilePathParts{Container: "bucket", Path: "user/file"},
		Storage:   &models.CloudFileStorage{ID: "stor1", StorageType: "S3"},
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
						"storage":   map[string]interface{}{"id": "s1", "storageType": "S3"},
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
						"storage":   map[string]interface{}{"id": "s1", "storageType": "S3"},
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
					"storage":   map[string]interface{}{"id": "s1", "storageType": "S3"},
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
					"storage":   map[string]interface{}{"id": "s1", "storageType": "S3"},
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

func TestCreateJob_AppendsHintForInteractiveFlagError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"jobanalyses":[{"command":["the 'interactive' flag must be present"]}]}`))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	_, err := client.CreateJob(context.Background(), models.JobRequest{Name: "t"})
	if err == nil {
		t.Fatal("CreateJob() should return error on 400")
	}
	if !strings.Contains(err.Error(), "interactive' flag must be present") {
		t.Errorf("error should preserve original body, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "Hint:") {
		t.Errorf("error should append actionable hint, got %q", err.Error())
	}
}

func TestCreateJob_NoHintForGenericError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"name":["This field is required."]}`))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	_, err := client.CreateJob(context.Background(), models.JobRequest{Name: "t"})
	if err == nil {
		t.Fatal("CreateJob() should return error on 400")
	}
	if strings.Contains(err.Error(), "Hint:") {
		t.Errorf("generic error should not append interactive hint, got %q", err.Error())
	}
}

func TestArchiveContents_PostsCorrectURLAndBody(t *testing.T) {
	var gotPath string
	var gotBody map[string][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	err := client.ArchiveContents(context.Background(), "QWGjp", []string{"f1", "f2"}, []string{"d1"})
	if err != nil {
		t.Fatalf("ArchiveContents() error = %v", err)
	}
	if gotPath != "/api/v3/folders/QWGjp/contents/archive/" {
		t.Errorf("path = %q, want folder-scoped archive endpoint", gotPath)
	}
	if len(gotBody["fileIds"]) != 2 || gotBody["fileIds"][0] != "f1" {
		t.Errorf("fileIds = %v, want [f1 f2]", gotBody["fileIds"])
	}
	if len(gotBody["folderIds"]) != 1 || gotBody["folderIds"][0] != "d1" {
		t.Errorf("folderIds = %v, want [d1]", gotBody["folderIds"])
	}
}

func TestArchiveContents_RequiresParentFolder(t *testing.T) {
	client := newTestClient(t, "http://example.invalid")
	if err := client.ArchiveContents(context.Background(), "", []string{"f1"}, nil); err == nil {
		t.Fatal("ArchiveContents() should error when folderID is empty")
	}
}

func TestArchiveContents_EncodesEmptyListsNotNull(t *testing.T) {
	var raw string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		raw = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	if err := client.ArchiveContents(context.Background(), "QWGjp", []string{"f1"}, nil); err != nil {
		t.Fatalf("ArchiveContents() error = %v", err)
	}
	if strings.Contains(raw, "null") {
		t.Errorf("body should encode empty lists as [], not null: %s", raw)
	}
}

// TestListJobsPage verifies deep pages are addressed by page number in a
// single request. The live jobs endpoint honors page/page_size and ignores
// limit/offset, so page-number addressing is the only correct windowing.
func TestListJobsPage(t *testing.T) {
	makeJobs := func(start, n int) []models.JobResponse {
		jobs := make([]models.JobResponse, n)
		for i := range jobs {
			jobs[i] = models.JobResponse{ID: "job-" + strconv.Itoa(start+i)}
		}
		return jobs
	}

	t.Run("requests exactly one page with more remaining", func(t *testing.T) {
		var gotQueries []string
		var server *httptest.Server
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v3/jobs/" {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			gotQueries = append(gotQueries, r.URL.RawQuery)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"next":    server.URL + "/api/v3/jobs/?ordering=-dateInserted&page=4&page_size=50",
				"results": makeJobs(100, 50),
			})
		}))
		defer server.Close()

		client := newTestClient(t, server.URL)
		jobs, hasMore, err := client.ListJobsPage(context.Background(), 3, 50)
		if err != nil {
			t.Fatalf("ListJobsPage() error = %v", err)
		}
		if len(jobs) != 50 || !hasMore {
			t.Errorf("got %d jobs, hasMore=%v; want 50, true", len(jobs), hasMore)
		}
		if len(gotQueries) != 1 {
			t.Fatalf("server saw %d requests, want 1: %v", len(gotQueries), gotQueries)
		}
		for _, want := range []string{"page=3", "page_size=50", "ordering=-dateInserted"} {
			if !strings.Contains(gotQueries[0], want) {
				t.Errorf("request query %q missing %q", gotQueries[0], want)
			}
		}
	})

	t.Run("last page reports no more", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"next":    nil,
				"results": makeJobs(150, 12),
			})
		}))
		defer server.Close()

		client := newTestClient(t, server.URL)
		jobs, hasMore, err := client.ListJobsPage(context.Background(), 4, 50)
		if err != nil {
			t.Fatalf("ListJobsPage() error = %v", err)
		}
		if len(jobs) != 12 || hasMore {
			t.Errorf("got %d jobs, hasMore=%v; want 12, false", len(jobs), hasMore)
		}
	})
}

// --- Retry policy: backoff clamping, retry budget, notices, body handling ---

// newRetryTestPolicy builds a policy with an isolated limiter store and a
// notice recorder.
func newRetryTestPolicy(t *testing.T, maxElapsed time.Duration) (*retryPolicy, func() []string) {
	t.Helper()

	var mu sync.Mutex
	var notices []string
	policy := &retryPolicy{
		store:      ratelimit.NewTestStore(),
		baseURL:    "https://platform.rescale.com",
		apiKey:     "test-key",
		maxElapsed: maxElapsed,
		notify: func(_, message string) {
			mu.Lock()
			defer mu.Unlock()
			notices = append(notices, message)
		},
	}
	return policy, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), notices...)
	}
}

// retryTestResponse fabricates a response carrying the request that produced it,
// which is what the backoff and retry callbacks read.
func retryTestResponse(status int, retryAfter string) *http.Response {
	u, _ := neturl.Parse("https://platform.rescale.com/api/v3/files/?page=2")
	header := http.Header{}
	if retryAfter != "" {
		header.Set("Retry-After", retryAfter)
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Request:    &http.Request{Method: "GET", URL: u},
	}
}

// TestRetryBackoffClampsToWaitMax covers the sleep math: go-retryablehttp's
// DefaultBackoff returns Retry-After verbatim on 429/503 without applying
// RetryWaitMax, and a 429 cooldown can be just as long.
func TestRetryBackoffClampsToWaitMax(t *testing.T) {
	const (
		waitMin = 1 * time.Second
		waitMax = 30 * time.Second
	)

	t.Run("retry-after far beyond max is clamped and announced", func(t *testing.T) {
		policy, notices := newRetryTestPolicy(t, 0)

		got := policy.backoff(waitMin, waitMax, 0, retryTestResponse(429, "1800"))
		if got != waitMax {
			t.Errorf("backoff() = %v, want %v (Retry-After 1800s clamped)", got, waitMax)
		}

		msgs := notices()
		if len(msgs) != 1 {
			t.Fatalf("expected one notice before a %v sleep, got %d: %v", got, len(msgs), msgs)
		}
		for _, want := range []string{"Waiting 30s", "GET /api/v3/files/", "HTTP 429", "server asked for 30m0s", "capped at 30s"} {
			if !strings.Contains(msgs[0], want) {
				t.Errorf("notice %q missing %q", msgs[0], want)
			}
		}
	})

	t.Run("503 retry-after is clamped too", func(t *testing.T) {
		policy, _ := newRetryTestPolicy(t, 0)

		if got := policy.backoff(waitMin, waitMax, 0, retryTestResponse(503, "600")); got != waitMax {
			t.Errorf("backoff() = %v, want %v", got, waitMax)
		}
	})

	t.Run("limiter cooldown beyond max is clamped", func(t *testing.T) {
		policy, _ := newRetryTestPolicy(t, 0)
		policy.store.GetLimiter(policy.baseURL, policy.apiKey, ratelimit.ScopeUser).SetCooldown(20 * time.Minute)

		if got := policy.backoff(waitMin, waitMax, 0, retryTestResponse(429, "")); got != waitMax {
			t.Errorf("backoff() = %v, want %v (cooldown clamped)", got, waitMax)
		}
	})

	t.Run("short retry-after passes through unannounced", func(t *testing.T) {
		policy, notices := newRetryTestPolicy(t, 0)

		if got := policy.backoff(waitMin, waitMax, 0, retryTestResponse(429, "2")); got != 2*time.Second {
			t.Errorf("backoff() = %v, want 2s", got)
		}
		if msgs := notices(); len(msgs) != 0 {
			t.Errorf("a 2s wait should not notify, got %v", msgs)
		}
	})

	t.Run("exponential growth stops at max", func(t *testing.T) {
		policy, notices := newRetryTestPolicy(t, 0)

		// 2^5 * 1s = 32s, over the 30s ceiling.
		if got := policy.backoff(waitMin, waitMax, 5, retryTestResponse(500, "")); got != waitMax {
			t.Errorf("backoff() = %v, want %v", got, waitMax)
		}
		msgs := notices()
		if len(msgs) != 1 {
			t.Fatalf("expected one notice, got %d: %v", len(msgs), msgs)
		}
		if strings.Contains(msgs[0], "server asked") {
			t.Errorf("notice should not blame the server for our own backoff: %q", msgs[0])
		}
	})

	t.Run("first 500 retry is quiet", func(t *testing.T) {
		policy, notices := newRetryTestPolicy(t, 0)

		if got := policy.backoff(waitMin, waitMax, 0, retryTestResponse(500, "")); got != waitMin {
			t.Errorf("backoff() = %v, want %v", got, waitMin)
		}
		if msgs := notices(); len(msgs) != 0 {
			t.Errorf("a 1s wait should not notify, got %v", msgs)
		}
	})
}

// TestRetryBudgetStopsRetries verifies the wall-clock cap: once a call has spent
// its budget, checkRetry stops the loop and says what it was failing on.
func TestRetryBudgetStopsRetries(t *testing.T) {
	const budget = 40 * time.Millisecond
	policy, _ := newRetryTestPolicy(t, budget)
	resp := retryTestResponse(500, "")

	ctx := withRetryBudget(context.Background())
	retry, err := policy.checkRetry(ctx, resp, nil)
	if !retry || err != nil {
		t.Fatalf("checkRetry() inside budget = (%v, %v), want (true, nil)", retry, err)
	}

	time.Sleep(budget + 20*time.Millisecond)

	retry, err = policy.checkRetry(ctx, resp, nil)
	if retry {
		t.Error("checkRetry() should stop retrying once the budget is spent")
	}
	if err == nil {
		t.Fatal("checkRetry() should explain why it gave up")
	}
	for _, want := range []string{"retries exhausted after", "HTTP 500"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}

	t.Run("transport error is preserved as the cause", func(t *testing.T) {
		dialErr := errors.New("dial tcp: lookup platform.rescale.com: no such host")
		retry, err := policy.checkRetry(ctx, nil, dialErr)
		if retry {
			t.Error("checkRetry() should stop retrying a transport error past the budget")
		}
		if !errors.Is(err, dialErr) {
			t.Errorf("error %v should wrap the transport error", err)
		}
	})

	t.Run("unstamped context is not capped", func(t *testing.T) {
		retry, err := policy.checkRetry(context.Background(), resp, nil)
		if !retry || err != nil {
			t.Errorf("checkRetry() without a budget stamp = (%v, %v), want (true, nil)", retry, err)
		}
	})
}

// TestRetryEmitsNoticesAndHonorsBudget drives the real go-retryablehttp wiring
// against a server that never recovers: the retries have to be visible and the
// call has to give up well short of all 11 attempts.
func TestRetryEmitsNoticesAndHonorsBudget(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"detail":"backend on fire"}`))
	}))
	defer server.Close()

	policy, notices := newRetryTestPolicy(t, 120*time.Millisecond)
	policy.baseURL = server.URL
	retryClient := newRetryClient(&http.Client{}, policy, apiRetryMax, 20*time.Millisecond, 40*time.Millisecond)

	req, err := http.NewRequestWithContext(withRetryBudget(context.Background()), "GET", server.URL+"/api/v3/jobs/", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}

	resp, err := retryClient.StandardClient().Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("Do() should fail once the retry budget is spent")
	}
	if !strings.Contains(err.Error(), "retries exhausted after") {
		t.Errorf("error %q should name the exhausted retry budget", err.Error())
	}

	attempts := atomic.LoadInt32(&hits)
	if attempts < 2 {
		t.Errorf("server saw %d requests, want at least one retry", attempts)
	}
	if attempts > apiRetryMax {
		t.Errorf("server saw %d requests — the budget should stop the loop short of %d attempts",
			attempts, apiRetryMax+1)
	}

	msgs := notices()
	if len(msgs) == 0 {
		t.Fatal("retries produced no user-visible notice")
	}
	if !strings.Contains(msgs[0], "Retrying") || !strings.Contains(msgs[0], "500") {
		t.Errorf("first notice %q should name the retry and the status", msgs[0])
	}
}

// trackedBody records whether a response body was read and closed.
type trackedBody struct {
	reader io.Reader
	read   int
	closed bool
}

func (b *trackedBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.read += n
	return n, err
}

func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}

// TestRetryErrorHandlerBodyHandling covers both exits from the retry loop: a
// cancelled call must not leak the response net/http is about to discard, and
// retry exhaustion must hand the body to the caller so it can report the
// server's own error text.
func TestRetryErrorHandlerBodyHandling(t *testing.T) {
	policy, _ := newRetryTestPolicy(t, 0)

	t.Run("error path drains and closes", func(t *testing.T) {
		body := &trackedBody{reader: strings.NewReader("server said no")}
		got, err := policy.errorHandler(&http.Response{StatusCode: 500, Body: body}, context.Canceled, 3)

		if got != nil {
			t.Error("errorHandler() must not hand back a response alongside an error — net/http drops it unclosed")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("errorHandler() error = %v, want context.Canceled", err)
		}
		if !body.closed {
			t.Error("response body was not closed")
		}
		if body.read == 0 {
			t.Error("response body was not drained, so the connection cannot be reused")
		}
	})

	t.Run("exhaustion path keeps the response readable", func(t *testing.T) {
		body := &trackedBody{reader: strings.NewReader(`{"detail":"still broken"}`)}
		resp := &http.Response{StatusCode: 500, Body: body}

		got, err := policy.errorHandler(resp, nil, 11)
		if got != resp || err != nil {
			t.Fatalf("errorHandler() = (%v, %v), want the response and nil", got, err)
		}
		if body.closed {
			t.Error("body must stay open for the caller to read the server's error")
		}
	})
}

// TestPaginationFollowsNextWithMixedCaseBaseURL pins the fix for a configured
// platform URL whose host case differs from the one the API echoes back.
// Platform URLs are validated case-insensitively, so "LOCALHOST" is accepted;
// trimming it as a literal prefix left the whole next URL in place, which was
// then appended to the base and failed DNS on every retry.
func TestPaginationFollowsNextWithMixedCaseBaseURL(t *testing.T) {
	tests := []struct {
		name string
		path string
		// page1 returns the first page, with next pointing at page 2 using the
		// server's own (canonical-case) URL.
		page1 func(nextURL string) map[string]interface{}
		page2 map[string]interface{}
		call  func(t *testing.T, c *Client) error
	}{
		{
			name: "ListJobs",
			path: "/api/v3/jobs/",
			page1: func(nextURL string) map[string]interface{} {
				return map[string]interface{}{
					"count":   2,
					"next":    nextURL,
					"results": []map[string]interface{}{{"id": "job-1"}},
				}
			},
			page2: map[string]interface{}{
				"count":   2,
				"next":    nil,
				"results": []map[string]interface{}{{"id": "job-2"}},
			},
			call: func(t *testing.T, c *Client) error {
				jobs, err := c.ListJobs(context.Background())
				if err == nil && len(jobs) != 2 {
					t.Errorf("ListJobs() returned %d jobs, want 2 (both pages)", len(jobs))
				}
				return err
			},
		},
		{
			name: "paginateRaw",
			path: "/api/v2/analyses/",
			page1: func(nextURL string) map[string]interface{} {
				return map[string]interface{}{
					"count":   2,
					"next":    nextURL,
					"results": []map[string]interface{}{{"code": "analysis-1"}},
				}
			},
			page2: map[string]interface{}{
				"count":   2,
				"next":    nil,
				"results": []map[string]interface{}{{"code": "analysis-2"}},
			},
			call: func(t *testing.T, c *Client) error {
				raw, err := c.GetAnalysesRaw(context.Background())
				if err == nil && len(raw) != 2 {
					t.Errorf("GetAnalysesRaw() returned %d results, want 2 (both pages)", len(raw))
				}
				return err
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var server *httptest.Server
			var mu sync.Mutex
			var seen []string

			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				seen = append(seen, r.URL.RequestURI())
				mu.Unlock()

				if r.URL.Path != tc.path {
					http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Query().Get("page") == "2" {
					_ = json.NewEncoder(w).Encode(tc.page2)
					return
				}
				_ = json.NewEncoder(w).Encode(tc.page1(server.URL + tc.path + "?page=2"))
			}))
			defer server.Close()

			// Same host, different case — exactly what a mixed-case --api-url
			// produces after validation accepts it.
			mixedCase := strings.Replace(server.URL, "127.0.0.1", "LOCALHOST", 1)
			if mixedCase == server.URL {
				t.Skipf("test server URL %q is not host-based", server.URL)
			}

			client := newTestClient(t, mixedCase)
			if err := tc.call(t, client); err != nil {
				t.Fatalf("pagination with base URL %q failed: %v", mixedCase, err)
			}

			mu.Lock()
			defer mu.Unlock()
			if len(seen) != 2 {
				t.Fatalf("server saw %d requests, want 2 (page 1 then page 2): %v", len(seen), seen)
			}
			if !strings.Contains(seen[1], "page=2") {
				t.Errorf("second request was %q, want the page 2 path", seen[1])
			}
		})
	}
}

// TestMetricsPathTrackingIsBounded verifies the usage map cannot grow without
// limit: keys drop the query string, and new paths stop being admitted at the
// cap.
func TestMetricsPathTrackingIsBounded(t *testing.T) {
	m := &apiMetrics{callsByPath: make(map[string]int64)}

	m.Lock()
	for i := 0; i < 5; i++ {
		m.trackPath("/api/v3/files/?page=" + strconv.Itoa(i) + "&page_size=100")
	}
	m.Unlock()

	if len(m.callsByPath) != 1 {
		t.Fatalf("paginated crawl produced %d keys, want 1: %v", len(m.callsByPath), m.callsByPath)
	}
	if got := m.callsByPath["/api/v3/files/"]; got != 5 {
		t.Errorf("callsByPath[/api/v3/files/] = %d, want 5", got)
	}

	m.Lock()
	for i := 0; i < maxTrackedPaths+500; i++ {
		m.trackPath("/api/v3/folders/" + strconv.Itoa(i) + "/contents/")
	}
	// Known paths keep counting even at the cap.
	m.trackPath("/api/v3/files/")
	m.Unlock()

	if len(m.callsByPath) > maxTrackedPaths {
		t.Errorf("callsByPath grew to %d keys, want at most %d", len(m.callsByPath), maxTrackedPaths)
	}
	if got := m.callsByPath["/api/v3/files/"]; got != 6 {
		t.Errorf("callsByPath[/api/v3/files/] = %d, want 6 — known paths must keep counting at the cap", got)
	}
}

// TestFileTagErrorsCarryServerText verifies the tag calls read the body before
// closing it, so the server's explanation survives into the error.
func TestFileTagErrorsCarryServerText(t *testing.T) {
	const serverText = `{"detail":"tag name is reserved"}`

	tests := []struct {
		name    string
		call    func(c *Client) error
		wantMsg string
	}{
		{
			name:    "AddFileTags",
			call:    func(c *Client) error { return c.AddFileTags(context.Background(), "file-1", []string{"bad tag"}) },
			wantMsg: `failed to add tag "bad tag"`,
		},
		{
			name:    "RemoveFileTags",
			call:    func(c *Client) error { return c.RemoveFileTags(context.Background(), "file-1", []string{"bad tag"}) },
			wantMsg: `failed to remove tag "bad tag"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(serverText))
			}))
			defer server.Close()

			err := tc.call(newTestClient(t, server.URL))
			if err == nil {
				t.Fatal("expected an error for a 400 response")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q missing %q", err.Error(), tc.wantMsg)
			}
			if !strings.Contains(err.Error(), "tag name is reserved") {
				t.Errorf("error %q dropped the server's explanation", err.Error())
			}
		})
	}
}
