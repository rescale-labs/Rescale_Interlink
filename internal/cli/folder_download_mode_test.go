package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/cloud/download"
	"github.com/rescale/rescale-int/internal/reporting"
)

// Without a terminal, folders download-dir needs a conflict flag only when the
// destination already holds something to conflict with, and its refusal is the
// user's to fix: no error report. A dry run needs no flag, says the download
// would refuse where it would, and ends with its own summary rather than a
// download summary of folders it did not create.
func TestFoldersDownloadDirWithoutTerminal(t *testing.T) {
	if IsTerminal() {
		t.Skip("needs a non-interactive stdin")
	}
	home := t.TempDir()
	for _, env := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "LOCALAPPDATA"} {
		t.Setenv(env, home)
	}
	useFakeLibrary(t, newFakeLibrary(t))
	write := func(_ context.Context, p download.DownloadParams) error {
		return os.WriteFile(p.LocalPath, []byte("new"), 0o600)
	}
	holdsFile := func(t *testing.T, dest string) {
		if err := os.MkdirAll(dest, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dest, "kept.txt"), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	empty := func(t *testing.T, dest string) {
		if err := os.MkdirAll(dest, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name    string
		dest    func(*testing.T, string) // what the destination holds; nil: it does not exist
		args    []string
		refused bool
		mode    string // the conflict mode a dry run reports; <dest> stands for the destination
	}{
		{"a new destination", nil, nil, false, ""},
		{"an empty destination", empty, nil, false, ""},
		{"a destination holding a file", holdsFile, nil, true, ""},
		{"a dry run", holdsFile, []string{"--dry-run"}, false,
			"Conflict mode: NONE (the download would refuse: conflict handling mode required in non-interactive mode: <dest> is not empty; use --skip, --overwrite, or --merge)\n"},
		{"a dry run into a new destination", nil, []string{"--dry-run"}, false, "Conflict mode: MERGE"},
		{"a dry run with a mode", holdsFile, []string{"--dry-run", "--merge"}, false, "Conflict mode: MERGE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := t.TempDir()
			dest := filepath.Join(out, libraryRoot)
			if tc.dest != nil {
				tc.dest(t, dest)
			}
			var printed string
			var err error
			captureStderr(t, func() {
				printed, err = runWithCancel(t, newFoldersCmd(), write,
					append([]string{"download-dir", libraryRoot, "--outdir", out, "--max-concurrent", "1"}, tc.args...)...)
			})
			if tc.refused {
				if err == nil || !strings.Contains(err.Error(), "use --skip, --overwrite, or --merge") {
					t.Fatalf("got %v, want the refusal to name the flags", err)
				}
				if saved := reporting.HandleCLIError(err, "cli", "rescale-int folders download-dir", ""); saved != "" {
					t.Errorf("the refusal saved an error report to %s", saved)
				}
				return
			}
			if err != nil {
				t.Fatalf("folders download-dir %v: %v\n%s", tc.args, err, printed)
			}
			dryRun := len(tc.args) > 0
			if got := strings.Contains(printed, "Download Summary") || strings.Contains(printed, "Folders created:"); got == dryRun {
				t.Errorf("printed a download summary: %v, want %v\n%s", got, !dryRun, printed)
			}
			if want := strings.ReplaceAll(tc.mode, "<dest>", dest); !strings.Contains(printed, want) {
				t.Errorf("printed\n%s\nwant %q", printed, want)
			}
			if _, statErr := os.Stat(filepath.Join(dest, "sub1", "f2.dat")); (statErr == nil) == dryRun {
				t.Errorf("sub1/f2.dat downloaded: %v, want %v", statErr == nil, !dryRun)
			}
		})
	}
}
