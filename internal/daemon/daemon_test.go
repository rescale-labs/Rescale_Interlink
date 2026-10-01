package daemon

import (
	"testing"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/logging"
)

// TestDaemonDefaultConfig verifies daemon defaults used by downloadJob.
func TestDaemonDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MaxConcurrent != 5 {
		t.Errorf("DefaultConfig().MaxConcurrent = %d, want 5", cfg.MaxConcurrent)
	}
}

// TestDaemonConstruction verifies that the daemon holds a
// TransferService and an EventBus. The test uses a temp state file so New()
// doesn't mutate the real user's state.
func TestDaemonConstruction(t *testing.T) {
	isolateHome(t)
	appCfg := &config.Config{APIKey: "dummy", APIBaseURL: "https://platform.rescale.com"}
	daemonCfg := DefaultConfig()
	daemonCfg.StateFile = t.TempDir() + "/state.json"

	d, err := New(appCfg, daemonCfg, logging.NewLogger("daemon-test", nil))
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if d.ts == nil {
		t.Error("Daemon.ts is nil; expected TransferService")
	}
	if d.events == nil {
		t.Error("Daemon.events is nil; expected EventBus")
	}
	if d.TransferService() != d.ts {
		t.Error("TransferService() does not return the stored instance")
	}
}
