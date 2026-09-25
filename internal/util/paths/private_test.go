package paths

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A process killed between staging a private file and renaming it leaves the
// staging file behind, under the unique or the fixed "<file>.tmp" name. The
// next write reclaims either, so interruptions do not pile files up. A link at
// the staging name is refused and left alone.
func TestWritePrivateFileReclaimsWhatAKilledWriteLeft(t *testing.T) {
	if path := os.Getenv("INTERLINK_TEST_KILLED_WRITE"); path != "" {
		beforeRename = func() { os.Exit(3) }
		_ = WritePrivateFile(path, []byte("first"))
		return
	}
	dir := t.TempDir()
	count := func() int { entries, _ := os.ReadDir(dir); return len(entries) }
	path := filepath.Join(dir, "data.bin.upload.resume")
	child := exec.Command(os.Args[0], "-test.run=^TestWritePrivateFileReclaimsWhatAKilledWriteLeft$")
	child.Env = append(os.Environ(), "INTERLINK_TEST_KILLED_WRITE="+path)
	if err := child.Run(); child.ProcessState == nil || child.ProcessState.ExitCode() != 3 {
		t.Fatalf("the first write was not killed before its rename: %v", err)
	}
	if count() != 1 {
		t.Fatalf("the killed write left %d files, want its staging file", count())
	}
	if err := WritePrivateFile(path, []byte("second")); err != nil {
		t.Fatalf("the next write: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "second" || count() != 1 {
		t.Errorf("after the next write the file holds %q and the folder %d files, want only the file", got, count())
	}

	base := filepath.Join(dir, "other.upload.resume")
	if err := os.WriteFile(base+".tmp", []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivateFile(base, []byte("x")); err != nil || count() != 2 {
		t.Errorf("with the base's leftover there: %v, and the folder holds %d files, want the two files", err, count())
	}

	victim := filepath.Join(t.TempDir(), "victim.txt")
	if err := os.WriteFile(victim, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(dir, "linked.upload.resume")
	if err := os.Symlink(victim, linked+".tmp"); err != nil {
		t.Skipf("cannot make a symbolic link here: %v", err)
	}
	if err := WritePrivateFile(linked, []byte("x")); err == nil {
		t.Error("wrote with a link at the staging name, want a refusal")
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep me" {
		t.Errorf("the link's target now holds %q", got)
	}
	if info, err := os.Lstat(linked + ".tmp"); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was not left in place: %v", err)
	}
}
