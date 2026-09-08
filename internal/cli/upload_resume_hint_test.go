package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The hint printed when an upload is interrupted told the user the opposite of
// what the code does: the partial upload is not discarded, and re-running the
// same command continues from the parts already accepted. Following the old
// text — deleting the file's resume record, or starting somewhere else —
// threw away the transferred bytes it promised were already gone.
func TestInterruptedUploadHint(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "payload.dat")
	if err := os.WriteFile(source, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	if got := interruptedUploadHint(source); got != "" {
		t.Errorf("hint = %q with no resume record, want nothing", got)
	}

	if err := os.WriteFile(source+".upload.resume", []byte("{}"), 0o600); err != nil {
		t.Fatalf("write resume record: %v", err)
	}

	got := interruptedUploadHint(source)
	if got == "" {
		t.Fatal("no hint printed for an upload that left a resume record")
	}
	for _, want := range []string{
		"Re-running the same command",
		"can resume",
		"completed parts",
		"7 days",
		"unchanged",
		"still usable",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("hint %q does not mention %q", got, want)
		}
	}
	for _, unwanted := range []string{
		"from the beginning",
		"discarded",
	} {
		if strings.Contains(got, unwanted) {
			t.Errorf("hint %q still claims %q", got, unwanted)
		}
	}
}
