package compat

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestStatusCmd_RequiresJobID(t *testing.T) {
	rootCmd := NewCompatRootCmd()
	rootCmd.SetArgs([]string{"status"})
	err := rootCmd.Execute()
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
	rootCmd := NewCompatRootCmd()
	rootCmd.SetArgs([]string{"status", "-e", "-j", "TEST123"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error (no API key configured)")
	}
	if strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("status -e should no longer be deferred, got: %v", err)
	}
}

func TestStatusCmd_DeferredLoadHours(t *testing.T) {
	rootCmd := NewCompatRootCmd()
	rootCmd.SetArgs([]string{"status", "--load-hours", "24", "-j", "TEST123"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for deferred --load-hours flag")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("expected 'not yet implemented' error, got: %v", err)
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
		{"extended-output", "e", true},
		{"load-hours", "", true},
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
	rootCmd := NewCompatRootCmd()
	rootCmd.SetArgs([]string{"stop"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing --job-id")
	}
	if !strings.Contains(err.Error(), "--job-id is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDeleteCmd_RequiresJobID(t *testing.T) {
	rootCmd := NewCompatRootCmd()
	rootCmd.SetArgs([]string{"delete"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing --job-id")
	}
	if !strings.Contains(err.Error(), "--job-id is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCheckForUpdateCmd_SkipAuth(t *testing.T) {
	cmd := newCheckForUpdateCmd()

	if cmd.Annotations == nil || cmd.Annotations["skipAuth"] != "true" {
		t.Error("check-for-update should have skipAuth annotation")
	}
}

func TestCheckForUpdateCmd_RunsWithoutAPIKey(t *testing.T) {
	rootCmd := NewCompatRootCmd()

	var out strings.Builder
	rootCmd.SetOut(&out)
	rootCmd.SetArgs([]string{"check-for-update"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("check-for-update returned error: %v", err)
	}
}

func TestCheckForUpdateCmd_DeferredInstallFlag(t *testing.T) {
	rootCmd := NewCompatRootCmd()
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
	rootCmd := NewCompatRootCmd()
	rootCmd.SetArgs([]string{"list-info"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when neither -c nor -a specified")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestListInfoCmd_MutualExclusion(t *testing.T) {
	rootCmd := NewCompatRootCmd()
	rootCmd.SetArgs([]string{"list-info", "-c", "-a"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for mutually exclusive flags")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestListInfoCmd_DeferredDesktopsFlag(t *testing.T) {
	rootCmd := NewCompatRootCmd()
	rootCmd.SetArgs([]string{"list-info", "-d"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for deferred -d flag")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("expected 'not yet implemented' error, got: %v", err)
	}
}

func TestUploadCmd_RequiresFiles(t *testing.T) {
	rootCmd := NewCompatRootCmd()
	rootCmd.SetArgs([]string{"upload"})
	err := rootCmd.Execute()
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
		{"report", []string{"upload", "-r", "report.txt", "-f", "a.txt"}},
		{"copy-to-cfs", []string{"upload", "--copy-to-cfs", "-f", "a.txt"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rootCmd := NewCompatRootCmd()
			rootCmd.SetArgs(tt.args)
			err := rootCmd.Execute()
			if err == nil {
				t.Fatal("expected error for deferred flag")
			}
			if !strings.Contains(err.Error(), "not yet implemented") {
				t.Errorf("expected 'not yet implemented' error, got: %v", err)
			}
		})
	}
}

func TestUploadCmd_ExtendedOutputRequiresAPI(t *testing.T) {
	rootCmd := NewCompatRootCmd()
	rootCmd.SetArgs([]string{"upload", "-e", "-f", "a.txt"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error (no API key configured)")
	}
	if strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("upload -e should no longer be deferred, got: %v", err)
	}
}

func TestDownloadFileCmd_RequiresIDFlag(t *testing.T) {
	rootCmd := NewCompatRootCmd()
	rootCmd.SetArgs([]string{"download-file"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when neither -j nor --file-id specified")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDownloadFileCmd_MutualExclusion(t *testing.T) {
	rootCmd := NewCompatRootCmd()
	rootCmd.SetArgs([]string{"download-file", "-j", "JOB1", "--file-id", "FILE1"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for mutually exclusive flags")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDownloadFileCmd_DeferredFlags(t *testing.T) {
	rootCmd := NewCompatRootCmd()
	rootCmd.SetArgs([]string{"download-file", "-r", "RUN1", "-j", "JOB1"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for deferred flag")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("expected 'not yet implemented' error, got: %v", err)
	}
}

func TestDownloadFileCmd_ExtendedJobIDNotSupported(t *testing.T) {
	rootCmd := NewCompatRootCmd()
	rootCmd.SetArgs([]string{"download-file", "-e", "-j", "JOB1"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for -e -j combination")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("expected 'not supported' error about rescale-cli bug, got: %v", err)
	}
}

func TestDownloadFileCmd_ExtendedFileIDRequiresAPI(t *testing.T) {
	rootCmd := NewCompatRootCmd()
	rootCmd.SetArgs([]string{"download-file", "-e", "--file-id", "FILE1"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error (no API key configured)")
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

func TestSubmitCmd_RequiresScript(t *testing.T) {
	rootCmd := NewCompatRootCmd()
	rootCmd.SetArgs([]string{"submit"})
	err := rootCmd.Execute()
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
	rootCmd := NewCompatRootCmd()
	rootCmd.SetArgs([]string{"submit", "-e", "-i", "nonexistent_script.sh"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for nonexistent script file")
	}
	if strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("submit -e should no longer be deferred, got: %v", err)
	}
}

func TestSubmitCmd_DeferredFlags(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantMatch string
	}{
		{"p-cluster", []string{"submit", "--p-cluster", "CL1", "-i", "script.sh"}, "not yet implemented"},
		{"waive-sla", []string{"submit", "--waive-sla", "-i", "script.sh"}, "not yet implemented"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rootCmd := NewCompatRootCmd()
			rootCmd.SetArgs(tt.args)
			err := rootCmd.Execute()
			if err == nil {
				t.Fatal("expected error for deferred flag")
			}
			if !strings.Contains(err.Error(), tt.wantMatch) {
				t.Errorf("expected error containing %q, got: %v", tt.wantMatch, err)
			}
		})
	}
}

func TestAllCommandsRegistered(t *testing.T) {
	rootCmd := NewCompatRootCmd()

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

func TestRootCmd_AuthSkipAnnotation(t *testing.T) {
	rootCmd := NewCompatRootCmd()

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
