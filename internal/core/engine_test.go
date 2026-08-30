package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/models"
)

// newScanEngine returns an engine whose config skips directory validation, as
// every scan test needs.
func newScanEngine(t *testing.T) *Engine {
	t.Helper()
	cfg, _ := config.LoadConfigCSV("")
	cfg.ValidationPattern = ""
	engine, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	return engine
}

// mkRunDirs creates the pattern-matching directories a scan is expected to find.
func mkRunDirs(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// wantAbsoluteDirs fails for any scanned job whose directory is relative.
func wantAbsoluteDirs(t *testing.T, jobs []models.JobSpec) {
	t.Helper()
	for i, job := range jobs {
		if !filepath.IsAbs(job.Directory) {
			t.Errorf("Job %d directory should be absolute, got %q", i, job.Directory)
		}
	}
}

func TestNewEngine(t *testing.T) {
	engine, err := NewEngine(nil)
	if err != nil {
		t.Fatalf("Failed to create engine with nil config: %v", err)
	}
	if engine == nil {
		t.Fatal("Engine should not be nil")
	}
	if engine.eventBus == nil {
		t.Error("Event bus should be initialized")
	}
}

// TestEngine_Config covers the two ways a config reaches the engine — passed to
// NewEngine, or swapped in later with UpdateConfig — and reads it back through
// GetConfig. A zero want field is not asserted.
func TestEngine_Config(t *testing.T) {
	tests := []struct {
		name              string
		mutate            func(cfg *config.Config)
		viaUpdate         bool // NewEngine(nil) first, then UpdateConfig
		wantTarWorkers    int
		wantUploadWorkers int
		wantAPIBaseURL    string
	}{
		{
			name: "new engine keeps worker counts",
			mutate: func(cfg *config.Config) {
				cfg.TarWorkers = 8
				cfg.UploadWorkers = 16
			},
			wantTarWorkers:    8,
			wantUploadWorkers: 16,
		},
		{
			name: "new engine keeps the endpoint",
			mutate: func(cfg *config.Config) {
				cfg.APIBaseURL = "https://eu.rescale.com"
				cfg.APIKey = "test-key"
			},
			wantAPIBaseURL: "https://eu.rescale.com",
		},
		{
			name: "update config replaces values",
			mutate: func(cfg *config.Config) {
				cfg.TarWorkers = 12
				cfg.APIBaseURL = "https://platform.rescale.com"
			},
			viaUpdate:      true,
			wantTarWorkers: 12,
			wantAPIBaseURL: "https://platform.rescale.com",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _ := config.LoadConfigCSV("")
			tc.mutate(cfg)

			var engine *Engine
			if tc.viaUpdate {
				engine, _ = NewEngine(nil)
				if err := engine.UpdateConfig(cfg); err != nil {
					t.Fatalf("Failed to update config: %v", err)
				}
			} else {
				var err error
				engine, err = NewEngine(cfg)
				if err != nil {
					t.Fatalf("Failed to create engine with custom config: %v", err)
				}
			}

			got := engine.GetConfig()
			if tc.wantTarWorkers != 0 && got.TarWorkers != tc.wantTarWorkers {
				t.Errorf("TarWorkers = %d, want %d", got.TarWorkers, tc.wantTarWorkers)
			}
			if tc.wantUploadWorkers != 0 && got.UploadWorkers != tc.wantUploadWorkers {
				t.Errorf("UploadWorkers = %d, want %d", got.UploadWorkers, tc.wantUploadWorkers)
			}
			if tc.wantAPIBaseURL != "" && got.APIBaseURL != tc.wantAPIBaseURL {
				t.Errorf("APIBaseURL = %q, want %q", got.APIBaseURL, tc.wantAPIBaseURL)
			}
		})
	}
}

func TestEngine_Events(t *testing.T) {
	engine, _ := NewEngine(nil)

	eventBus := engine.Events()
	if eventBus == nil {
		t.Fatal("Event bus should not be nil")
	}

	// Test that we can subscribe and receive events
	ch := eventBus.Subscribe(events.EventLog)

	eventBus.PublishLog(events.InfoLevel, "test", "", "", nil)

	select {
	case event := <-ch:
		logEvent, ok := event.(*events.LogEvent)
		if !ok {
			t.Fatal("Expected LogEvent")
		}
		if logEvent.Message != "test" {
			t.Errorf("Expected message 'test', got '%s'", logEvent.Message)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Timeout waiting for event")
	}
}

func TestEngine_Stop(t *testing.T) {
	engine, _ := NewEngine(nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	engine.ctx = ctx
	engine.cancel = cancel

	// Stop should not panic
	engine.Stop()

	// Context should be cancelled
	select {
	case <-ctx.Done():
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Error("Context was not cancelled")
	}
}

func TestEngine_SaveConfig(t *testing.T) {
	cfg, _ := config.LoadConfigCSV("")
	cfg.TarWorkers = 8
	cfg.APIBaseURL = "https://platform.rescale.com"

	engine, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}

	savePath := filepath.Join(t.TempDir(), "test_config.csv")
	if err := engine.SaveConfig(savePath); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	if _, err := os.Stat(savePath); os.IsNotExist(err) {
		t.Error("Config file was not created")
	}

	// Load it back and verify
	loadedCfg, err := config.LoadConfigCSV(savePath)
	if err != nil {
		t.Fatalf("Failed to load saved config: %v", err)
	}
	if loadedCfg.TarWorkers != 8 {
		t.Errorf("Expected TarWorkers=8, got %d", loadedCfg.TarWorkers)
	}
}

// TestEngine_ScanToSpecs_AbsolutePaths covers the PartDirs scan root; the
// recursive test below covers the working-directory fallback.
func TestEngine_ScanToSpecs_AbsolutePaths(t *testing.T) {
	engine := newScanEngine(t)

	tmpDir := t.TempDir()
	mkRunDirs(t, tmpDir, "Run_1", "Run_2")

	template := models.JobSpec{
		JobName:         "test_job_1",
		AnalysisCode:    "user_included",
		AnalysisVersion: "1.0",
		Command:         "./run.sh",
		CoreType:        "emerald",
		CoresPerSlot:    4,
		Slots:           1,
		WalltimeHours:   1.0,
	}

	jobs, err := engine.ScanToSpecs(template, ScanOptions{
		Pattern:           "Run_*",
		StartIndex:        1,
		ValidationPattern: "",
		PartDirs:          []string{tmpDir},
	})
	if err != nil {
		t.Fatalf("ScanToSpecs failed: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("Expected 2 jobs, got %d", len(jobs))
	}
	wantAbsoluteDirs(t, jobs)
}

// TestEngine_RecursiveScan_SkipDir verifies that nested directories matching
// the pattern are NOT discovered when using recursive scan (SkipDir behavior).
// With no PartDirs, the scan root is the working directory.
func TestEngine_RecursiveScan_SkipDir(t *testing.T) {
	engine := newScanEngine(t)

	tmpDir := t.TempDir()
	// Top-level Run dirs, plus a nested one inside Run_1 that must not match.
	mkRunDirs(t, tmpDir, "Run_1", "Run_2", filepath.Join("Run_1", "sub", "Run_3"))

	t.Chdir(tmpDir)

	jobs, err := engine.ScanToSpecs(models.JobSpec{JobName: "test_job_1"}, ScanOptions{
		Pattern:           "Run_*",
		Recursive:         true,
		StartIndex:        1,
		ValidationPattern: "",
	})
	if err != nil {
		t.Fatalf("Recursive scan failed: %v", err)
	}

	// Should find only Run_1 and Run_2 (not nested Run_3)
	if len(jobs) != 2 {
		t.Errorf("Expected 2 jobs (SkipDir should prevent nested match), got %d", len(jobs))
		for _, j := range jobs {
			t.Logf("  Found: %s -> %s", j.JobName, j.Directory)
		}
	}
	wantAbsoluteDirs(t, jobs)
}

func TestEngine_JobMonitoring(t *testing.T) {
	engine, _ := NewEngine(nil)

	engine.StartJobMonitoring(100 * time.Millisecond)

	// Should be able to start without errors
	time.Sleep(50 * time.Millisecond)

	// Should be able to stop without errors
	engine.StopJobMonitoring()
}

// TestEngine_RunContext walks the run-context state machine. Each row gets a
// fresh engine; the flows differ, so they stay separate closures rather than
// collapsing into shared inputs.
func TestEngine_RunContext(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, engine *Engine)
	}{
		{
			name: "start_run",
			run: func(t *testing.T, engine *Engine) {
				if engine.IsRunActive() {
					t.Error("No run should be active initially")
				}
				if err := engine.StartRun("test_run_1", "/tmp/state.csv", 5); err != nil {
					t.Fatalf("StartRun failed: %v", err)
				}
				if !engine.IsRunActive() {
					t.Error("Run should be active after StartRun")
				}

				ctx := engine.GetRunContext()
				if ctx == nil {
					t.Fatal("GetRunContext should return non-nil when run is active")
				}
				if ctx.RunID != "test_run_1" {
					t.Errorf("Expected RunID 'test_run_1', got '%s'", ctx.RunID)
				}
				if ctx.StateFile != "/tmp/state.csv" {
					t.Errorf("Expected StateFile '/tmp/state.csv', got '%s'", ctx.StateFile)
				}
				if ctx.TotalJobs != 5 {
					t.Errorf("Expected TotalJobs 5, got %d", ctx.TotalJobs)
				}
				engine.EndRun()
			},
		},
		{
			name: "prevent_double_start",
			run: func(t *testing.T, engine *Engine) {
				if err := engine.StartRun("run_1", "/tmp/state1.csv", 3); err != nil {
					t.Fatalf("First StartRun failed: %v", err)
				}
				if err := engine.StartRun("run_2", "/tmp/state2.csv", 2); err == nil {
					t.Error("Second StartRun should fail while first run is active")
				}

				engine.EndRun()

				// Now should be able to start again
				if err := engine.StartRun("run_3", "/tmp/state3.csv", 1); err != nil {
					t.Errorf("StartRun after EndRun should succeed: %v", err)
				}
				engine.EndRun()
			},
		},
		{
			name: "end_run",
			run: func(t *testing.T, engine *Engine) {
				engine.StartRun("test_run", "/tmp/state.csv", 5)
				engine.EndRun()

				if engine.IsRunActive() {
					t.Error("Run should not be active after EndRun")
				}
				if ctx := engine.GetRunContext(); ctx != nil {
					t.Error("GetRunContext should return nil after EndRun")
				}
			},
		},
		{
			name: "reset_run",
			run: func(t *testing.T, engine *Engine) {
				engine.StartRun("test_run", "/tmp/state.csv", 5)
				engine.ResetRun()

				if engine.IsRunActive() {
					t.Error("Run should not be active after ResetRun")
				}
				if engine.GetState() != nil {
					t.Error("State should be nil after ResetRun")
				}
			},
		},
		{
			name: "get_run_stats_with_no_run",
			run: func(t *testing.T, engine *Engine) {
				total, completed, failed, pending := engine.GetRunStats()
				if total != 0 || completed != 0 || failed != 0 || pending != 0 {
					t.Errorf("Expected all zeros, got total=%d, completed=%d, failed=%d, pending=%d",
						total, completed, failed, pending)
				}
			},
		},
		{
			name: "get_run_context_returns_a_copy",
			run: func(t *testing.T, engine *Engine) {
				engine.StartRun("test_run", "/tmp/state.csv", 5)

				ctx1 := engine.GetRunContext()
				ctx1.RunID = "modified"

				if ctx2 := engine.GetRunContext(); ctx2.RunID != "test_run" {
					t.Error("GetRunContext should return a copy, not allow external modification")
				}
				engine.EndRun()
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			engine, _ := NewEngine(nil)
			tc.run(t, engine)
		})
	}
}
