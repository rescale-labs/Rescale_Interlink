package compat

import (
	"testing"
)

func TestExitCodeConstant(t *testing.T) {
	if ExitCodeCompatError != 33 {
		t.Errorf("ExitCodeCompatError = %d, want 33", ExitCodeCompatError)
	}
}

func TestExecuteCompat_VersionExitsZero(t *testing.T) {
	rootCmd, _ := NewCompatRootCmd()
	rootCmd.SetArgs([]string{"--version"})

	err := rootCmd.Execute()
	if err != nil {
		t.Errorf("--version returned error: %v", err)
	}
}

func TestExecuteCompat_SyncRequiresJobID(t *testing.T) {
	rootCmd, _ := NewCompatRootCmd()

	rootCmd.SetArgs([]string{"sync"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing --job-id, got nil")
	}
	if err.Error() != "--job-id is required" {
		t.Errorf("error = %q, want '--job-id is required'", err.Error())
	}
}

func TestExecuteCompat_SpubPlaceholder(t *testing.T) {
	rootCmd, _ := NewCompatRootCmd()

	rootCmd.SetArgs([]string{"spub", "register"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error from spub placeholder, got nil")
	}
	if err.Error() != "compat command 'spub register' is deferred to v5.0.0" {
		t.Errorf("error = %q, want deferred message", err.Error())
	}
}

func TestExecuteCompat_HelpExitsZero(t *testing.T) {
	rootCmd, _ := NewCompatRootCmd()
	rootCmd.SetArgs([]string{"--help"})

	err := rootCmd.Execute()
	if err != nil {
		t.Errorf("--help returned error: %v", err)
	}
}

func TestExecuteCompat_UnknownCommand(t *testing.T) {
	rootCmd, _ := NewCompatRootCmd()
	rootCmd.SetArgs([]string{"nonexistent"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for unknown command, got nil")
	}
}
