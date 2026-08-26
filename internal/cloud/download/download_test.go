package download

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestQuarantineCorruptFile pins the disposition of a download that fails
// checksum verification in strict mode. The corrupt bytes are full-size, so
// leaving them at LocalPath makes every size-only presence check downstream
// (the auto-download daemon's poll, the CLI's skip-existing modes) report the
// download as finished.
func TestQuarantineCorruptFile(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "results.dat")
	contents := []byte("bytes that failed verification")

	if err := os.WriteFile(localPath, contents, 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	err := quarantineCorruptFile(localPath, errors.New("checksum mismatch: expected SHA-512=aaa, got bbb"))
	if err == nil {
		t.Fatal("quarantineCorruptFile returned nil, want the checksum error")
	}

	if _, statErr := os.Stat(localPath); !os.IsNotExist(statErr) {
		t.Errorf("corrupt file still at LocalPath (stat error = %v)", statErr)
	}

	quarantinePath := localPath + corruptFileSuffix
	got, readErr := os.ReadFile(quarantinePath)
	if readErr != nil {
		t.Fatalf("read quarantined file: %v", readErr)
	}
	if string(got) != string(contents) {
		t.Errorf("quarantined contents = %q, want %q", got, contents)
	}

	msg := err.Error()
	if !strings.Contains(msg, quarantinePath) {
		t.Errorf("error does not name the quarantine path %q: %s", quarantinePath, msg)
	}
	if !strings.Contains(msg, "--skip-checksum") {
		t.Errorf("error dropped the --skip-checksum guidance: %s", msg)
	}
}
