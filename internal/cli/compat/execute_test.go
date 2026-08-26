package compat

import (
	"strings"
	"testing"
)

func TestExitCodeConstant(t *testing.T) {
	if ExitCodeCompatError != 33 {
		t.Errorf("ExitCodeCompatError = %d, want 33", ExitCodeCompatError)
	}
}

func TestExecuteCompat_SpubPlaceholder(t *testing.T) {
	rootCmd, _ := NewCompatRootCmd()
	rootCmd.SetArgs([]string{"spub", "register"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error from spub placeholder, got nil")
	}
	// May fail on auth or on the placeholder message — both are valid
	// The important thing is it doesn't succeed silently
	errMsg := err.Error()
	if errMsg != "compat command 'spub register' is deferred to v5.0.0" &&
		!strings.Contains(errMsg, "API key") {
		t.Errorf("error = %q, want deferred message or auth error", errMsg)
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
