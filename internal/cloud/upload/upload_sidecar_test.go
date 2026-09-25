package upload

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/rescale/rescale-int/internal/cloud/state"
)

// An upload's resume sidecar is data on disk. Abandoning it removes only the
// names CreateEncryptedTempFile gives this source's ciphertext, in the temp
// folder or beside the source, whatever the sidecar names.
func TestAbandonPreEncryptStateRemovesOnlyItsOwnCiphertext(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	localPath, _ := writeStreamingSource(t, 64)
	own, err := CreateEncryptedTempFile(localPath)
	if err != nil {
		t.Fatal(err)
	}
	beside := filepath.Join(filepath.Dir(localPath), ".streamed.dat-12345.encrypted")
	other := filepath.Join(t.TempDir(), "results.encrypted")
	for path, owned := range map[string]bool{
		own:                                 true,
		beside:                              true,
		other:                               false,
		localPath + ".encrypted":            false,
		filepath.Join(tmp, "x-1.encrypted"): false,
	} {
		if err := os.WriteFile(path, []byte("ciphertext"), 0o600); err != nil {
			t.Fatal(err)
		}
		saved := &state.UploadResumeState{LocalPath: localPath, EncryptedPath: path}
		if err := state.SaveUploadState(saved, localPath); err != nil {
			t.Fatal(err)
		}
		if err := abandonPreEncryptState(saved, localPath); err != nil {
			t.Fatalf("abandonPreEncryptState: %v", err)
		}
		if _, err := os.Stat(path); os.IsNotExist(err) != owned {
			t.Errorf("%s: removed = %v, want %v", path, os.IsNotExist(err), owned)
		}
		if state.UploadResumeStateExists(localPath) {
			t.Errorf("%s: the sidecar was left behind", path)
		}
	}
}

// A relaunch under another temp folder resumes the pre-encrypt upload it
// interrupted: the folder the ciphertext was made in decides only whether
// abandoning a checkpoint may delete it.
func TestUploadPreEncryptResumesUnderAnotherTempFolder(t *testing.T) {
	setTemp := func() {
		dir := t.TempDir()
		t.Setenv("TMPDIR", dir)
		t.Setenv("TMP", dir)
		t.Setenv("TEMP", dir)
	}
	setTemp()
	source, data := writeStreamingSource(t, 300)
	fake := &resumableFakeUploader{partSize: 64, failAfterParts: 2}
	params := UploadParams{LocalPath: source, PreEncrypt: true}
	if _, err := uploadPreEncrypt(context.Background(), fake, params, int64(len(data))); err == nil {
		t.Fatal("the first attempt was expected to fail")
	}
	setTemp()
	fake.failAfterParts = 0
	if _, err := uploadPreEncrypt(context.Background(), fake, params, int64(len(data))); err != nil {
		t.Fatalf("the relaunch: %v", err)
	}
	if first, second := fake.attempts[0], fake.attempts[len(fake.attempts)-1]; second.encryptedPath != first.encryptedPath || second.resumedFrom != 2 {
		t.Errorf("the relaunch used %s from part %d, want %s resumed after the 2 parts already sent", second.encryptedPath, second.resumedFrom, first.encryptedPath)
	}
}
