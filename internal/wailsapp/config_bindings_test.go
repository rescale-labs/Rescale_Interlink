package wailsapp

import (
	goruntime "runtime"
	"testing"
)

// TestGetAppInfo_OSAndSessionScopedDaemon verifies spec §2.2/§2.3: on
// macOS and Linux the daemon is session-scoped, and the DTO exposes
// goruntime.GOOS directly so the frontend can render platform-specific
// messaging without duplicating the check in React.
func TestGetAppInfo_OSAndSessionScopedDaemon(t *testing.T) {
	a := &App{}
	info := a.GetAppInfo()

	if info.OS != goruntime.GOOS {
		t.Errorf("OS = %q, want %q", info.OS, goruntime.GOOS)
	}

	wantSessionScoped := goruntime.GOOS == "darwin" || goruntime.GOOS == "linux"
	if info.SessionScopedDaemon != wantSessionScoped {
		t.Errorf("SessionScopedDaemon = %v, want %v (GOOS=%s)",
			info.SessionScopedDaemon, wantSessionScoped, goruntime.GOOS)
	}
}
