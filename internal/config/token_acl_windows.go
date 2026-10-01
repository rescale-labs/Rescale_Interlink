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
// directory is applied. Inherited defaults are not sufficient because a
// misconfigured parent could widen access unexpectedly.
//
// If ownerSID is empty, no ACL is applied and an error is returned —
// callers should log WARN and continue (the file has still been written
// with Go's default permissions).
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
