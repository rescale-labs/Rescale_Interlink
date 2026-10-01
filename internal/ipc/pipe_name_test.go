package ipc

import "testing"

// The pipe names and their DACL are plain strings, so they are checked here on
// every platform; pipe_windows_test.go listens on them natively.

const testSID, otherTestSID = "S-1-5-21-1000-2000-3000-1001", "S-1-5-21-1000-2000-3000-1002"

// Several people signed in at once each run a daemon, so each has a pipe of
// their own, named for their SID.
func TestPipeNameFor_OnePerUser(t *testing.T) {
	if name, err := pipeNameFor(pipeBase, testSID); err != nil || name != `\\.\pipe\rescale-interlink-`+testSID {
		t.Errorf("pipeNameFor(%q) = %q, %v; want the pipe named for that SID", testSID, name, err)
	}
}

func TestPipeNameFor_NoSIDIsRefused(t *testing.T) {
	if name, err := pipeNameFor(pipeBase, ""); err == nil || name != "" {
		t.Errorf(`pipeNameFor("") = %q, %v; want a refusal, not a name every user shares`, name, err)
	}
}

// The pipe admits its user and LocalSystem, and no one else; a protected DACL
// inherits nothing. Its owner is the user whatever the token's default owner,
// Administrators when elevated, so a client can tell it from a pipe of the same
// name another user created.
func TestPipeSDDL_OnlyTheUserAndSystem(t *testing.T) {
	if got, want := pipeSDDL(testSID), "O:"+testSID+"D:P(A;;GA;;;"+testSID+")(A;;GA;;;SY)"; got != want {
		t.Errorf("pipeSDDL(%q) = %q, want %q", testSID, got, want)
	}
}
