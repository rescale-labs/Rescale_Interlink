package state

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
)

// TestWindowsOpenProcessLiveness pins what each answer OpenProcess gives means.
// It runs on every platform rather than only on the one it describes, because
// this mapping is what decides whether a lock may be cleared on Windows and
// nothing else here can be run there: reading ERROR_ACCESS_DENIED as absence is
// exactly how a second login takes a running upload's lock.
func TestWindowsOpenProcessLiveness(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want processLiveness
	}{
		{"access denied: a live process this login may not open", syscall.Errno(5), livenessAlive},
		{"access denied, wrapped by its caller", fmt.Errorf("open PID 4711: %w", syscall.Errno(5)), livenessAlive},
		{"invalid parameter: no process has that PID", syscall.Errno(87), livenessDead},
		{"any other failure has answered nothing", syscall.Errno(8), livenessUnknown},
		{"an error that never came from OpenProcess", errors.New("something else went wrong"), livenessUnknown},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := windowsOpenProcessLiveness(testCase.err); got != testCase.want {
				t.Errorf("%v is read as %s, want %s", testCase.err, livenessName(got), livenessName(testCase.want))
			}
		})
	}
}

// TestProbeProcessLiveness_AnswersAboutThisProcess is the one test that asks the
// operating system itself rather than the seam. This process is alive by
// definition; a PID no process can hold is not.
func TestProbeProcessLiveness_AnswersAboutThisProcess(t *testing.T) {
	if liveness, err := probeProcessLiveness(os.Getpid()); liveness != livenessAlive {
		t.Errorf("this process reads as %s (%v), want alive", livenessName(liveness), err)
	}
	if liveness, err := probeProcessLiveness(0); liveness != livenessDead {
		t.Errorf("PID 0 reads as %s (%v), want dead", livenessName(liveness), err)
	}
}

func livenessName(liveness processLiveness) string {
	switch liveness {
	case livenessAlive:
		return "alive"
	case livenessDead:
		return "dead"
	default:
		return "unknown"
	}
}
