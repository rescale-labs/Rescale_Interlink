//go:build windows

package config

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// applyTokenFileACL replaces the named file's DACL with a protected ACL
// that grants full control to:
//
//   - the supplied ownerSID (the user who owns the token)
//   - BUILTIN\Administrators (BA)
//   - NT AUTHORITY\SYSTEM (SY)
//
// The descriptor is SE_DACL_PROTECTED: no inheritance from the parent
// directory is applied. This is the explicit-ACL posture spec §11.2
// commits to — falling back to inherited defaults is not sufficient
// because a misconfigured parent could widen access unexpectedly.
//
// If ownerSID is empty, no ACL is applied and an error is returned —
// callers should log WARN and continue (the file has still been written
// with Go's default permissions, same as pre-Plan-4 behavior).
func applyTokenFileACL(path string, ownerSID string) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}
	if ownerSID == "" {
		return fmt.Errorf("empty owner SID")
	}

	sddl := fmt.Sprintf("D:P(A;;FA;;;%s)(A;;FA;;;BA)(A;;FA;;;SY)", ownerSID)
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("parse SDDL %q: %w", sddl, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("extract DACL: %w", err)
	}

	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, // owner unchanged
		nil, // group unchanged
		dacl,
		nil, // sacl unchanged
	); err != nil {
		return fmt.Errorf("SetNamedSecurityInfo %s: %w", path, err)
	}
	return nil
}

// currentUserSID returns the SID of the user the current process is
// running as, as a string suitable for inclusion in an SDDL ACE.
func currentUserSID() (string, error) {
	token := windows.GetCurrentProcessToken()
	// GetCurrentProcessToken returns a pseudo-handle that does not need
	// to be closed; it is valid for the life of the process.
	user, err := token.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("get token user: %w", err)
	}
	return user.User.Sid.String(), nil
}
