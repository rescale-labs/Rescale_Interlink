package compat

import "testing"

func TestExitCodeConstant(t *testing.T) {
	if ExitCodeCompatError != 33 {
		t.Errorf("ExitCodeCompatError = %d, want 33", ExitCodeCompatError)
	}
}

func TestExecuteCompat_SpubPlaceholder(t *testing.T) {
	isolateCredentials(t)
	rootCmd, _ := NewCompatRootCmd()
	rootCmd.SetArgs([]string{"spub", "register"})

	// A placeholder needs no account: it says it is deferred, with no key found.
	err := rootCmd.Execute()
	if err == nil || err.Error() != "compat command 'spub register' is deferred to v5.0.0" {
		t.Errorf("error = %v, want the deferred message", err)
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
