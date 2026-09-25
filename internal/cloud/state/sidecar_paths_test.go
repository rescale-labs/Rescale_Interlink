package state

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A download sidecar names its encrypted temp file. Cleanup removes only the
// path Interlink itself gives that file, whatever the sidecar names.
func TestCleanupExpiredDownloadResumeRemovesOnlyItsOwnTempFile(t *testing.T) {
	victim := filepath.Join(t.TempDir(), "victim.txt")
	if err := os.WriteFile(victim, []byte("keep me"), 0644); err != nil {
		t.Fatal(err)
	}
	localPath := filepath.Join(t.TempDir(), "results.dat.encrypted")
	if err := os.WriteFile(localPath, []byte("partial ciphertext"), 0644); err != nil {
		t.Fatal(err)
	}

	planted := &DownloadResumeState{LocalPath: localPath, EncryptedPath: victim, CreatedAt: time.Now()}
	if err := SaveDownloadState(planted, localPath); err != nil {
		t.Fatal(err)
	}
	CleanupExpiredDownloadResume(planted, localPath, false)
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("cleanup removed the path a sidecar named outside the download: %v", err)
	}
	if DownloadResumeStateExists(localPath) {
		t.Error("cleanup left the sidecar behind")
	}

	own := &DownloadResumeState{LocalPath: localPath, EncryptedPath: localPath, CreatedAt: time.Now()}
	CleanupExpiredDownloadResume(own, localPath, false)
	if _, err := os.Stat(localPath); !os.IsNotExist(err) {
		t.Errorf("cleanup kept the download's own temp file: %v", err)
	}
}

// Sidecars were written through a fixed "<sidecar>.tmp" name with
// os.WriteFile, whose mode applies only when it creates the file: a stale temp
// file kept its looser mode. (A link at that name is refused; see
// paths.TestWritePrivateFileReclaimsWhatAKilledWriteLeft.)
func TestSidecarsIgnoreWhateverIsAtTheOldTempName(t *testing.T) {
	type sidecar struct {
		name string
		save func(localPath string) error
	}
	sidecars := []sidecar{
		{".upload.resume", func(p string) error {
			return SaveUploadState(&UploadResumeState{LocalPath: p, EncryptionKey: "FAKEKEY"}, p)
		}},
		{".download.resume", func(p string) error {
			return SaveDownloadState(&DownloadResumeState{LocalPath: p, MasterKey: "FAKEKEY"}, p)
		}},
	}

	for _, sc := range sidecars {
		t.Run(sc.name+" stale temp", func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("Windows keeps no Unix permission bits")
			}
			localPath := filepath.Join(t.TempDir(), "data.bin")
			tmp := localPath + sc.name + ".tmp"
			if err := os.WriteFile(tmp, nil, 0666); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(tmp, 0666); err != nil {
				t.Fatal(err)
			}
			if err := sc.save(localPath); err != nil {
				t.Fatalf("save: %v", err)
			}
			info, err := os.Stat(localPath + sc.name)
			if err != nil {
				t.Fatal(err)
			}
			if perm := info.Mode().Perm(); perm != 0600 {
				t.Errorf("sidecar mode = %o, want 600", perm)
			}
		})
	}
}

// A sidecar name at the 255-byte limit leaves no room for "<sidecar>.tmp", and
// a staging name longer than the sidecar's could not be created, so the
// download or upload lost its resume state in silence. The staging name falls
// back to a short one there.
func TestSidecarsFitAtTheNameLengthLimit(t *testing.T) {
	for _, first := range []string{"a", "é"} { // 1 and 2 bytes
		for suffix, save := range map[string]func(localPath string) error{
			".upload.resume": func(p string) error {
				return SaveUploadState(&UploadResumeState{LocalPath: p, EncryptionKey: "FAKEKEY"}, p)
			},
			".download.resume": func(p string) error {
				return SaveDownloadState(&DownloadResumeState{LocalPath: p, MasterKey: "FAKEKEY"}, p)
			},
		} {
			name := first + strings.Repeat("a", 255-len(first)-len(suffix))
			localPath := filepath.Join(t.TempDir(), name)
			if err := save(localPath); err != nil {
				t.Errorf("%s (%d bytes): %v", suffix, len(name+suffix), err)
			} else if _, err := os.Stat(localPath + suffix); err != nil {
				t.Errorf("%s (%d bytes): %v", suffix, len(name+suffix), err)
			}
		}
	}
}

// A record names its file by the path it was written with, which older
// versions left relative. Either form of this download's path matches it; any
// other file does not.
func TestValidateDownloadStateComparesAbsolutePaths(t *testing.T) {
	t.Chdir(t.TempDir())
	abs, err := filepath.Abs("a.encrypted")
	if err != nil {
		t.Fatal(err)
	}
	for recorded, want := range map[string]bool{
		"a.encrypted": true, "./a.encrypted": true, abs: true,
		"b.encrypted": false, filepath.Join(t.TempDir(), "a.encrypted"): false,
	} {
		if got := ValidateDownloadState(&DownloadResumeState{LocalPath: recorded, CreatedAt: time.Now()}, abs) == nil; got != want {
			t.Errorf("a record for %q matches %s: %v, want %v", recorded, abs, got, want)
		}
	}
}
