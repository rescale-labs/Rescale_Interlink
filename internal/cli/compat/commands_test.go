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

func TestCompatCmdValidation(t *testing.T) {
	tests := []struct {
		name string
		// newCmd builds the command under test; setupCmdTest supplies a
		// CompatContext so PersistentPreRunE auth never runs.
		newCmd    func() *cobra.Command
		args      []string
		wantErr   string // error must mention this
		wantExact string // error message must equal this
		notWant   string // error must not mention this
	}{
		{name: "status requires job ID", newCmd: newStatusCmd, wantErr: "--job-id is required"},
		{name: "status -e reaches the API", newCmd: newStatusCmd, args: []string{"-e", "-j", "TEST123"}, notWant: "not yet implemented"},
		{name: "status --load-hours reaches the API", newCmd: newStatusCmd, args: []string{"--load-hours", "24", "-j", "TEST123"}, notWant: "not yet implemented"},

		{name: "list-info requires -c or -a", newCmd: newListInfoCmd, wantErr: "required"},
		{name: "list-info -c and -a are exclusive", newCmd: newListInfoCmd, args: []string{"-c", "-a"}, wantErr: "mutually exclusive"},
		{name: "list-info -d is deferred", newCmd: newListInfoCmd, args: []string{"-d"}, wantErr: "not yet implemented"},

		{name: "upload requires -f", newCmd: newUploadCmd, wantErr: "-f"},
		{name: "upload --copy-to-cfs is deferred", newCmd: newUploadCmd, args: []string{"--copy-to-cfs", "-f", "a.txt"}, wantErr: "not yet implemented"},
		{name: "upload -r reaches the API", newCmd: newUploadCmd, args: []string{"-r", "report.json", "-f", "a.txt"}, notWant: "not yet implemented"},
		{name: "upload -e reaches the API", newCmd: newUploadCmd, args: []string{"-e", "-f", "a.txt"}, notWant: "not yet implemented"},

		{name: "download-file requires an ID", newCmd: newDownloadFileCmd, wantErr: "required"},
		{name: "download-file -j and --file-id are exclusive", newCmd: newDownloadFileCmd, args: []string{"-j", "JOB1", "--file-id", "FILE1"}, wantErr: "mutually exclusive"},
		{name: "download-file --run-id reaches the API", newCmd: newDownloadFileCmd, args: []string{"-r", "RUN1", "-j", "JOB1"}, notWant: "not yet implemented"},
		// The -e -j rejection happens after GetAPIClient(), so without
		// credentials the auth error arrives first; either way it must fail.
		{name: "download-file -e -j fails", newCmd: newDownloadFileCmd, args: []string{"-e", "-j", "JOB1"}},
		{name: "download-file -e --file-id reaches the API", newCmd: newDownloadFileCmd, args: []string{"-e", "--file-id", "FILE1"}, notWant: "not yet implemented"},

		{name: "submit requires a script", newCmd: newSubmitCmd, wantErr: "script file required"},
		{name: "submit -e validates the script file", newCmd: newSubmitCmd, args: []string{"-e", "-i", "nonexistent_script.sh"}, notWant: "not yet implemented"},
		{name: "submit --p-cluster is accepted", newCmd: newSubmitCmd, args: []string{"--p-cluster", "CL1", "-i", "nonexistent_script.sh"}, notWant: "not yet implemented"},
		{name: "submit --waive-sla is accepted", newCmd: newSubmitCmd, args: []string{"--waive-sla", "-i", "nonexistent_script.sh"}, notWant: "not yet implemented"},

		{name: "list-files requires job ID", newCmd: newListFilesCmd, wantErr: "--job-id is required"},
		{name: "sync requires job ID", newCmd: newSyncCmd, wantExact: "--job-id is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := tt.newCmd()
			setupCmdTest(cmd)
			if err := cmd.Flags().Parse(tt.args); err != nil {
				t.Fatalf("flag parse: %v", err)
			}

			err := cmd.RunE(cmd, []string{})
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if tt.wantExact != "" && err.Error() != tt.wantExact {
				t.Errorf("error = %q, want exactly %q", err.Error(), tt.wantExact)
			}
			if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want mention of %q", err, tt.wantErr)
			}
			if tt.notWant != "" && strings.Contains(err.Error(), tt.notWant) {
				t.Errorf("error = %v, should not mention %q", err, tt.notWant)
			}
		})
	}
}

// TestCompatRootCmdExecute drives commands through the root command so
// PersistentPreRunE (auth) runs, which is what a real invocation does.
func TestCompatRootCmdExecute(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
		errWant string
	}{
		// Auth may fail before the missing-flag check; either way it errors.
		{name: "stop without job ID", args: []string{"stop"}, wantErr: true},
		{name: "delete without job ID", args: []string{"delete"}, wantErr: true},
		{name: "check-for-update needs no API key", args: []string{"check-for-update"}},
		{name: "check-for-update -i is deferred", args: []string{"check-for-update", "-i"}, wantErr: true, errWant: "not yet implemented"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rootCmd, _ := NewCompatRootCmd()
			var out strings.Builder
			rootCmd.SetOut(&out)
			rootCmd.SetArgs(tt.args)

			err := rootCmd.Execute()
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if err.Error() == "" {
				t.Error("expected a non-empty error message")
			}
			if tt.errWant != "" && !strings.Contains(err.Error(), tt.errWant) {
				t.Errorf("error = %v, want mention of %q", err, tt.errWant)
			}
		})
	}
}

func TestCompatCmdFlags(t *testing.T) {
	tests := []struct {
		cmdName       string
		newCmd        func() *cobra.Command
		flag          string
		wantShorthand string
		wantHidden    bool
	}{
		{cmdName: "status", newCmd: newStatusCmd, flag: "job-id", wantShorthand: "j"},
		{cmdName: "status", newCmd: newStatusCmd, flag: "extended-output", wantShorthand: "e"},
		{cmdName: "status", newCmd: newStatusCmd, flag: "load-hours"},
		{cmdName: "download-file", newCmd: newDownloadFileCmd, flag: "file-id"},
		{cmdName: "download-file", newCmd: newDownloadFileCmd, flag: "run-id", wantShorthand: "r"},
		{cmdName: "submit", newCmd: newSubmitCmd, flag: "p-cluster"},
		{cmdName: "submit", newCmd: newSubmitCmd, flag: "waive-sla"},
		{cmdName: "upload", newCmd: newUploadCmd, flag: "report", wantShorthand: "r"},
		{cmdName: "list-files", newCmd: newListFilesCmd, flag: "run-id", wantShorthand: "r"},
	}

	for _, tt := range tests {
		t.Run(tt.cmdName+"/"+tt.flag, func(t *testing.T) {
			flag := tt.newCmd().Flags().Lookup(tt.flag)
			if flag == nil {
				t.Fatalf("--%s not registered", tt.flag)
			}
			if flag.Shorthand != tt.wantShorthand {
				t.Errorf("--%s shorthand = %q, want %q", tt.flag, flag.Shorthand, tt.wantShorthand)
			}
			if flag.Hidden != tt.wantHidden {
				t.Errorf("--%s hidden = %v, want %v", tt.flag, flag.Hidden, tt.wantHidden)
			}
		})
	}
}

// TestCompatCmdShorthandMappings pins the shorthands rescale-cli users type, so
// a renamed long flag cannot silently steal a letter.
func TestCompatCmdShorthandMappings(t *testing.T) {
	tests := []struct {
		cmdName   string
		newCmd    func() *cobra.Command
		shorthand string
		wantFlag  string
	}{
		{cmdName: "submit", newCmd: newSubmitCmd, shorthand: "f", wantFlag: "file-matcher"},
		{cmdName: "submit", newCmd: newSubmitCmd, shorthand: "s", wantFlag: "search"},
		{cmdName: "submit", newCmd: newSubmitCmd, shorthand: "i", wantFlag: "input-file"},
		{cmdName: "upload", newCmd: newUploadCmd, shorthand: "f", wantFlag: "files"},
		{cmdName: "upload", newCmd: newUploadCmd, shorthand: "d", wantFlag: "directory-id"},
	}

	for _, tt := range tests {
		t.Run(tt.cmdName+"/-"+tt.shorthand, func(t *testing.T) {
			flag := tt.newCmd().Flags().ShorthandLookup(tt.shorthand)
			if flag == nil {
				t.Fatalf("-%s shorthand not registered", tt.shorthand)
			}
			if flag.Name != tt.wantFlag {
				t.Errorf("-%s maps to %q, want %q", tt.shorthand, flag.Name, tt.wantFlag)
			}
		})
	}
}

func TestDetectSubcommand_ProfileFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		// --profile takes a value, which must not be read as the subcommand.
		{name: "profile before command", args: []string{"--profile", "default", "upload", "-f", "a.txt"}, want: "upload"},
		{name: "token and profile before command", args: []string{"-p", "TOKEN", "--profile", "eu", "status", "-j", "JOB1"}, want: "status"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectSubcommand(tt.args); got != tt.want {
				t.Errorf("detectSubcommand() = %q, want %q", got, tt.want)
			}
		})
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
