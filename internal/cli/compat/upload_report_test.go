package compat

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/models"
)

// The upload report carries every file's encryption key, so it is written
// readable by the owner alone, whatever mode an earlier report at the path had.
func TestUploadReportIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows keeps no Unix permission bits")
	}
	files := []*models.CloudFile{{ID: "FAKEID", Name: "data.bin", EncodedEncryptionKey: "FAKEKEY"}}
	for _, existing := range []bool{false, true} {
		report := filepath.Join(t.TempDir(), "report.json")
		if existing {
			if err := os.WriteFile(report, []byte("[]"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		if err := writeUploadReport(report, files); err != nil {
			t.Fatalf("writeUploadReport: %v", err)
		}
		info, err := os.Stat(report)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0600 {
			t.Errorf("existing=%v: report mode = %o, want 600", existing, perm)
		}
	}
}

// A link or a FIFO at the report path is refused and left alone, and so is a
// link's target. An earlier report is replaced, not rewritten, so a second name
// for it keeps the old contents.
func TestUploadReportRefusesALinkOrAFIFO(t *testing.T) {
	files := []*models.CloudFile{{ID: "FAKEID", Name: "data.bin", EncodedEncryptionKey: "FAKEKEY"}}
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.txt")
	if err := os.WriteFile(victim, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linked.json")
	if err := os.Symlink(victim, link); err != nil {
		t.Skipf("cannot make a symbolic link here: %v", err)
	}
	before, err := os.Stat(victim)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeUploadReport(link, files); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Errorf("report through a link: err = %v, want a refusal", err)
	}
	if info, err := os.Stat(victim); err != nil || info.Mode() != before.Mode() {
		t.Errorf("the link's target mode is now %v, was %v (%v)", info.Mode(), before.Mode(), err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep me" {
		t.Errorf("the link's target now holds %q", got)
	}

	// No FIFOs to test with on Windows; Git Bash's mkfifo makes none Go can see.
	fifo := filepath.Join(dir, "fifo.json")
	if runtime.GOOS != "windows" && exec.Command("mkfifo", fifo).Run() == nil {
		done := make(chan error, 1)
		go func() { done <- writeUploadReport(fifo, files) }()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "refusing") {
				t.Errorf("report into a FIFO: err = %v, want a refusal", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("the report blocked opening a FIFO")
		}
	}

	report, second := filepath.Join(dir, "report.json"), filepath.Join(dir, "second.json")
	if err := os.WriteFile(report, []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(report, second); err != nil {
		t.Skipf("cannot make a hard link here: %v", err)
	}
	if err := writeUploadReport(report, files); err != nil {
		t.Fatalf("writeUploadReport: %v", err)
	}
	if got, _ := os.ReadFile(second); string(got) != "[]" {
		t.Errorf("a second name for the old report now holds %q", got)
	}
}
