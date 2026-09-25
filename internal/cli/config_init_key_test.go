package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// At a terminal the API key is read without echo, so it never reaches the
// scrollback or a screen share. (A piped key is covered by
// config_init_path_test.go.)
func TestConfigInitReadsTheAPIKeyWithoutEcho(t *testing.T) {
	orig := readHiddenFn
	t.Cleanup(func() { readHiddenFn = orig })

	hidden := false
	readHiddenFn = func(io.Reader) (string, bool, error) {
		hidden = true
		return "FAKEKEY-typed", true, nil
	}
	// The key is not among the piped answers: the terminal supplied it.
	path, err := runConfigInitFrom(t, strings.NewReader("\n7\n\n\n\n"))
	if err != nil {
		t.Fatalf("config init: %v", err)
	}
	if !hidden {
		t.Error("the API key was not read through the no-echo reader")
	}
	if got, _ := os.ReadFile(filepath.Join(filepath.Dir(path), "token")); strings.TrimSpace(string(got)) != "FAKEKEY-typed" {
		t.Errorf("token file holds %q, want the key typed at the terminal", got)
	}
}
