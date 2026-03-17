package platform

import "testing"

func TestInhibitSleep(t *testing.T) {
	release, err := InhibitSleep("test assertion")
	if err != nil {
		t.Logf("InhibitSleep returned error (may be expected on CI): %v", err)
	}
	if release == nil {
		t.Fatal("release function must never be nil")
	}

	// First call should succeed
	release()
	// Second call (idempotency) should not panic
	release()
}
