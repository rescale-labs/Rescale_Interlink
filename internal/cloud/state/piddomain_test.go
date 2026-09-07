package state

import (
	"errors"
	"runtime"
	"strings"
	"testing"
)

// TestReadPIDDomain_TheSystemNamesOneAndGoesOnNamingIt is the only test that
// asks the operating system itself rather than the seam, and it is the one that
// would catch a platform whose identifier cannot actually be read where the
// program runs. Two calls have to agree: the domain is compared against a record
// written by an earlier process, so an identifier that moved between reads would
// refuse every reclamation including this installation's own.
func TestReadPIDDomain_TheSystemNamesOneAndGoesOnNamingIt(t *testing.T) {
	named := runtime.GOOS == "linux" || runtime.GOOS == "darwin" || runtime.GOOS == "windows"

	domain, err := readPIDDomain()
	if !named {
		if err == nil {
			t.Fatalf("%s reported PID domain %q; nothing on this platform names the set of processes a PID belongs to", runtime.GOOS, domain)
		}
		return
	}
	if err != nil {
		t.Fatalf("%s did not name the PID domain of this process: %v", runtime.GOOS, err)
	}
	if strings.TrimSpace(domain) == "" {
		t.Fatal("the PID domain is blank; every domain would then compare equal to it")
	}

	again, err := readPIDDomain()
	if err != nil {
		t.Fatalf("the second read failed: %v", err)
	}
	if again != domain {
		t.Errorf("the second read names domain %q, want the one already read %q", again, domain)
	}
}

// TestCurrentPIDDomain_ReportsNothingWhenTheSystemWillNotSay pins the shape of
// the answer at the boundary: an error becomes no domain, never a partial one. A
// partial answer would be a string two systems could both report, and a domain
// two systems share is exactly what this replaced.
func TestCurrentPIDDomain_ReportsNothingWhenTheSystemWillNotSay(t *testing.T) {
	replacePIDDomain(t, func() (string, error) {
		return "half-an-answer", errors.New("the machine identifier could not be read")
	})
	if got := currentPIDDomain(); got != "" {
		t.Errorf("currentPIDDomain reported %q alongside an error, want no domain", got)
	}

	withPIDDomain(t, "a-whole-answer")
	if got := currentPIDDomain(); got != "a-whole-answer" {
		t.Errorf("currentPIDDomain reported %q, want the domain the system named", got)
	}
}
