package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// inItsOwnConfigDirectory points the per-user configuration directory at one the
// test owns, on every platform, so no identifier of whoever runs the suite is
// read, written or removed.
func inItsOwnConfigDirectory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"HOME", "USERPROFILE", "LOCALAPPDATA"} {
		t.Setenv(name, dir)
	}
	if getConfigDir() == "" {
		t.Skip("this platform resolves no configuration directory from the environment")
	}
	return getConfigDir()
}

// TestInstallID_IsWrittenOnceAndReadBack pins the property the identifier
// exists for: it is the same string for every process of one installation, and
// it goes on being the same one after the process that made it is gone. An
// identifier that were regenerated per call would name a different installation
// on every acquisition of an upload lock, and nothing would ever be reclaimed.
func TestInstallID_IsWrittenOnceAndReadBack(t *testing.T) {
	dir := inItsOwnConfigDirectory(t)

	first, err := InstallID()
	if err != nil {
		t.Fatalf("InstallID failed: %v", err)
	}
	if first == "" {
		t.Fatal("InstallID returned no identifier and no error")
	}

	again, err := InstallID()
	if err != nil {
		t.Fatalf("second InstallID failed: %v", err)
	}
	if again != first {
		t.Errorf("the second call reports installation %q, want the one already written %q", again, first)
	}

	path := filepath.Join(dir, installIDFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != first {
		t.Errorf("%s holds %q, want the identifier that was reported %q", path, data, first)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	// The identifier decides whose upload locks may be reclaimed, so no other
	// account may write it.
	if perm := info.Mode().Perm(); perm&0077 != 0 {
		t.Errorf("%s is mode %#o, want nothing for group or other", path, perm)
	}
}

// TestInstallID_TellsTwoInstallationsApart is the whole point of the file: the
// hostname and the uid can be the same on two machines, and this cannot.
func TestInstallID_TellsTwoInstallationsApart(t *testing.T) {
	inItsOwnConfigDirectory(t)
	first, err := InstallID()
	if err != nil {
		t.Fatalf("InstallID failed: %v", err)
	}

	inItsOwnConfigDirectory(t)
	second, err := InstallID()
	if err != nil {
		t.Fatalf("InstallID failed in the second installation: %v", err)
	}
	if second == first {
		t.Errorf("two installations report one identifier %q", first)
	}
}

// TestInstallID_ConcurrentCreatorsAgree pins the O_EXCL race. Two processes
// starting an upload at once both find no file; if each wrote its own, one
// installation would answer to two identifiers and neither could reclaim what
// the other left.
func TestInstallID_ConcurrentCreatorsAgree(t *testing.T) {
	inItsOwnConfigDirectory(t)

	const creators = 8
	ids := make([]string, creators)
	errs := make([]error, creators)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(creators)
	for i := 0; i < creators; i++ {
		go func(idx int) {
			defer wg.Done()
			<-start
			ids[idx], errs[idx] = InstallID()
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("creator %d failed: %v", i, err)
		}
		if ids[i] != ids[0] {
			t.Fatalf("creator %d reports installation %q, creator 0 reports %q", i, ids[i], ids[0])
		}
	}
}

// TestInstallID_RefusesAFileHoldingNoIdentifier covers the file a crash between
// creating and writing leaves. Reporting the empty string would make every
// installation whose file is empty answer to one identity, and each would then
// reclaim the others' upload locks.
func TestInstallID_RefusesAFileHoldingNoIdentifier(t *testing.T) {
	dir := inItsOwnConfigDirectory(t)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("create the configuration directory: %v", err)
	}
	path := filepath.Join(dir, installIDFile)
	if err := os.WriteFile(path, []byte("  \n"), 0600); err != nil {
		t.Fatalf("write an empty identifier: %v", err)
	}

	id, err := InstallID()
	if err == nil {
		t.Fatalf("a file holding no identifier reported installation %q", id)
	}
	if id != "" {
		t.Errorf("InstallID reported %q alongside an error", id)
	}
}

// TestInstallID_WithoutAConfigurationDirectory pins the answer when there is
// nowhere to keep it: an error, never an identifier a second installation could
// also report.
func TestInstallID_WithoutAConfigurationDirectory(t *testing.T) {
	for _, name := range []string{"HOME", "USERPROFILE", "LOCALAPPDATA"} {
		t.Setenv(name, "")
	}
	if getConfigDir() != "" {
		t.Skip("this platform resolves a configuration directory without the environment")
	}

	id, err := InstallID()
	if err == nil {
		t.Fatalf("reported installation %q with nowhere to keep it", id)
	}
	if id != "" {
		t.Errorf("InstallID reported %q alongside an error", id)
	}
}
