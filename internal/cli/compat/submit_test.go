package compat

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCompatStageSubmitFiles(t *testing.T) {
	// Create a fake script file
	scriptDir := t.TempDir()
	scriptPath := filepath.Join(scriptDir, "test.sge")
	scriptContent := "#!/bin/bash\necho hello\n"
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create fake input files
	inputPath1 := filepath.Join(scriptDir, "data.txt")
	inputPath2 := filepath.Join(scriptDir, "config.json")
	if err := os.WriteFile(inputPath1, []byte("test data"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inputPath2, []byte(`{"key":"value"}`), 0644); err != nil {
		t.Fatal(err)
	}

	tmpDir, cleanup, err := compatStageSubmitFiles(scriptPath, []string{inputPath1, inputPath2})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	// Verify run.sh exists and has script content
	runShData, err := os.ReadFile(filepath.Join(tmpDir, "run.sh"))
	if err != nil {
		t.Fatalf("run.sh not found: %v", err)
	}
	if string(runShData) != scriptContent {
		t.Errorf("run.sh content = %q, want %q", string(runShData), scriptContent)
	}

	// Verify input.zip exists and is a valid zip
	zipPath := filepath.Join(tmpDir, "input.zip")
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("input.zip is not a valid zip: %v", err)
	}
	defer zr.Close()

	// Verify zip contains exactly the input files with flat names
	fileNames := make(map[string]bool)
	for _, f := range zr.File {
		fileNames[f.Name] = true
	}

	if !fileNames["data.txt"] {
		t.Error("input.zip missing data.txt")
	}
	if !fileNames["config.json"] {
		t.Error("input.zip missing config.json")
	}
	if len(zr.File) != 2 {
		t.Errorf("input.zip has %d files, want 2", len(zr.File))
	}
}

func TestCompatStageSubmitFiles_NoInputFiles(t *testing.T) {
	scriptDir := t.TempDir()
	scriptPath := filepath.Join(scriptDir, "test.sge")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\necho hello\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tmpDir, cleanup, err := compatStageSubmitFiles(scriptPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	// Verify empty zip is created (rescale-cli creates input.zip even with no extra files)
	zipPath := filepath.Join(tmpDir, "input.zip")
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("input.zip is not a valid zip: %v", err)
	}
	defer zr.Close()

	if len(zr.File) != 0 {
		t.Errorf("empty input.zip has %d files, want 0", len(zr.File))
	}
}

func TestTransformSubmitJSON(t *testing.T) {
	// Simulate a v3 API job response with key fields
	v3Response := map[string]interface{}{
		// Shared keys (should survive)
		"id":                 "testID",
		"name":               "TestJob",
		"dateInserted":       "2026-04-09T00:00:00Z",
		"isLowPriority":     false,
		"billingPriorityValue": "INSTANT",
		"sshPort":            22,
		"archiveFilters":     []interface{}{},
		"resourceFilters":    []interface{}{},
		"cidrRule":           "1.2.3.4/32",
		"publicKey":          "ssh-rsa AAAA",
		"expectedRuns":       nil,
		"isTemplateDryRun":   false,
		"includeNominalRun":  false,
		"monteCarloIterations": nil,
		"paramFile":          nil,
		"caseFile":           nil,

		// v3-only keys (should be removed)
		"owner":                    "test@example.com",
		"sharedWith":               []interface{}{},
		"launchConfig":             []interface{}{},
		"currentUserHasFullAccess": true,
		"folderId":                 "abc",
		"osCostTier":               "linux-free",
		"description":              "",

		// jobanalyses with nested structure
		"jobanalyses": []interface{}{
			map[string]interface{}{
				"command":            "./run.sh",
				"useRescaleLicense":  false,
				"envVars":            map[string]interface{}{},
				"inputFiles":         []interface{}{},
				"inputFolders":       []interface{}{},
				"templateTasks":      []interface{}{},
				"preProcessScript":   nil,
				"preProcessScriptCommand":  "",
				"postProcessScript":  nil,
				"postProcessScriptCommand": "",

				// v3-only JA keys (should be removed)
				"analysis": map[string]interface{}{
					"code":    "user_included",
					"type":    "compute",
					"version": "0",
					"id":      "ver123",
				},
				"flags":                      map[string]interface{}{"igCv": true},
				"onDemandLicenseSeller":       nil,
				"userDefinedLicenseSettings":  nil,

				// hardware with nested coreType
				"hardware": map[string]interface{}{
					"coreType": map[string]interface{}{
						"code": "emerald",
						"name": "Emerald",
						"io":   35.0,
					},
					"coresPerSlot": 1,
					"walltime":     48,
				},
			},
		},
	}

	raw, err := json.Marshal(v3Response)
	if err != nil {
		t.Fatal(err)
	}

	result, err := transformSubmitJSON(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(result, &m); err != nil {
		t.Fatal(err)
	}

	// Check v3-only top-level keys were removed
	for _, key := range []string{"owner", "sharedWith", "launchConfig", "currentUserHasFullAccess", "folderId", "osCostTier", "description"} {
		if _, ok := m[key]; ok {
			t.Errorf("v3-only key %q should have been removed", key)
		}
	}

	// Check CLI-only top-level keys were added
	if m["apiKey"] != nil {
		t.Errorf("apiKey = %v, want nil", m["apiKey"])
	}
	if m["autoTerminateCluster"] != true {
		t.Errorf("autoTerminateCluster = %v, want true", m["autoTerminateCluster"])
	}
	if m["ownerId"] != nil {
		t.Errorf("ownerId = %v, want nil", m["ownerId"])
	}
	if m["isInteractive"] != false {
		t.Errorf("isInteractive = %v, want false", m["isInteractive"])
	}
	if m["isLargeDoe"] != false {
		t.Errorf("isLargeDoe = %v, want false", m["isLargeDoe"])
	}

	// Check shared keys survived
	if m["id"] != "testID" {
		t.Errorf("id = %v, want testID", m["id"])
	}
	if m["name"] != "TestJob" {
		t.Errorf("name = %v, want TestJob", m["name"])
	}

	// Check jobanalyses flattening
	jaSlice, ok := m["jobanalyses"].([]interface{})
	if !ok || len(jaSlice) != 1 {
		t.Fatalf("jobanalyses unexpected: %v", m["jobanalyses"])
	}
	ja := jaSlice[0].(map[string]interface{})

	// analysis object should be removed, flat fields added
	if _, ok := ja["analysis"]; ok {
		t.Error("nested 'analysis' object should have been removed")
	}
	if _, ok := ja["flags"]; ok {
		t.Error("'flags' should have been removed")
	}
	if ja["analysisCode"] != "user_included" {
		t.Errorf("analysisCode = %v, want user_included", ja["analysisCode"])
	}
	if ja["analysisType"] != "compute" {
		t.Errorf("analysisType = %v, want compute", ja["analysisType"])
	}
	if ja["analysisVersionId"] != "ver123" {
		t.Errorf("analysisVersionId = %v, want ver123", ja["analysisVersionId"])
	}

	// Check CLI-only JA defaults
	if ja["isCustomDoe"] != false {
		t.Errorf("isCustomDoe = %v, want false", ja["isCustomDoe"])
	}
	if ja["order"] != float64(0) { // JSON numbers are float64
		t.Errorf("order = %v, want 0", ja["order"])
	}
	if ja["shouldRunForever"] != false {
		t.Errorf("shouldRunForever = %v, want false", ja["shouldRunForever"])
	}
	if ja["useSharedStorage"] != false {
		t.Errorf("useSharedStorage = %v, want false", ja["useSharedStorage"])
	}

	// Check hardware.coreType was flattened to string
	hw := ja["hardware"].(map[string]interface{})
	if hw["coreType"] != "emerald" {
		t.Errorf("hardware.coreType = %v, want \"emerald\"", hw["coreType"])
	}
}

func TestTransformSubmitJSON_InputFileFlattening(t *testing.T) {
	v3Response := map[string]interface{}{
		"id":   "testID",
		"name": "TestJob",
		"jobanalyses": []interface{}{
			map[string]interface{}{
				"command": "./run.sh",
				"analysis": map[string]interface{}{
					"code": "test",
					"type": "compute",
				},
				"hardware": map[string]interface{}{
					"coreType": map[string]interface{}{"code": "emerald"},
				},
				"inputFiles": []interface{}{
					map[string]interface{}{
						"id":                   "file1",
						"name":                 "input.zip",
						"decompress":           true,
						"decryptedSize":        float64(1234),
						"isUploaded":           true,
						"typeId":               float64(1),
						"encodedEncryptionKey": "abc123",
						"pathParts":            map[string]interface{}{"path": "user/test", "container": "bucket"},
						"storage":              map[string]interface{}{"storageType": "S3Storage"},
						"fileChecksums":        []interface{}{},
						// v3-only fields that should be removed
						"dateUploaded":  "2026-04-09",
						"downloadUrl":   "https://example.com",
						"isDeleted":     false,
						"owner":         "test@example.com",
						"path":          "user/test/file",
						"relativePath":  "file",
						"userTags":      []interface{}{},
						"viewInBrowser": true,
					},
				},
			},
		},
	}

	raw, _ := json.Marshal(v3Response)
	result, err := transformSubmitJSON(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]interface{}
	json.Unmarshal(result, &m)
	ja := m["jobanalyses"].([]interface{})[0].(map[string]interface{})
	inputFiles := ja["inputFiles"].([]interface{})
	if len(inputFiles) != 1 {
		t.Fatalf("expected 1 input file, got %d", len(inputFiles))
	}

	f := inputFiles[0].(map[string]interface{})

	// Check v3-only fields removed
	for _, key := range []string{"dateUploaded", "downloadUrl", "isDeleted", "owner", "path", "relativePath", "userTags", "viewInBrowser"} {
		if _, ok := f[key]; ok {
			t.Errorf("v3-only inputFile key %q should have been removed", key)
		}
	}

	// Check CLI-only field added
	if f["inputFileType"] != "REMOTE" {
		t.Errorf("inputFileType = %v, want REMOTE", f["inputFileType"])
	}

	// Check preserved fields
	if f["id"] != "file1" {
		t.Errorf("id = %v, want file1", f["id"])
	}
	if f["name"] != "input.zip" {
		t.Errorf("name = %v, want input.zip", f["name"])
	}
	if f["decompress"] != true {
		t.Errorf("decompress = %v, want true", f["decompress"])
	}
}
