package cli

import (
	"strings"
	"testing"
)

// TestJobsIDAliasParses covers the documented --id alias. It was unusable:
// --job-id and --id are two pflag entries sharing one variable, and cobra's
// MarkFlagRequired("job-id") rejected "--id X" as a missing required flag.
func TestJobsIDAliasParses(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "get with --job-id", args: []string{"get", "--job-id", "abc123"}},
		{name: "get with --id", args: []string{"get", "--id", "abc123"}},
		{name: "get with -j", args: []string{"get", "-j", "abc123"}},
		{name: "delete with --id", args: []string{"delete", "--id", "abc123", "--confirm"}},
		{name: "download with --id", args: []string{"download", "--id", "abc123"}},
		{name: "get with neither", args: []string{"get"}, wantErr: "is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Parse only: stop before RunE, which would need an API client.
			cmd := newJobsCmd()
			sub, flags, err := cmd.Find(tt.args)
			if err != nil {
				t.Fatalf("Find(%v): %v", tt.args, err)
			}
			if err := sub.ParseFlags(flags); err != nil {
				t.Fatalf("ParseFlags(%v): %v", flags, err)
			}

			// Cobra's own required-flag validation must not reject the alias.
			if err := sub.ValidateRequiredFlags(); err != nil {
				t.Fatalf("ValidateRequiredFlags: %v", err)
			}

			got := sub.Flags().Lookup("job-id")
			if got == nil {
				t.Fatal("no --job-id flag on the command")
			}
			if tt.wantErr != "" {
				if got.Value.String() != "" && got.Value.String() != "[]" {
					t.Errorf("expected no job id, got %q", got.Value.String())
				}
				return
			}
			if !strings.Contains(got.Value.String(), "abc123") {
				t.Errorf("--job-id holds %q, want it to carry abc123", got.Value.String())
			}
		})
	}
}

// TestFilesDeleteRejectsPositionalArgs covers silently discarded IDs:
// "files delete --fileid A B C" deleted A and ignored B and C.
func TestFilesDeleteRejectsPositionalArgs(t *testing.T) {
	cmd := newFilesCmd()
	sub, _, err := cmd.Find([]string{"delete"})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if sub.Args == nil {
		t.Fatal("files delete accepts any positional args")
	}
	if err := sub.Args(sub, []string{"B", "C"}); err == nil {
		t.Error("expected positional arguments to be rejected")
	}
	if err := sub.Args(sub, nil); err != nil {
		t.Errorf("no positional arguments must be fine, got %v", err)
	}
}

// TestConfirmDestructiveWithoutTerminal verifies that a destructive command with
// no terminal fails loudly instead of reading EOF, printing "Cancelled" and
// exiting 0 — which left scripts unable to tell a refusal from a completion.
func TestConfirmDestructiveWithoutTerminal(t *testing.T) {
	if IsTerminal() {
		t.Skip("test needs a non-interactive stdin")
	}

	ok, err := confirmDestructive("deleting files", "--confirm")
	if ok {
		t.Error("must not confirm without a terminal")
	}
	if err == nil {
		t.Fatal("expected an error explaining that --confirm is needed")
	}
	if !strings.Contains(err.Error(), "--confirm") {
		t.Errorf("error should name the flag to use: %v", err)
	}
}

// TestPromptsRefuseWithoutTerminal verifies every interactive prompt reachable
// from a batch worker reports why it cannot ask, instead of surfacing "EOF".
func TestPromptsRefuseWithoutTerminal(t *testing.T) {
	if IsTerminal() {
		t.Skip("test needs a non-interactive stdin")
	}

	checks := []struct {
		name string
		call func() error
	}{
		{"folder conflict", func() error { _, err := promptFolderConflict("data"); return err }},
		{"file conflict", func() error { _, err := promptFileConflict("a.txt", "data"); return err }},
		{"download conflict", func() error { _, err := promptDownloadConflict("a.txt", "/tmp/a.txt"); return err }},
		{"folder download conflict", func() error {
			_, err := promptFolderDownloadConflict("data", "/tmp/data")
			return err
		}},
		{"folder download mode", func() error { _, err := promptFolderDownloadMode(); return err }},
		{"upload duplicate mode", func() error { _, err := promptUploadDuplicateMode(); return err }},
		{"upload conflict", func() error { _, err := promptUploadConflict("a.txt", ""); return err }},
		{"upload error", func() error { _, err := promptUploadError("a.txt", errFake); return err }},
	}

	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			err := c.call()
			if err == nil {
				t.Fatal("expected an error without a terminal")
			}
			if !strings.Contains(err.Error(), "no interactive terminal") {
				t.Errorf("error should explain the missing terminal: %v", err)
			}
		})
	}
}

var errFake = fakeErr("upload failed")

type fakeErr string

func (e fakeErr) Error() string { return string(e) }
