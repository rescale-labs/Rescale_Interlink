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

// TestVerifyDownloadedSize pins the size backstop. It is the only completeness
// check that works on a file with no checksum, and it has to fire because
// everything downstream (the auto-download daemon's poll, the CLI's
// skip-existing modes) reads a file of the expected length as a finished
// download.
func TestVerifyDownloadedSize(t *testing.T) {
	tests := []struct {
		name           string
		actual         int64
		expected       int64
		wantErr        string
		wantQuarantine bool
	}{{
		name:     "the expected size is what landed",
		actual:   1024,
		expected: 1024,
	}, {
		name:     "no expected size means nothing to check",
		actual:   7,
		expected: 0,
	}, {
		name:     "an empty file is reported, not preserved",
		actual:   0,
		expected: 1024,
		wantErr:  "file is empty (0 bytes)",
	}, {
		name:           "a short file fails and is moved aside",
		actual:         512,
		expected:       1024,
		wantErr:        "512 bytes on disk but 1024 bytes were expected",
		wantQuarantine: true,
	}, {
		name:           "a long file fails and is moved aside",
		actual:         2048,
		expected:       1024,
		wantErr:        "2048 bytes on disk but 1024 bytes were expected",
		wantQuarantine: true,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			localPath := filepath.Join(t.TempDir(), "results.dat")
			if err := os.WriteFile(localPath, []byte("downloaded bytes"), 0o644); err != nil {
				t.Fatalf("seed file: %v", err)
			}

			err := verifyDownloadedSize(localPath, tt.actual, tt.expected)

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("verifyDownloadedSize: %v", err)
				}
				if _, statErr := os.Stat(localPath); statErr != nil {
					t.Errorf("an accepted download was disturbed: %v", statErr)
				}
				return
			}

			if err == nil {
				t.Fatalf("verifyDownloadedSize returned nil, want %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}

			_, statErr := os.Stat(localPath)
			if tt.wantQuarantine {
				if !os.IsNotExist(statErr) {
					t.Errorf("the wrong-size file is still at LocalPath (stat error = %v)", statErr)
				}
				if _, quarantineErr := os.Stat(localPath + corruptFileSuffix); quarantineErr != nil {
					t.Errorf("the file was not moved aside: %v", quarantineErr)
				}
			} else if statErr != nil {
				t.Errorf("the file was moved aside unexpectedly: %v", statErr)
			}
		})
	}
}
