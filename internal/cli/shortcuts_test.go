package cli

import (
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/constants"
	"github.com/spf13/cobra"
)

// TestUploadShortcut tests the upload shortcut command
func TestUploadShortcut(t *testing.T) {
	cmd := newUploadShortcut()
	if cmd == nil {
		t.Fatal("newUploadShortcut() returned nil")
	}

	if cmd.Use != "upload <file> [file...]" {
		t.Errorf("Expected Use='upload <file> [file...]', got '%s'", cmd.Use)
	}

	if cmd.Short == "" {
		t.Error("Short description is empty")
	}

	if cmd.RunE == nil {
		t.Error("RunE function is nil")
	}

	// Check for flags
	folderFlag := cmd.Flags().Lookup("folder-id")
	if folderFlag == nil {
		t.Error("--folder-id flag not found")
	}

	maxConcurrentFlag := cmd.Flags().Lookup("max-concurrent")
	if maxConcurrentFlag == nil {
		t.Error("--max-concurrent flag not found")
	}
}

// TestDownloadShortcut tests the download shortcut command
func TestDownloadShortcut(t *testing.T) {
	cmd := newDownloadShortcut()
	if cmd == nil {
		t.Fatal("newDownloadShortcut() returned nil")
	}

	if cmd.Use != "download <file-id> [file-id...]" {
		t.Errorf("Expected Use='download <file-id> [file-id...]', got '%s'", cmd.Use)
	}

	if cmd.Short == "" {
		t.Error("Short description is empty")
	}

	if cmd.RunE == nil {
		t.Error("RunE function is nil")
	}

	// Check for flags
	outdirFlag := cmd.Flags().Lookup("outdir")
	if outdirFlag == nil {
		t.Error("--outdir flag not found")
	}

	maxConcurrentFlag := cmd.Flags().Lookup("max-concurrent")
	if maxConcurrentFlag == nil {
		t.Error("--max-concurrent flag not found")
	}
}

// TestLsShortcut tests the ls shortcut command
func TestLsShortcut(t *testing.T) {
	cmd := newLsShortcut()
	if cmd == nil {
		t.Fatal("newLsShortcut() returned nil")
	}

	if cmd.Use != "ls" {
		t.Errorf("Expected Use='ls', got '%s'", cmd.Use)
	}

	if cmd.Short == "" {
		t.Error("Short description is empty")
	}

	if cmd.RunE == nil {
		t.Error("RunE function is nil")
	}

	// Check for flags
	limitFlag := cmd.Flags().Lookup("limit")
	if limitFlag == nil {
		t.Error("--limit flag not found")
	}

}

// TestShortcutCommands tests that all shortcut commands exist
func TestShortcutCommands(t *testing.T) {
	shortcuts := []struct {
		name     string
		createFn func() *cobra.Command
	}{
		{"upload", newUploadShortcut},
		{"download", newDownloadShortcut},
		{"ls", newLsShortcut},
	}

	for _, sc := range shortcuts {
		t.Run(sc.name, func(t *testing.T) {
			cmd := sc.createFn()
			if cmd == nil {
				t.Fatalf("Shortcut command '%s' creation returned nil", sc.name)
			}

			if cmd.RunE == nil {
				t.Errorf("Shortcut command '%s' has no RunE function", sc.name)
			}

			if cmd.Short == "" {
				t.Errorf("Shortcut command '%s' has empty Short description", sc.name)
			}

			if cmd.Long == "" {
				t.Errorf("Shortcut command '%s' has empty Long description", sc.name)
			}
		})
	}
}

// TestAddShortcuts tests that AddShortcuts adds commands to root
func TestAddShortcuts(t *testing.T) {
	// Create a new root command
	rootCmd := NewRootCmd()

	// Add shortcuts
	AddShortcuts(rootCmd)

	// Check that shortcuts were added
	expectedShortcuts := []string{"upload", "download", "ls"}
	foundShortcuts := make(map[string]bool)

	for _, cmd := range rootCmd.Commands() {
		foundShortcuts[cmd.Name()] = true
	}

	for _, expected := range expectedShortcuts {
		if !foundShortcuts[expected] {
			t.Errorf("Shortcut command '%s' not found in root command", expected)
		}
	}
}

// TestDownloadShortcutMaxConcurrentRange verifies Bug #5 fix:
// Download shortcut uses MaxMaxConcurrent (20), not hardcoded 10.
func TestDownloadShortcutMaxConcurrentRange(t *testing.T) {
	tests := []struct {
		name        string
		value       string
		wantRangeErr bool
	}{
		{"below_min", "0", true},
		{"at_min", "1", false},
		{"default_value", "5", false},
		{"at_old_max_10", "10", false},
		{"above_old_max_15", "15", false},
		{"at_new_max_20", "20", false},
		{"above_max_21", "21", true},
		{"way_above_max", "100", true},
		{"negative", "-1", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newDownloadShortcut()
			cmd.Flags().Set("max-concurrent", tt.value)
			// RunE will validate max-concurrent first, then fail on getAPIClient().
			// We only care whether the range error occurs.
			err := cmd.RunE(cmd, []string{"fake-file-id"})
			if err == nil {
				t.Fatal("expected error (at least from missing API client), got nil")
			}
			isRangeErr := strings.Contains(err.Error(), "must be between")
			if tt.wantRangeErr && !isRangeErr {
				t.Errorf("expected range validation error, got: %v", err)
			}
			if !tt.wantRangeErr && isRangeErr {
				t.Errorf("did not expect range error for value %s, got: %v", tt.value, err)
			}
		})
	}

	// Verify the actual constant values used
	if constants.MinMaxConcurrent != 1 {
		t.Errorf("MinMaxConcurrent = %d, want 1", constants.MinMaxConcurrent)
	}
	if constants.MaxMaxConcurrent != 20 {
		t.Errorf("MaxMaxConcurrent = %d, want 20 (was 10 before Bug #5 fix)", constants.MaxMaxConcurrent)
	}
}

// TestUploadShortcutMaxConcurrentRange verifies upload shortcut also uses MaxMaxConcurrent.
func TestUploadShortcutMaxConcurrentRange(t *testing.T) {
	tests := []struct {
		name        string
		value       string
		wantRangeErr bool
	}{
		{"below_min", "0", true},
		{"at_max_20", "20", false},
		{"above_max_21", "21", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newUploadShortcut()
			cmd.Flags().Set("max-concurrent", tt.value)
			err := cmd.RunE(cmd, []string{"fake-file"})
			if err == nil {
				t.Fatal("expected error (at least from missing API client), got nil")
			}
			isRangeErr := strings.Contains(err.Error(), "must be between")
			if tt.wantRangeErr && !isRangeErr {
				t.Errorf("expected range validation error, got: %v", err)
			}
			if !tt.wantRangeErr && isRangeErr {
				t.Errorf("did not expect range error for value %s, got: %v", tt.value, err)
			}
		})
	}
}
