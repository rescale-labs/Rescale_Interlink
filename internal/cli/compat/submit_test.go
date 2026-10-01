package compat

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/pur/parser/testsupport"
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
		"id":                   "testID",
		"name":                 "TestJob",
		"dateInserted":         "2026-04-09T00:00:00Z",
		"isLowPriority":        false,
		"billingPriorityValue": "INSTANT",
		"sshPort":              22,
		"archiveFilters":       []interface{}{},
		"resourceFilters":      []interface{}{},
		"cidrRule":             "192.0.2.4/32",
		"publicKey":            "ssh-rsa AAAA",
		"expectedRuns":         nil,
		"isTemplateDryRun":     false,
		"includeNominalRun":    false,
		"monteCarloIterations": nil,
		"paramFile":            nil,
		"caseFile":             nil,

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
				"command":                  "./run.sh",
				"useRescaleLicense":        false,
				"envVars":                  map[string]interface{}{},
				"inputFiles":               []interface{}{},
				"inputFolders":             []interface{}{},
				"templateTasks":            []interface{}{},
				"preProcessScript":         nil,
				"preProcessScriptCommand":  "",
				"postProcessScript":        nil,
				"postProcessScriptCommand": "",

				// v3-only JA keys (should be removed)
				"analysis": map[string]interface{}{
					"code":    "user_included",
					"type":    "compute",
					"version": "0",
					"id":      "ver123",
				},
				"flags":                      map[string]interface{}{"igCv": true},
				"onDemandLicenseSeller":      nil,
				"userDefinedLicenseSettings": nil,

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

// TestCompatE2EDownload_AppliesFilters covers submit -E's download filters,
// which were computed and then dropped, so every output file downloaded. The
// kept file is already on disk at its full size and is skipped; the other one's
// storage is no real backend, so a download of it fails at provider creation
// without touching the network.
func TestCompatE2EDownload_AppliesFilters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/jobs/JOB1/files/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[`+
			`{"id":"f1","name":"results.dat","decryptedSize":4,"storage":{"storageType":"NotAStorageBackend"}},`+
			`{"id":"f2","name":"debug.log","decryptedSize":4,"storage":{"storageType":"NotAStorageBackend"}}]}`)
	}))
	t.Cleanup(server.Close)
	client := api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})

	for _, tc := range []struct {
		flag                    string
		matchers                []string
		excludeTerm, searchTerm string
	}{
		{"-f '*.dat'", []string{"*.dat"}, "", ""},
		{"--exclude log", nil, "log", ""},
		{"-s results", nil, "", "results"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if err := os.WriteFile("results.dat", []byte("done"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := compatE2EDownload(context.Background(), "JOB1", tc.matchers, tc.excludeTerm, tc.searchTerm,
				client, &CompatContext{Quiet: true}); err != nil {
				t.Errorf("submit -E %s: %v, want debug.log filtered out", tc.flag, err)
			}
		})
	}
}

// fakeSubmitAPI answers what submit asks the API for, with the user's projects
// and the core types for a script that names them, and keeps each job
// create's body. The upload of submit's two staged files is stood in for and
// counted: they go to the storage the platform names.
type fakeSubmitAPI struct {
	client  *api.Client
	mu      sync.Mutex
	creates [][]byte
	uploads int
}

func newFakeSubmitAPI(t *testing.T) *fakeSubmitAPI {
	t.Helper()
	f := &fakeSubmitAPI{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v3/jobs/":
			f.mu.Lock()
			f.creates = append(f.creates, body)
			f.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"JOB1"}`)
		case "POST /api/v2/jobs/JOB1/submit/":
		case "GET /api/v2/users/me/projects/":
			fmt.Fprint(w, `{"results":[{"id":"PROJ0","name":"Other"},{"id":"PROJ1","name":"CFD Program"}]}`)
		case "GET /api/v3/coretypes/":
			fmt.Fprint(w, `{"results":[{"code":"emerald","name":"Emerald","cores":[1,2,4,8]}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	f.client = api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})

	orig := compatSubmitUploadFn
	t.Cleanup(func() { compatSubmitUploadFn = orig })
	compatSubmitUploadFn = func(context.Context, []string, string, *api.Client, *CompatContext) ([]string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.uploads++
		return []string{"RUNSH", "INPUTZIP"}, nil
	}
	return f
}

// submit runs submit on a script holding content, and returns the uploads made,
// the bodies of the jobs created and submit's error.
func (f *fakeSubmitAPI) submit(t *testing.T, content string) (int, [][]byte, error) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "job.sge")
	if err := os.WriteFile(script, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := newSubmitCmd()
	cmd.SetContext(context.Background())
	SetCompatContext(cmd, &CompatContext{Quiet: true, apiClient: f.client})
	err := cmd.RunE(cmd, []string{script})
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.uploads, append([][]byte(nil), f.creates...), err
}

// TestCompatSubmitLicenseDirective: rescale-cli writes the license directive
// with "=" and Interlink with a space; either way the create request carries
// the userDefinedLicenseSettings object, or null when there is none, as for
// rescale-cli's "=" with nothing after it. One that cannot be sent as written
// is refused, naming its line, before anything is uploaded or created.
func TestCompatSubmitLicenseDirective(t *testing.T) {
	const settings = `{"featureSets":[{"name":"USER_SPECIFIED_0","features":[{"name":"ansys_hpc","count":8}]}]}`
	for _, tt := range []struct{ directive, want string }{
		{"#RESCALE_USER_DEFINED_LICENSE_SETTINGS=" + settings, settings},
		{"#RESCALE_USER_DEFINED_LICENSE_SETTINGS " + settings, settings},
		{"", "null"},
		{"#RESCALE_USER_DEFINED_LICENSE_SETTINGS=", "null"},
		{"#RESCALE_USER_DEFINED_LICENSE_SETTINGS ", ""},
		{`#RESCALE_USER_DEFINED_LICENSE_SETTINGS={"featureSets":[{"name":"USER_SPECIFIED_0",` +
			`"features":[{"name":"ansys_hpc"}]}]}`, ""},
	} {
		uploads, sent, err := newFakeSubmitAPI(t).submit(t, "#!/bin/bash\n"+tt.directive+"\n./solve.sh\n")
		if tt.want == "" {
			if err == nil || !strings.Contains(err.Error(), "at line 2") || uploads != 0 || len(sent) != 0 {
				t.Errorf("%q: error %v after %d upload(s) and %d create(s), want a refusal naming line 2 before either",
					tt.directive, err, uploads, len(sent))
			}
			continue
		}
		if err != nil || len(sent) != 1 {
			t.Fatalf("%q: submit: %v, %d create(s)", tt.directive, err, len(sent))
		}
		var req struct {
			JobAnalyses []map[string]json.RawMessage `json:"jobanalyses"`
		}
		if err := json.Unmarshal(sent[0], &req); err != nil || len(req.JobAnalyses) != 1 {
			t.Fatalf("%q: create body: %v", tt.directive, err)
		}
		if got := string(req.JobAnalyses[0]["userDefinedLicenseSettings"]); got != tt.want {
			t.Errorf("%q: userDefinedLicenseSettings = %s\nwant %s", tt.directive, got, tt.want)
		}
	}
}

// TestCompatSubmitSendsWhatRescaleCLIWould: submit takes a script written for
// rescale-cli and creates the job rescale-cli would, checked field by field:
// what each directive sets, the project and core type looked up by name, and
// the existing file ahead of the two staged files, with ./run.sh as the
// command, as rescale-cli sends them.
func TestCompatSubmitSendsWhatRescaleCLIWould(t *testing.T) {
	_, sent, err := newFakeSubmitAPI(t).submit(t, testsupport.RescaleCLIScript)
	if err != nil || len(sent) != 1 {
		t.Fatalf("submit: %v, %d create(s)", err, len(sent))
	}
	testsupport.CheckRequest(t, sent[0], testsupport.RescaleCLIRequest("./run.sh",
		`[{"id": "FILE1", "decompress": true}, {"id": "RUNSH", "decompress": true}, {"id": "INPUTZIP", "decompress": true}]`))
}
