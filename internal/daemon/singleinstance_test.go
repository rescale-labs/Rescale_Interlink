package daemon

import "testing"

// TestSingleInstanceLock_BlocksSecondAcquire verifies the core duplicate-daemon
// guarantee: while one holder owns the lock, a second acquire in the same
// process fails; once released, the lock is available again.
func TestSingleInstanceLock_BlocksSecondAcquire(t *testing.T) {
	release, ok := AcquireSingleInstanceLock()
	if !ok {
		t.Skip("could not acquire lock (another daemon may be running on this machine); skipping")
	}

	// A second acquire while the first is held must fail.
	release2, ok2 := AcquireSingleInstanceLock()
	if ok2 {
		release2()
		release()
		t.Fatal("second AcquireSingleInstanceLock succeeded while first was held; lock is not exclusive")
	}

	// Release the first holder; the lock must become available again.
	release()

	release3, ok3 := AcquireSingleInstanceLock()
	if !ok3 {
		t.Fatal("AcquireSingleInstanceLock failed after the previous holder released; lock not freed")
	}
	release3()
}
