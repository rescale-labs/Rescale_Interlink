//go:build !windows

package wailsapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The folder half of pre-flight must agree with what the daemon will actually
// be able to do. A stat-only check green-lit read-only folders, and every
// download then failed after the user had been told setup was fine.
//
// Only the folder verdict is asserted: the API-key half reads the real user's
// on-disk config, which a unit test has no business depending on.
func TestValidateAutoDownloadPreFlight_FolderVerdicts(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}

	base := t.TempDir()

	writable := filepath.Join(base, "writable")
	if err := os.MkdirAll(writable, 0o755); err != nil {
		t.Fatalf("mkdir writable: %v", err)
	}

	readOnly := filepath.Join(base, "readonly")
	if err := os.MkdirAll(readOnly, 0o500); err != nil {
		t.Fatalf("mkdir readonly: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o755) })

	regularFile := filepath.Join(base, "a-file")
	if err := os.WriteFile(regularFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	unwritableParent := filepath.Join(readOnly, "child")

	tests := []struct {
		name        string
		folder      string
		wantOK      bool
		wantErrPart string
	}{
		{
			name:   "existing writable directory",
			folder: writable,
			wantOK: true,
		},
		{
			name:   "missing directory under a writable parent is created",
			folder: filepath.Join(writable, "nested", "deep"),
			wantOK: true,
		},
		{
			name:        "existing directory that cannot be written to",
			folder:      readOnly,
			wantOK:      false,
			wantErrPart: "Cannot write to folder",
		},
		{
			name:        "path is a regular file",
			folder:      regularFile,
			wantOK:      false,
			wantErrPart: "Cannot create folder",
		},
		{
			name:        "missing directory under an unwritable parent",
			folder:      unwritableParent,
			wantOK:      false,
			wantErrPart: "Cannot create folder",
		},
	}

	app := &App{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := app.ValidateAutoDownloadPreFlight(tc.folder)
			if got.FolderOK != tc.wantOK {
				t.Fatalf("FolderOK = %v, want %v (error: %q)", got.FolderOK, tc.wantOK, got.FolderError)
			}
			if tc.wantOK {
				if got.FolderError != "" {
					t.Errorf("FolderError = %q, want empty", got.FolderError)
				}
				return
			}
			if !strings.Contains(got.FolderError, tc.wantErrPart) {
				t.Errorf("FolderError = %q, want it to contain %q", got.FolderError, tc.wantErrPart)
			}
		})
	}
}
