package compat

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Compat mode prints sync's download and status errors itself, redacted as
// every other output is.
func TestCompatOutputRedactsCredentials(t *testing.T) {
	stdout, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	defer func(orig *os.File) { os.Stdout = orig }(os.Stdout)
	os.Stdout = stdout

	cause := errors.New(`Put "https://a.blob.core.windows.net/c/f?comp=block&sig=SECRET": EOF`)
	cb := compatSyncCallbacks(&CompatContext{}, "sync error")
	cb.OnDownloadPass("job1", cause)
	cb.OnError("job1", cause)

	printed, _ := os.ReadFile(stdout.Name())
	if strings.Contains(string(printed), "SECRET") || strings.Count(string(printed), "sig=REDACTED") != 2 {
		t.Errorf("compat printed %q, want the signature redacted on both lines", printed)
	}
}

// RESCALE_DEBUG keeps the standard logger's transfer diagnostics, which quote
// storage URLs.
func TestCompatDebugLogRedactsCredentials(t *testing.T) {
	t.Setenv("RESCALE_DEBUG", "1")
	stderr, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	defer func(orig *os.File, args []string) { os.Stderr, os.Args = orig, args; log.SetOutput(orig) }(os.Stderr, os.Args)
	os.Stderr, os.Args = stderr, []string{"rescale-int", "--help"}

	ExecuteCompat()
	log.Printf(`[BATCH] Put "https://a.blob.core.windows.net/c/f?comp=block&sig=SECRET": EOF`)

	printed, _ := os.ReadFile(stderr.Name())
	if strings.Contains(string(printed), "SECRET") || !strings.Contains(string(printed), "sig=REDACTED") {
		t.Errorf("compat logged %q, want the signature redacted", printed)
	}
}
