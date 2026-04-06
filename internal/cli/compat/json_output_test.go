package compat

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/models"
)

func TestFormatRescaleTime_UsesOffsetNotZ(t *testing.T) {
	ts := time.Date(2026, 4, 5, 22, 23, 22, 840000000, time.UTC)
	got := formatRescaleTime(ts)
	want := "2026-04-05T22:23:22.840+00:00"
	if got != want {
		t.Errorf("formatRescaleTime() = %q, want %q", got, want)
	}
}

func TestFormatRescaleTime_MillisecondPrecision(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got := formatRescaleTime(ts)
	if !strings.Contains(got, ".000+00:00") {
		t.Errorf("expected millisecond precision, got %q", got)
	}
}

func TestFormatRescaleTime_ConvertsToUTC(t *testing.T) {
	est := time.FixedZone("EST", -5*3600)
	ts := time.Date(2026, 4, 5, 17, 23, 22, 840000000, est)
	got := formatRescaleTime(ts)
	want := "2026-04-05T22:23:22.840+00:00"
	if got != want {
		t.Errorf("formatRescaleTime() = %q, want %q", got, want)
	}
}

func TestToCompatFileEntry_FixtureFields(t *testing.T) {
	cf := &models.CloudFile{
		ID:                   "qpOdrb",
		Name:                 "test.txt",
		TypeID:               1,
		IsUploaded:           true,
		Owner:                "should-be-excluded",
		Path:                 "should-be-excluded",
		EncodedEncryptionKey: "key==",
		IV:                   "should-be-excluded",
		PathParts:            &models.CloudFilePathParts{Container: "bucket", Path: "user/file.txt"},
		Storage: &models.CloudFileStorage{
			ID:             "pCTMk",
			StorageType:    "S3Storage",
			EncryptionType: "default",
		},
		DecryptedSize: 32,
		FileChecksums: []models.FileChecksum{{HashFunction: "sha512", FileHash: "abc"}},
	}

	entry := toCompatFileEntry(cf)
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	// Exactly the fixture fields, no extras
	wantFields := []string{"name", "pathParts", "storage", "encodedEncryptionKey", "isUploaded", "decryptedSize", "typeId", "fileChecksums", "id"}
	if len(m) != len(wantFields) {
		t.Errorf("got %d fields, want %d", len(m), len(wantFields))
	}
	for _, f := range wantFields {
		if _, ok := m[f]; !ok {
			t.Errorf("missing field %q", f)
		}
	}

	// Excluded fields must not appear
	for _, f := range []string{"owner", "path", "iv", "userTags"} {
		if _, ok := m[f]; ok {
			t.Errorf("unexpected field %q should not be in compat output", f)
		}
	}
}

func TestToCompatFileEntry_NilChecksums(t *testing.T) {
	cf := &models.CloudFile{
		ID:   "test",
		Name: "test.txt",
	}
	entry := toCompatFileEntry(cf)
	data, _ := json.Marshal(entry)
	// fileChecksums should be [] not null
	if strings.Contains(string(data), `"fileChecksums":null`) {
		t.Error("fileChecksums should be empty array, not null")
	}
}

func TestWriteTransferEnvelope(t *testing.T) {
	start := time.Date(2026, 4, 5, 22, 23, 22, 840000000, time.UTC)
	end := time.Date(2026, 4, 5, 22, 23, 25, 163000000, time.UTC)

	files := []compatFileEntry{{
		Name:                 "test.txt",
		EncodedEncryptionKey: "key==",
		IsUploaded:           true,
		DecryptedSize:        32,
		TypeID:               1,
		FileChecksums:        []models.FileChecksum{},
		ID:                   "abc",
	}}

	var buf bytes.Buffer
	err := writeTransferEnvelope(&buf, true, start, end, files)
	if err != nil {
		t.Fatalf("writeTransferEnvelope error: %v", err)
	}

	output := buf.String()

	// Single line
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 1 {
		t.Errorf("expected single-line output, got %d lines", len(lines))
	}

	var env map[string]interface{}
	if err := json.Unmarshal([]byte(output), &env); err != nil {
		t.Fatalf("failed to parse output: %v", err)
	}

	if env["success"] != true {
		t.Error("success should be true")
	}
	if env["startTime"] != "2026-04-05T22:23:22.840+00:00" {
		t.Errorf("startTime = %v", env["startTime"])
	}
	if env["endTime"] != "2026-04-05T22:23:25.163+00:00" {
		t.Errorf("endTime = %v", env["endTime"])
	}
	filesArr, ok := env["files"].([]interface{})
	if !ok || len(filesArr) != 1 {
		t.Error("files should be array with 1 entry")
	}
}

func TestWriteTransferEnvelopeRaw(t *testing.T) {
	start := time.Date(2026, 4, 5, 22, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 5, 22, 0, 1, 0, time.UTC)

	raw := []json.RawMessage{json.RawMessage(`{"id":"abc","name":"test.txt"}`)}

	var buf bytes.Buffer
	err := writeTransferEnvelopeRaw(&buf, true, start, end, raw)
	if err != nil {
		t.Fatalf("writeTransferEnvelopeRaw error: %v", err)
	}

	var env map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}

	filesArr, ok := env["files"].([]interface{})
	if !ok || len(filesArr) != 1 {
		t.Error("files should be array with 1 entry")
	}
}

func TestWriteJSON_CompactSingleLine(t *testing.T) {
	var buf bytes.Buffer
	data := map[string]string{"key": "value", "foo": "bar"}
	if err := writeJSON(&buf, data); err != nil {
		t.Fatalf("writeJSON error: %v", err)
	}
	output := buf.String()
	if strings.Contains(output, "\n\n") || strings.Count(output, "\n") != 1 {
		t.Error("expected exactly one trailing newline")
	}
	if strings.Contains(output, "  ") {
		t.Error("output should be compact, not pretty-printed")
	}
}

func TestCompatFileEntry_FieldOrder(t *testing.T) {
	entry := compatFileEntry{
		Name:                 "test.txt",
		PathParts:            &models.CloudFilePathParts{Container: "b", Path: "p"},
		Storage:              &compatFileStorage{StorageType: "S3Storage", ID: "s1", EncryptionType: "default"},
		EncodedEncryptionKey: "key==",
		IsUploaded:           true,
		DecryptedSize:        100,
		TypeID:               1,
		FileChecksums:        []models.FileChecksum{},
		ID:                   "abc",
	}

	data, _ := json.Marshal(entry)
	s := string(data)

	// Verify top-level field order matches fixture.
	// Use unique substrings to avoid matching nested "id" fields.
	nameIdx := strings.Index(s, `"name":"test.txt"`)
	pathIdx := strings.Index(s, `"pathParts"`)
	storIdx := strings.Index(s, `"storage":{`)
	keyIdx := strings.Index(s, `"encodedEncryptionKey"`)
	typeIdx := strings.Index(s, `"typeId"`)
	// Top-level "id" is the last field
	idIdx := strings.LastIndex(s, `"id":"abc"`)

	if nameIdx > pathIdx || pathIdx > storIdx || storIdx > keyIdx || keyIdx > typeIdx || typeIdx > idIdx {
		t.Errorf("field order doesn't match fixture; got: %s", s)
	}
}
