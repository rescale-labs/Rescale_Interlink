//go:build windows

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestWriteTokenFile_WindowsExplicitACL verifies that WriteTokenFile
// applies the spec §11.2 explicit ACL: three ACEs (owner, Administrators,
// SYSTEM), each with full access, and no inheritance.
func TestWriteTokenFile_WindowsExplicitACL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")

	if err := WriteTokenFile(path, "test-token-value"); err != nil {
		t.Fatalf("WriteTokenFile: %v", err)
	}

	sd, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatalf("GetNamedSecurityInfo: %v", err)
	}

	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("DACL: %v (nil=%v)", err, dacl == nil)
	}

	sddl := sd.String()
	// Every ACE should carry FA (FILE_ALL_ACCESS = full control) and the
	// protected flag "P" must be present on the DACL.
	if !strings.Contains(sddl, "D:P") {
		t.Errorf("DACL is not protected (missing 'D:P'): %s", sddl)
	}

	// Expect exactly three ACEs granting FA to (owner, BA, SY). The owner
	// SID varies per test environment, so we assert against BA and SY by
	// name and count the ACE markers.
	aceMarker := "(A;"
	aceCount := strings.Count(sddl, aceMarker)
	if aceCount != 3 {
		t.Errorf("Expected 3 ACEs, got %d: %s", aceCount, sddl)
	}
	if !strings.Contains(sddl, "(A;;FA;;;BA)") {
		t.Errorf("Missing Administrators ACE: %s", sddl)
	}
	if !strings.Contains(sddl, "(A;;FA;;;SY)") {
		t.Errorf("Missing SYSTEM ACE: %s", sddl)
	}
}

// TestWriteTokenFile_NoInheritance verifies SE_DACL_PROTECTED is set on
// the token file. An inherited ACL could widen access unexpectedly if a
// parent directory has looser permissions.
func TestWriteTokenFile_NoInheritance(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := WriteTokenFile(path, "v"); err != nil {
		t.Fatalf("WriteTokenFile: %v", err)
	}

	sd, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatalf("GetNamedSecurityInfo: %v", err)
	}
	ctrl, _, err := sd.Control()
	if err != nil {
		t.Fatalf("Control: %v", err)
	}
	if ctrl&windows.SE_DACL_PROTECTED == 0 {
		t.Errorf("DACL is not protected (SE_DACL_PROTECTED clear); control=0x%x", ctrl)
	}
}

// TestMigrate_AppliesACLOnCopiedToken stages a token file in an
// old-location directory, then invokes the migration and asserts the new
// location has the explicit ACL.
func TestMigrate_AppliesACLOnCopiedToken(t *testing.T) {
	profileRoot := t.TempDir()
	oldBase := filepath.Join(profileRoot, "AppData", "Roaming", "Rescale", "Interlink")
	if err := os.MkdirAll(oldBase, 0700); err != nil {
		t.Fatal(err)
	}
	oldToken := filepath.Join(oldBase, "token")
	if err := os.WriteFile(oldToken, []byte("migrated-token\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// Drive the per-profile path directly; use the current process SID so
	// GetNamedSecurityInfo returns three ACEs with one recognizable owner.
	sid, err := currentUserSID()
	if err != nil || sid == "" {
		t.Skipf("could not capture current user SID: %v", err)
	}

	migratePerProfileWindowsCredentials(nil, ProfileMigrationTarget{
		Username:    "test",
		SID:         sid,
		ProfilePath: profileRoot,
	})

	newToken := filepath.Join(profileRoot, "AppData", "Local", "Rescale", "Interlink", "token")
	if _, err := os.Stat(newToken); err != nil {
		t.Fatalf("migrated token not at new location: %v", err)
	}
	sd, err := windows.GetNamedSecurityInfo(
		newToken,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatalf("GetNamedSecurityInfo: %v", err)
	}
	sddl := sd.String()
	if !strings.Contains(sddl, "D:P") {
		t.Errorf("Migrated token DACL not protected: %s", sddl)
	}
	if !strings.Contains(sddl, "(A;;FA;;;BA)") || !strings.Contains(sddl, "(A;;FA;;;SY)") {
		t.Errorf("Migrated token missing expected ACEs: %s", sddl)
	}
}
