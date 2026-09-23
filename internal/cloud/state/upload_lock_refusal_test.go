package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestUploadLockRefusalsMatchErrUploadLocked pins which failures to take a
// source's upload lock match ErrUploadLocked: those where another transfer holds
// the source, or may. The rest are faults in working the lock file.
func TestUploadLockRefusalsMatchErrUploadLocked(t *testing.T) {
	// A fixed domain, so a live, undecided or abandoned owner is judged as one
	// on every host, including one whose system names no domain.
	withPIDDomain(t, "this-machine")
	record := func(localPath string, pid int, token string) uploadLockState {
		return uploadLockState{ProcessID: pid, OwnerToken: token, Host: lockHost, Owner: lockOwner,
			PIDDomain: currentPIDDomain(), AcquiredAt: time.Now(), LocalPath: localPath}
	}
	newSource := func(t *testing.T) string { return filepath.Join(t.TempDir(), "testfile.bin") }

	for _, tc := range []struct {
		name   string
		locked bool
		hold   func(t *testing.T) string // leaves a source held the way this row finds it
	}{
		{"another transfer in this process", true, func(t *testing.T) string {
			localPath := newSource(t)
			held, err := AcquireUploadLock(localPath)
			if err != nil {
				t.Fatalf("acquire: %v", err)
			}
			t.Cleanup(func() { ReleaseUploadLock(held) })
			return localPath
		}},
		{"this run's record on disk", true, func(t *testing.T) string {
			localPath := newSource(t)
			writeLockFile(t, localPath, record(localPath, os.Getpid(), processLockToken))
			return localPath
		}},
		{"a live process", true, func(t *testing.T) string {
			withProcessLiveness(t, livenessOf(func(int) bool { return true }))
			localPath := newSource(t)
			writeLockFile(t, localPath, record(localPath, 424301, "a-running-upload"))
			return localPath
		}},
		{"another PID domain", true, func(t *testing.T) string {
			localPath := newSource(t)
			foreign := record(localPath, 424302, "elsewhere")
			foreign.PIDDomain = "another-machine"
			writeLockFile(t, localPath, foreign)
			return localPath
		}},
		{"an owner the system will not answer about", true, func(t *testing.T) string {
			withProcessLiveness(t, func(int) (processLiveness, error) {
				return livenessUnknown, errors.New("access is denied")
			})
			localPath := newSource(t)
			writeLockFile(t, localPath, record(localPath, 424303, "unanswered"))
			return localPath
		}},
		{"a record naming no owner", true, func(t *testing.T) string {
			localPath := newSource(t)
			plantOwnerlessLock(t, localPath+".upload.lock", time.Now())
			return localPath
		}},
		{"a reclamation in progress", true, func(t *testing.T) string {
			localPath := plantAbandonedLock(t, 424304)
			if err := os.WriteFile(claimPathOf(t, localPath), nil, 0600); err != nil {
				t.Fatalf("plant the other taker's claim: %v", err)
			}
			return localPath
		}},
		// Every create meets the link and every judgement finds nothing behind
		// it, so the lock "keeps being retaken" on every attempt, by nobody.
		{"a dangling symlink at the lock path", false, func(t *testing.T) string {
			localPath := newSource(t)
			if err := os.Symlink(filepath.Join(t.TempDir(), "gone", "lock"), localPath+".upload.lock"); err != nil {
				t.Skipf("cannot create a symlink: %v", err)
			}
			return localPath
		}},
		{"a directory that will not hold a lock", false, func(t *testing.T) string {
			return filepath.Join(denyingDirectory(t), "testfile.bin")
		}},
		{"a lock that cannot be read", false, func(t *testing.T) string {
			localPath := newSource(t)
			if err := os.Mkdir(localPath+".upload.lock", 0700); err != nil {
				t.Fatalf("plant an unreadable lock: %v", err)
			}
			return localPath
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lock, err := AcquireUploadLock(tc.hold(t))
			if err == nil {
				ReleaseUploadLock(lock)
				t.Fatal("acquired the upload lock")
			}
			if got := errors.Is(err, ErrUploadLocked); got != tc.locked {
				t.Errorf("errors.Is(err, ErrUploadLocked) = %v, want %v: %v", got, tc.locked, err)
			}
		})
	}
}
