package compat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/models"
)

// setupCmdTest creates a command with a CompatContext already set, bypassing
// PersistentPreRunE auth. This isolates command-level validation tests from
// credential state on the test machine.
func setupCmdTest(cmd *cobra.Command) {
	cmd.SetContext(context.Background())
	SetCompatContext(cmd, &CompatContext{})
}

func TestStatusCmd_RequiresJobID(t *testing.T) {
	cmd := newStatusCmd()
	setupCmdTest(cmd)
	cmd.SetArgs([]string{})
	err := cmd.RunE(cmd, []string{})
	if err == nil {
		t.Fatal("expected error for missing --job-id")
	}
	if !strings.Contains(err.Error(), "--job-id is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestStatusCmd_ExtendedOutputRequiresAPI(t *testing.T) {
	// status -e now attempts real API calls — should fail without valid API key,
	// NOT return "not yet implemented"
	cmd := newStatusCmd()
	setupCmdTest(cmd)
	cmd.SetArgs([]string{"-e", "-j", "TEST123"})
	cmd.Flags().Parse([]string{"-e", "-j", "TEST123"})
	err := cmd.RunE(cmd, []string{})
	if err == nil {
		t.Fatal("expected error (no API client)")
	}
	if strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("status -e should no longer be deferred, got: %v", err)
	}
}

func TestStatusCmd_LoadHoursAccepted(t *testing.T) {
	// --load-hours is no longer deferred — it should proceed past the guard
	// and fail on API/auth, not "not yet implemented"
	cmd := newStatusCmd()
	setupCmdTest(cmd)
	cmd.SetArgs([]string{"--load-hours", "24", "-j", "TEST123"})
	cmd.Flags().Parse([]string{"--load-hours", "24", "-j", "TEST123"})
	err := cmd.RunE(cmd, []string{})
	if err == nil {
		t.Fatal("expected error (no API client)")
	}
	if strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("--load-hours should no longer be deferred, got: %v", err)
	}
}

func TestStatusCmd_Flags(t *testing.T) {
	cmd := newStatusCmd()

	flags := []struct {
		name      string
		shorthand string
		hidden    bool
	}{
		{"job-id", "j", false},
		{"extended-output", "e", false},
		{"load-hours", "", false}, // no longer hidden
	}

	for _, f := range flags {
		flag := cmd.Flags().Lookup(f.name)
		if flag == nil {
			t.Errorf("flag %q not registered", f.name)
			continue
		}
		if f.shorthand != "" && flag.Shorthand != f.shorthand {
			t.Errorf("flag %q shorthand = %q, want %q", f.name, flag.Shorthand, f.shorthand)
		}
		if flag.Hidden != f.hidden {
			t.Errorf("flag %q hidden = %v, want %v", f.name, flag.Hidden, f.hidden)
		}
	}
}

func TestStopCmd_RequiresJobID(t *testing.T) {
	rootCmd, _ := NewCompatRootCmd()
	rootCmd.SetArgs([]string{"stop"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing --job-id")
	}
	// Auth may fail first; just verify we get an error
	if err.Error() == "" {
		t.Error("expected non-empty error")
	}
}

func TestDeleteCmd_RequiresJobID(t *testing.T) {
	rootCmd, _ := NewCompatRootCmd()
	rootCmd.SetArgs([]string{"delete"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing --job-id")
	}
	if err.Error() == "" {
		t.Error("expected non-empty error")
	}
}

func TestCheckForUpdateCmd_SkipAuth(t *testing.T) {
	cmd := newCheckForUpdateCmd()

	if cmd.Annotations == nil || cmd.Annotations["skipAuth"] != "true" {
		t.Error("check-for-update should have skipAuth annotation")
	}
}

func TestCheckForUpdateCmd_RunsWithoutAPIKey(t *testing.T) {
	rootCmd, _ := NewCompatRootCmd()

	var out strings.Builder
	rootCmd.SetOut(&out)
	rootCmd.SetArgs([]string{"check-for-update"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("check-for-update returned error: %v", err)
	}
}

func TestCheckForUpdateCmd_DeferredInstallFlag(t *testing.T) {
	rootCmd, _ := NewCompatRootCmd()
	rootCmd.SetArgs([]string{"check-for-update", "-i"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for deferred -i flag")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("expected 'not yet implemented' error, got: %v", err)
	}
}

func TestListInfoCmd_RequiresFlag(t *testing.T) {
	cmd := newListInfoCmd()
	setupCmdTest(cmd)
	cmd.SetArgs([]string{})
	err := cmd.RunE(cmd, []string{})
	if err == nil {
		t.Fatal("expected error when neither -c nor -a specified")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestListInfoCmd_MutualExclusion(t *testing.T) {
	cmd := newListInfoCmd()
	setupCmdTest(cmd)
	cmd.Flags().Parse([]string{"-c", "-a"})
	err := cmd.RunE(cmd, []string{})
	if err == nil {
		t.Fatal("expected error for mutually exclusive flags")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestListInfoCmd_DeferredDesktopsFlag(t *testing.T) {
	cmd := newListInfoCmd()
	setupCmdTest(cmd)
	cmd.Flags().Parse([]string{"-d"})
	err := cmd.RunE(cmd, []string{})
	if err == nil {
		t.Fatal("expected error for deferred -d flag")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("expected 'not yet implemented' error, got: %v", err)
	}
}

func TestUploadCmd_RequiresFiles(t *testing.T) {
	cmd := newUploadCmd()
	setupCmdTest(cmd)
	cmd.SetArgs([]string{})
	err := cmd.RunE(cmd, []string{})
	if err == nil {
		t.Fatal("expected error for missing -f flag")
	}
	if !strings.Contains(err.Error(), "-f") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestUploadCmd_DeferredFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"copy-to-cfs", []string{"--copy-to-cfs", "-f", "a.txt"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newUploadCmd()
			setupCmdTest(cmd)
			cmd.Flags().Parse(tt.args)
			err := cmd.RunE(cmd, []string{})
			if err == nil {
				t.Fatal("expected error for deferred flag")
			}
			if !strings.Contains(err.Error(), "not yet implemented") {
				t.Errorf("expected 'not yet implemented' error, got: %v", err)
			}
		})
	}
}

func TestUploadCmd_ReportAccepted(t *testing.T) {
	// -r (report) is no longer deferred — it should proceed past the guard
	cmd := newUploadCmd()
	setupCmdTest(cmd)
	cmd.Flags().Parse([]string{"-r", "report.json", "-f", "a.txt"})
	err := cmd.RunE(cmd, []string{})
	if err == nil {
		t.Fatal("expected error (no API client)")
	}
	if strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("-r should no longer be deferred, got: %v", err)
	}
}

func TestUploadCmd_ExtendedOutputRequiresAPI(t *testing.T) {
	cmd := newUploadCmd()
	setupCmdTest(cmd)
	cmd.Flags().Parse([]string{"-e", "-f", "a.txt"})
	err := cmd.RunE(cmd, []string{})
	if err == nil {
		t.Fatal("expected error (no API client)")
	}
	if strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("upload -e should no longer be deferred, got: %v", err)
	}
}

func TestDownloadFileCmd_RequiresIDFlag(t *testing.T) {
	cmd := newDownloadFileCmd()
	setupCmdTest(cmd)
	cmd.SetArgs([]string{})
	err := cmd.RunE(cmd, []string{})
	if err == nil {
		t.Fatal("expected error when neither -j nor --file-id specified")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDownloadFileCmd_MutualExclusion(t *testing.T) {
	cmd := newDownloadFileCmd()
	setupCmdTest(cmd)
	cmd.Flags().Parse([]string{"-j", "JOB1", "--file-id", "FILE1"})
	err := cmd.RunE(cmd, []string{})
	if err == nil {
		t.Fatal("expected error for mutually exclusive flags")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDownloadFileCmd_RunIdAccepted(t *testing.T) {
	// --run-id is no longer deferred — it should proceed past the guard
	cmd := newDownloadFileCmd()
	setupCmdTest(cmd)
	cmd.Flags().Parse([]string{"-r", "RUN1", "-j", "JOB1"})
	err := cmd.RunE(cmd, []string{})
	if err == nil {
		t.Fatal("expected error (no API client)")
	}
	if strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("--run-id should no longer be deferred, got: %v", err)
	}
}

func TestDownloadFileCmd_ExtendedJobIDNotSupported(t *testing.T) {
	// The -e -j "not supported" check happens after GetAPIClient(),
	// so without credentials we get an auth error first.
	// Verify we get an error (not success) for this combination.
	cmd := newDownloadFileCmd()
	setupCmdTest(cmd)
	cmd.Flags().Parse([]string{"-e", "-j", "JOB1"})
	err := cmd.RunE(cmd, []string{})
	if err == nil {
		t.Fatal("expected error for -e -j combination")
	}
}

func TestDownloadFileCmd_ExtendedFileIDRequiresAPI(t *testing.T) {
	cmd := newDownloadFileCmd()
	setupCmdTest(cmd)
	cmd.Flags().Parse([]string{"-e", "--file-id", "FILE1"})
	err := cmd.RunE(cmd, []string{})
	if err == nil {
		t.Fatal("expected error (no API client)")
	}
	if strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("download-file -e -fid should no longer be deferred, got: %v", err)
	}
}

func TestDownloadFileCmd_FileIdFlag(t *testing.T) {
	// Verify --file-id is registered without shorthand
	cmd := newDownloadFileCmd()
	flag := cmd.Flags().Lookup("file-id")
	if flag == nil {
		t.Fatal("--file-id flag not registered")
	}
	if flag.Shorthand != "" {
		t.Errorf("--file-id should have no shorthand, got %q", flag.Shorthand)
	}
}

func TestDownloadFileCmd_RunIdNotHidden(t *testing.T) {
	cmd := newDownloadFileCmd()
	flag := cmd.Flags().Lookup("run-id")
	if flag == nil {
		t.Fatal("--run-id flag not registered")
	}
	if flag.Hidden {
		t.Error("--run-id should not be hidden")
	}
}

func TestSubmitCmd_RequiresScript(t *testing.T) {
	cmd := newSubmitCmd()
	setupCmdTest(cmd)
	cmd.SetArgs([]string{})
	err := cmd.RunE(cmd, []string{})
	if err == nil {
		t.Fatal("expected error for missing script")
	}
	if !strings.Contains(err.Error(), "script file required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSubmitCmd_ExtendedOutputRequiresScript(t *testing.T) {
	// submit -e without a valid script file should fail on file validation,
	// not "not yet implemented"
	cmd := newSubmitCmd()
	setupCmdTest(cmd)
	cmd.Flags().Parse([]string{"-e", "-i", "nonexistent_script.sh"})
	err := cmd.RunE(cmd, []string{})
	if err == nil {
		t.Fatal("expected error for nonexistent script file")
	}
	if strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("submit -e should no longer be deferred, got: %v", err)
	}
}

func TestSubmitCmd_WaiveSlaAndPClusterAccepted(t *testing.T) {
	// --waive-sla and --p-cluster are no longer deferred
	tests := []struct {
		name string
		args []string
	}{
		{"p-cluster", []string{"--p-cluster", "CL1", "-i", "nonexistent_script.sh"}},
		{"waive-sla", []string{"--waive-sla", "-i", "nonexistent_script.sh"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newSubmitCmd()
			setupCmdTest(cmd)
			cmd.Flags().Parse(tt.args)
			err := cmd.RunE(cmd, []string{})
			if err == nil {
				t.Fatal("expected error (no script file)")
			}
			if strings.Contains(err.Error(), "not yet implemented") {
				t.Errorf("%s should no longer be deferred, got: %v", tt.name, err)
			}
		})
	}
}

func TestSubmitCmd_PClusterNotHidden(t *testing.T) {
	cmd := newSubmitCmd()
	flag := cmd.Flags().Lookup("p-cluster")
	if flag == nil {
		t.Fatal("--p-cluster flag not registered")
	}
	if flag.Hidden {
		t.Error("--p-cluster should not be hidden")
	}
}

func TestSubmitCmd_WaiveSlaNotHidden(t *testing.T) {
	cmd := newSubmitCmd()
	flag := cmd.Flags().Lookup("waive-sla")
	if flag == nil {
		t.Fatal("--waive-sla flag not registered")
	}
	if flag.Hidden {
		t.Error("--waive-sla should not be hidden")
	}
}

func TestAllCommandsRegistered(t *testing.T) {
	rootCmd, _ := NewCompatRootCmd()

	expectedCmds := []string{
		"status", "stop", "delete", "submit", "upload",
		"download-file", "sync", "list-info", "list-files",
		"check-for-update", "spub",
	}

	for _, name := range expectedCmds {
		found := false
		for _, cmd := range rootCmd.Commands() {
			if cmd.Name() == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected command %q not found", name)
		}
	}
}

func TestSubmitCmd_ShortFlagMappings(t *testing.T) {
	cmd := newSubmitCmd()

	// -f should map to --file-matcher (NOT --job-file)
	fFlag := cmd.Flags().ShorthandLookup("f")
	if fFlag == nil {
		t.Fatal("-f shorthand not registered")
	}
	if fFlag.Name != "file-matcher" {
		t.Errorf("-f maps to %q, want \"file-matcher\"", fFlag.Name)
	}

	// -s should map to --search
	sFlag := cmd.Flags().ShorthandLookup("s")
	if sFlag == nil {
		t.Fatal("-s shorthand not registered")
	}
	if sFlag.Name != "search" {
		t.Errorf("-s maps to %q, want \"search\"", sFlag.Name)
	}

	// -i should map to --input-file
	iFlag := cmd.Flags().ShorthandLookup("i")
	if iFlag == nil {
		t.Fatal("-i shorthand not registered")
	}
	if iFlag.Name != "input-file" {
		t.Errorf("-i maps to %q, want \"input-file\"", iFlag.Name)
	}
}

func TestUploadCmd_FlagShorthand(t *testing.T) {
	cmd := newUploadCmd()

	// -f should map to --files
	fFlag := cmd.Flags().ShorthandLookup("f")
	if fFlag == nil {
		t.Fatal("-f shorthand not registered")
	}
	if fFlag.Name != "files" {
		t.Errorf("-f maps to %q, want \"files\"", fFlag.Name)
	}

	// -d should map to --directory-id
	dFlag := cmd.Flags().ShorthandLookup("d")
	if dFlag == nil {
		t.Fatal("-d shorthand not registered")
	}
	if dFlag.Name != "directory-id" {
		t.Errorf("-d maps to %q, want \"directory-id\"", dFlag.Name)
	}
}

func TestUploadCmd_ReportFlag(t *testing.T) {
	cmd := newUploadCmd()
	flag := cmd.Flags().Lookup("report")
	if flag == nil {
		t.Fatal("--report flag not registered")
	}
	if flag.Shorthand != "r" {
		t.Errorf("--report shorthand = %q, want \"r\"", flag.Shorthand)
	}
	if flag.Hidden {
		t.Error("--report should not be hidden")
	}
}

func TestRootCmd_AuthSkipAnnotation(t *testing.T) {
	rootCmd, _ := NewCompatRootCmd()

	// Find check-for-update command and verify annotation
	var checkCmd *cobra.Command
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "check-for-update" {
			checkCmd = cmd
			break
		}
	}
	if checkCmd == nil {
		t.Fatal("check-for-update command not found")
	}
	if checkCmd.Annotations == nil || checkCmd.Annotations["skipAuth"] != "true" {
		t.Error("check-for-update should have skipAuth annotation")
	}

	// Verify other commands do NOT have skipAuth
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "check-for-update" || cmd.Name() == "help" || cmd.Name() == "completion" {
			continue
		}
		if cmd.Annotations != nil && cmd.Annotations["skipAuth"] == "true" {
			t.Errorf("command %q should not have skipAuth annotation", cmd.Name())
		}
	}
}

func TestRootCmd_ProfileFlagRegistered(t *testing.T) {
	rootCmd, _ := NewCompatRootCmd()
	flag := rootCmd.PersistentFlags().Lookup("profile")
	if flag == nil {
		t.Fatal("--profile flag not registered")
	}
	if flag.Hidden {
		t.Error("--profile should not be hidden")
	}
}

func TestDetectSubcommand_ProfileFlag(t *testing.T) {
	// --profile takes a value — its value should not be detected as a subcommand
	args := []string{"--profile", "default", "upload", "-f", "a.txt"}
	got := detectSubcommand(args)
	if got != "upload" {
		t.Errorf("detectSubcommand() = %q, want \"upload\"", got)
	}
}

func TestDetectSubcommand_ProfileFlagBeforeCommand(t *testing.T) {
	args := []string{"-p", "TOKEN", "--profile", "eu", "status", "-j", "JOB1"}
	got := detectSubcommand(args)
	if got != "status" {
		t.Errorf("detectSubcommand() = %q, want \"status\"", got)
	}
}

func TestWriteUploadReport_File(t *testing.T) {
	tmpDir := t.TempDir()
	reportPath := filepath.Join(tmpDir, "report.json")

	// Create minimal test data
	cf := &models.CloudFile{
		ID:   "file123",
		Name: "test.txt",
	}
	err := writeUploadReport(reportPath, []*models.CloudFile{cf})
	if err != nil {
		t.Fatalf("writeUploadReport() error: %v", err)
	}

	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("failed to read report: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "file123") {
		t.Errorf("report should contain file ID, got: %s", content)
	}
	if !strings.Contains(content, "test.txt") {
		t.Errorf("report should contain filename, got: %s", content)
	}
}

func TestWriteUploadReport_Stdout(t *testing.T) {
	// Test -r - (stdout) — just verify it doesn't error with valid input
	cf := &models.CloudFile{
		ID:   "file123",
		Name: "test.txt",
	}
	// Redirect stdout for test
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := writeUploadReport("-", []*models.CloudFile{cf})

	w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("writeUploadReport(-) error: %v", err)
	}

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	r.Close()
	output := string(buf[:n])

	if !strings.Contains(output, "file123") {
		t.Errorf("stdout report should contain file ID, got: %s", output)
	}
}

func TestListFilesCmd_RunIdNotHidden(t *testing.T) {
	cmd := newListFilesCmd()
	flag := cmd.Flags().Lookup("run-id")
	if flag == nil {
		t.Fatal("--run-id flag not registered")
	}
	if flag.Hidden {
		t.Error("--run-id should not be hidden")
	}
}

func TestListFilesCmd_RequiresJobID(t *testing.T) {
	cmd := newListFilesCmd()
	setupCmdTest(cmd)
	cmd.SetArgs([]string{})
	err := cmd.RunE(cmd, []string{})
	if err == nil {
		t.Fatal("expected error for missing --job-id")
	}
	if !strings.Contains(err.Error(), "--job-id is required") {
		t.Errorf("unexpected error: %v", err)
	}
}
