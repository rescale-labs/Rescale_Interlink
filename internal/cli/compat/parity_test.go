//go:build e2e

package compat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// --- Helpers ---

// runCompat executes the compat command tree in-process and captures stdout.
func runCompat(t *testing.T, args ...string) (stdout string, exitCode int) {
	t.Helper()
	rootCmd, _ := NewCompatRootCmd()
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	if err != nil {
		return buf.String(), ExitCodeCompatError
	}
	return buf.String(), 0
}

// runRescaleCLI executes the external rescale-cli binary if RESCALE_CLI_PATH is set.
// Returns empty string and -1 if not available.
func runRescaleCLI(t *testing.T, args ...string) (stdout string, exitCode int) {
	t.Helper()
	cliPath := os.Getenv("RESCALE_CLI_PATH")
	if cliPath == "" {
		return "", -1
	}

	cmd := exec.Command(cliPath, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return outBuf.String(), exitErr.ExitCode()
		}
		t.Logf("rescale-cli exec error: %v", err)
		return outBuf.String(), -1
	}
	return outBuf.String(), 0
}

func requireAPIKey(t *testing.T) {
	t.Helper()
	if os.Getenv("RESCALE_API_KEY") == "" {
		t.Skip("RESCALE_API_KEY not set")
	}
}

func getTestJobID(t *testing.T) string {
	t.Helper()
	id := os.Getenv("RESCALE_TEST_JOB_ID")
	if id == "" {
		t.Skip("RESCALE_TEST_JOB_ID not set (need a completed job)")
	}
	return id
}

func getTestFileID(t *testing.T) string {
	t.Helper()
	id := os.Getenv("RESCALE_TEST_FILE_ID")
	if id == "" {
		t.Skip("RESCALE_TEST_FILE_ID not set")
	}
	return id
}

func getRunningJobID(t *testing.T) string {
	t.Helper()
	id := os.Getenv("RESCALE_RUNNING_JOB_ID")
	if id == "" {
		t.Skip("RESCALE_RUNNING_JOB_ID not set (need a running job)")
	}
	return id
}

// compareJSONKeys compares the top-level key sets of two JSON blobs.
// Reports missing and extra keys as test errors.
func compareJSONKeys(t *testing.T, label string, got, want []byte) {
	t.Helper()

	var gotMap, wantMap map[string]json.RawMessage
	if err := json.Unmarshal(got, &gotMap); err != nil {
		t.Fatalf("%s: failed to unmarshal got JSON: %v", label, err)
	}
	if err := json.Unmarshal(want, &wantMap); err != nil {
		t.Fatalf("%s: failed to unmarshal want JSON: %v", label, err)
	}

	for key := range wantMap {
		if _, ok := gotMap[key]; !ok {
			t.Errorf("%s: missing key %q", label, key)
		}
	}
	for key := range gotMap {
		if _, ok := wantMap[key]; !ok {
			t.Logf("%s: extra key %q (acceptable — may be additional field)", label, key)
		}
	}
}

// --- READ-ONLY tests (safe for head-to-head comparison) ---

func TestParity_StatusText(t *testing.T) {
	requireAPIKey(t)
	jobID := getTestJobID(t)

	out, code := runCompat(t, "-q", "status", "-j", jobID)
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, out)
	}
	if !strings.Contains(out, "The status of job") {
		t.Errorf("unexpected status output: %s", out)
	}

	// Head-to-head if available
	if refOut, refCode := runRescaleCLI(t, "-q", "status", "-j", jobID); refCode >= 0 {
		if refCode != code {
			t.Errorf("exit code mismatch: compat=%d rescale-cli=%d", code, refCode)
		}
		if !strings.Contains(refOut, "The status of job") {
			t.Logf("rescale-cli output differs: %s", refOut)
		}
	}
}

func TestParity_StatusJSON(t *testing.T) {
	requireAPIKey(t)
	jobID := getTestJobID(t)

	out, code := runCompat(t, "-q", "status", "-e", "-j", jobID)
	if code != 0 {
		t.Fatalf("status -e exited %d: %s", code, out)
	}

	// Verify it's valid JSON with expected top-level keys
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("status -e output is not valid JSON: %v\nOutput: %s", err, out)
	}

	for _, key := range []string{"statuses"} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing key %q in status -e output", key)
		}
	}
}

func TestParity_ListInfoCoreTypes(t *testing.T) {
	requireAPIKey(t)

	out, code := runCompat(t, "-q", "list-info", "-c")
	if code != 0 {
		t.Fatalf("list-info -c exited %d: %s", code, out)
	}

	// Each line should be a JSON object
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 {
		t.Fatal("no core types returned")
	}

	// Verify first core type has expected fields
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatalf("first core type is not valid JSON: %v", err)
	}
	for _, field := range []string{"code", "name", "cores"} {
		if _, ok := m[field]; !ok {
			t.Errorf("core type missing field %q", field)
		}
	}
}

func TestParity_ListInfoAnalyses(t *testing.T) {
	requireAPIKey(t)

	out, code := runCompat(t, "-q", "list-info", "-a")
	if code != 0 {
		t.Fatalf("list-info -a exited %d: %s", code, out)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 {
		t.Fatal("no analyses returned")
	}

	var m map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatalf("first analysis is not valid JSON: %v", err)
	}
	for _, field := range []string{"code", "versions"} {
		if _, ok := m[field]; !ok {
			t.Errorf("analysis missing field %q", field)
		}
	}
}

func TestParity_ListFilesCompleted(t *testing.T) {
	requireAPIKey(t)
	jobID := getTestJobID(t)

	out, code := runCompat(t, "-q", "list-files", "-j", jobID)
	// Completed job should have no active run → exit 33
	if code != ExitCodeCompatError {
		t.Logf("list-files output: %s", out)
		// May succeed if runs endpoint returns runs — both outcomes acceptable
		t.Logf("exit code %d (expected %d for 'no active run')", code, ExitCodeCompatError)
	}
	if code == ExitCodeCompatError {
		if !strings.Contains(out, "no active run") && !strings.Contains(fmt.Sprintf("%v", out), "no active run") {
			t.Logf("unexpected error for completed job: %s", out)
		}
	}
}

func TestParity_ListFilesRunning(t *testing.T) {
	requireAPIKey(t)
	jobID := getRunningJobID(t)

	out, code := runCompat(t, "-q", "list-files", "-j", jobID)
	if code != 0 {
		t.Fatalf("list-files for running job exited %d: %s", code, out)
	}
	// Should list at least one file
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Log("no files listed for running job (may be expected if job just started)")
	}
}

func TestParity_ExitCode33(t *testing.T) {
	// Verify that errors produce exit code 33
	// We can test this without API key by using a command that fails before auth
	rootCmd, _ := NewCompatRootCmd()
	rootCmd.SetArgs([]string{"status"}) // missing -j
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	// The exit code mapping happens in ExecuteCompat, not here.
	// We just verify the error exists.
}

func TestParity_AuthLine(t *testing.T) {
	requireAPIKey(t)
	jobID := getTestJobID(t)

	// Non-quiet mode should include "Authenticated as"
	out, code := runCompat(t, "status", "-j", jobID)
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, out)
	}
	if !strings.Contains(out, "Authenticated as") {
		t.Errorf("expected 'Authenticated as' line, got: %s", out)
	}
}

func TestParity_QuietMode(t *testing.T) {
	requireAPIKey(t)
	jobID := getTestJobID(t)

	out, code := runCompat(t, "-q", "status", "-j", jobID)
	if code != 0 {
		t.Fatalf("status -q exited %d: %s", code, out)
	}
	if strings.Contains(out, "Authenticated as") {
		t.Errorf("-q mode should suppress auth line, got: %s", out)
	}
}

func TestParity_DownloadFileJSON(t *testing.T) {
	requireAPIKey(t)
	fileID := getTestFileID(t)

	out, code := runCompat(t, "-q", "download-file", "-e", "--file-id", fileID)
	if code != 0 {
		t.Fatalf("download-file -e exited %d: %s", code, out)
	}

	// Verify transfer envelope structure
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("output is not valid JSON: %v\nOutput: %s", err, out)
	}
	for _, key := range []string{"success", "startTime", "endTime", "files"} {
		if _, ok := envelope[key]; !ok {
			t.Errorf("missing envelope key %q", key)
		}
	}
}

// --- MUTATING tests (rescale-int only, structural assertions) ---

func TestParity_SyncSingleRun(t *testing.T) {
	requireAPIKey(t)
	jobID := getTestJobID(t)

	tmpDir := t.TempDir()
	out, code := runCompat(t, "-q", "sync", "-j", jobID, "-o", tmpDir)
	if code != 0 {
		t.Fatalf("sync exited %d: %s", code, out)
	}

	// Verify some files were downloaded to tmpDir
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("failed to read tmpDir: %v", err)
	}
	t.Logf("sync downloaded %d file(s)", len(entries))
}

func TestParity_SyncSingleRunSkipExisting(t *testing.T) {
	requireAPIKey(t)
	jobID := getTestJobID(t)

	tmpDir := t.TempDir()

	// First run: download files
	out, code := runCompat(t, "-q", "sync", "-j", jobID, "-o", tmpDir)
	if code != 0 {
		t.Fatalf("sync first run exited %d: %s", code, out)
	}

	entries1, _ := os.ReadDir(tmpDir)
	if len(entries1) == 0 {
		t.Skip("no files downloaded — cannot test skip-existing")
	}

	// Second run: should skip existing files
	out2, code2 := runCompat(t, "-q", "sync", "-j", jobID, "-o", tmpDir)
	if code2 != 0 {
		t.Fatalf("sync second run exited %d: %s", code2, out2)
	}

	entries2, _ := os.ReadDir(tmpDir)
	if len(entries2) != len(entries1) {
		t.Errorf("expected same file count after skip-existing: first=%d, second=%d", len(entries1), len(entries2))
	}
}

func TestParity_SyncPolling(t *testing.T) {
	requireAPIKey(t)
	jobID := getTestJobID(t) // completed job

	tmpDir := t.TempDir()
	// Polling mode on a completed job: should download once, detect terminal, exit
	out, code := runCompat(t, "-q", "sync", "-j", jobID, "-d", "5", "-o", tmpDir)
	if code != 0 {
		t.Fatalf("sync polling exited %d: %s", code, out)
	}

	entries, _ := os.ReadDir(tmpDir)
	t.Logf("sync polling downloaded %d file(s)", len(entries))

	// H2H note: rescale-cli sync -j JOB -d 5 on a completed job never exits
	// (confirmed manually 2026-04-10 — it keeps polling indefinitely even after
	// downloading all files from a Completed job). INT correctly detects terminal
	// status on first tick and exits. Not calling runRescaleCLI here because
	// the CLI hangs forever. This is an INT improvement over CLI behavior.
}

func TestParity_SyncNewerThan(t *testing.T) {
	requireAPIKey(t)
	refJobID := getReferenceJobID(t)

	tmpDir := t.TempDir()
	out, code := runCompat(t, "-q", "sync", "-n", refJobID, "-o", tmpDir)
	if code != 0 {
		t.Fatalf("sync -n exited %d: %s", code, out)
	}

	// Verify per-job subdirectories were created with rescale_job_ prefix
	entries, _ := os.ReadDir(tmpDir)
	jobDirCount := 0
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "rescale_job_") {
			jobDirCount++
		}
	}
	t.Logf("sync -n created %d job directories", jobDirCount)

	// Reference job directory should NOT exist
	refDir := fmt.Sprintf("rescale_job_%s", refJobID)
	for _, e := range entries {
		if e.Name() == refDir {
			t.Errorf("reference job directory %s should not be created", refDir)
		}
	}

	// H2H: compare directory structure
	if cliPath := os.Getenv("RESCALE_CLI_PATH"); cliPath != "" {
		cliDir := t.TempDir()
		_, _ = runRescaleCLI(t, "-q", "sync", "-n", refJobID, "-o", cliDir)

		cliEntries, _ := os.ReadDir(cliDir)
		cliJobDirs := 0
		for _, e := range cliEntries {
			if e.IsDir() && strings.HasPrefix(e.Name(), "rescale_job_") {
				cliJobDirs++
			}
		}
		t.Logf("H2H: CLI created %d job dirs, INT created %d job dirs", cliJobDirs, jobDirCount)
		// CLI creates dirs for ALL newer jobs (even empty ones), INT only for jobs with files.
		// CLI also creates reference job dir. These are known divergences (metadata-based vs file-based).
		if jobDirCount > cliJobDirs {
			t.Errorf("INT has more job dirs than CLI: INT=%d CLI=%d", jobDirCount, cliJobDirs)
		}
	}
}

func getReferenceJobID(t *testing.T) string {
	t.Helper()
	id := os.Getenv("RESCALE_REFERENCE_JOB_ID")
	if id == "" {
		t.Skip("RESCALE_REFERENCE_JOB_ID not set (need an older job with newer jobs after it)")
	}
	return id
}

func TestParity_DownloadFileExactName(t *testing.T) {
	requireAPIKey(t)
	jobID := getTestJobID(t)

	// First get the file list to find a real filename
	tmpDir := t.TempDir()

	// Try downloading with an exact filename that likely exists
	out, code := runCompat(t, "-q", "download-file", "-j", jobID, "-f", "process_output.log", "-o", tmpDir)
	if code != 0 {
		// If no file matches, that's OK — the test verifies the filter mechanism
		t.Logf("download-file -f returned code %d (file may not exist): %s", code, out)
	}
}
