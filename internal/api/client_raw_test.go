package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetCoreTypesRaw_PreservesAllFields(t *testing.T) {
	fixture := `{
		"count": 1,
		"next": null,
		"results": [{
			"hasSsd": true,
			"code": "adamite",
			"compute": null,
			"name": "Adamite",
			"isDeprecated": false,
			"price": 0.1751,
			"remoteVizAllowed": true,
			"storage": 37,
			"lowPriorityPrice": "0.0988",
			"walltimeRequired": false,
			"displayOrder": 33,
			"io": "50.0",
			"memory": 8000,
			"cores": [24],
			"isPrimary": false,
			"processorInfo": "Intel Xeon Platinum 8259CL CPU",
			"storageIo": "Standard",
			"description": ""
		}]
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fixture))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	results, err := client.GetCoreTypesRaw(context.Background(), false)
	if err != nil {
		t.Fatalf("GetCoreTypesRaw() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}

	// Verify all 18 fields survive passthrough
	var m map[string]interface{}
	if err := json.Unmarshal(results[0], &m); err != nil {
		t.Fatalf("failed to unmarshal raw result: %v", err)
	}

	expectedFields := []string{
		"hasSsd", "code", "compute", "name", "isDeprecated", "price",
		"remoteVizAllowed", "storage", "lowPriorityPrice", "walltimeRequired",
		"displayOrder", "io", "memory", "cores", "isPrimary",
		"processorInfo", "storageIo", "description",
	}
	for _, field := range expectedFields {
		if _, ok := m[field]; !ok {
			t.Errorf("field %q missing from raw result", field)
		}
	}
}

func TestGetAnalysesRaw_PreservesAllFields(t *testing.T) {
	fixture := `{
		"count": 1,
		"next": null,
		"results": [{
			"code": "3dx_station",
			"description": "<p>test</p>",
			"versions": [{"version": "2022x", "versionCode": "2022x-Batch"}],
			"industries": [{"name": "Automotive", "icon": "https://example.com/icon.png"}],
			"supportDesks": [{"code": "rescale", "displayName": "Rescale Support"}],
			"hasRescaleLicense": false,
			"vendorName": "Dassault Systemes",
			"pricing": null,
			"hasShortTermLicense": false,
			"licenseSettings": [],
			"optimizerType": 0,
			"resources": []
		}]
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fixture))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	results, err := client.GetAnalysesRaw(context.Background())
	if err != nil {
		t.Fatalf("GetAnalysesRaw() error = %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(results[0], &m); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	for _, field := range []string{"supportDesks", "hasRescaleLicense", "optimizerType", "resources"} {
		if _, ok := m[field]; !ok {
			t.Errorf("field %q missing from raw analysis result", field)
		}
	}
}

func TestPaginateRaw_MultiPage(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			nextURL := fmt.Sprintf("http://%s/api/v3/coretypes/?page=2", r.Host)
			fmt.Fprintf(w, `{"count":3,"next":"%s","results":[{"code":"a"},{"code":"b"}]}`, nextURL)
		} else {
			w.Write([]byte(`{"count":3,"next":null,"results":[{"code":"c"}]}`))
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	results, err := client.paginateRaw(context.Background(), "/api/v3/coretypes/")
	if err != nil {
		t.Fatalf("paginateRaw() error = %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	if callCount != 2 {
		t.Errorf("expected 2 API calls, got %d", callCount)
	}
}

func TestGetJobStatusesRaw_PreservesExtraFields(t *testing.T) {
	fixture := `{
		"count": 1,
		"next": null,
		"results": [{
			"status": "Completed",
			"statusReason": "Completed successfully",
			"statusDate": "2026-04-03T03:45:58.683875+00:00",
			"notify": false,
			"preventDuplicates": false
		}]
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fixture))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	results, err := client.GetJobStatusesRaw(context.Background(), "test123")
	if err != nil {
		t.Fatalf("GetJobStatusesRaw() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}

	var m map[string]interface{}
	if err := json.Unmarshal(results[0], &m); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// These fields are missing from models.JobStatusEntry but must survive raw passthrough
	for _, field := range []string{"notify", "preventDuplicates", "status", "statusDate", "statusReason"} {
		if _, ok := m[field]; !ok {
			t.Errorf("field %q missing from raw status result", field)
		}
	}
}

func TestGetJobConnectionDetailsRaw(t *testing.T) {
	fixture := `{"connectionDetails":[{"host":"10.0.0.1","port":22}]}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fixture))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	raw, err := client.GetJobConnectionDetailsRaw(context.Background(), "test123")
	if err != nil {
		t.Fatalf("GetJobConnectionDetailsRaw() error = %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if _, ok := m["connectionDetails"]; !ok {
		t.Error("connectionDetails field missing")
	}
}

func TestGetFileInfoRaw(t *testing.T) {
	fixture := `{
		"id": "abc123",
		"name": "test.txt",
		"typeId": 1,
		"isUploaded": true,
		"decryptedSize": 1024,
		"encodedEncryptionKey": "key==",
		"pathParts": {"container": "bucket", "path": "user/file.txt"},
		"storage": {"id": "s1", "storageType": "S3Storage", "encryptionType": "default"},
		"fileChecksums": [{"hashFunction": "sha512", "fileHash": "abc"}]
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fixture))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	raw, err := client.GetFileInfoRaw(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("GetFileInfoRaw() error = %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	for _, field := range []string{"id", "name", "typeId", "isUploaded", "pathParts", "storage", "fileChecksums"} {
		if _, ok := m[field]; !ok {
			t.Errorf("field %q missing from raw file info", field)
		}
	}
}

func TestGetJobRaw_PreservesAllFields(t *testing.T) {
	fixture := `{
		"id": "abcDe",
		"name": "Test Job",
		"dateInserted": "2026-04-03T03:45:58.683875+00:00",
		"jobanalyses": [{
			"command": "./run.sh",
			"analysis": {"code": "openfoam", "version": "10.0"},
			"hardware": {"coreType": {"code": "emerald"}, "coresPerSlot": 16},
			"inputFiles": [{"id": "file1", "name": "input.zip"}]
		}],
		"isLowPriority": false,
		"owner": "user@example.com"
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fixture))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	raw, err := client.GetJobRaw(context.Background(), "abcDe")
	if err != nil {
		t.Fatalf("GetJobRaw() error = %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	for _, field := range []string{"id", "name", "dateInserted", "jobanalyses", "isLowPriority", "owner"} {
		if _, ok := m[field]; !ok {
			t.Errorf("field %q missing from raw job result", field)
		}
	}
}

func TestGetJobRaw_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"detail":"Not found."}`))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	_, err := client.GetJobRaw(context.Background(), "bad-id")
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}

func TestGetConnectionDetailsRaw_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"detail":"Not found."}`))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	_, err := client.GetJobConnectionDetailsRaw(context.Background(), "bad-id")
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}
