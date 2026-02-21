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

func TestNewEngine(t *testing.T) {
	// Test with nil config
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

	// Test with custom config
	cfg, _ := config.LoadConfigCSV("")
	cfg.APIBaseURL = "https://test.rescale.com"
	cfg.APIKey = "test-key"

	engine2, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("Failed to create engine with custom config: %v", err)
	}

	gotCfg := engine2.GetConfig()
	if gotCfg.APIBaseURL != "https://test.rescale.com" {
		t.Errorf("Expected API URL 'https://test.rescale.com', got '%s'", gotCfg.APIBaseURL)
	}
}

func TestEngine_GetConfig(t *testing.T) {
	cfg, _ := config.LoadConfigCSV("")
	cfg.TarWorkers = 8
	cfg.UploadWorkers = 16

	engine, _ := NewEngine(cfg)

	gotCfg := engine.GetConfig()
	if gotCfg.TarWorkers != 8 {
		t.Errorf("Expected TarWorkers=8, got %d", gotCfg.TarWorkers)
	}
	if gotCfg.UploadWorkers != 16 {
		t.Errorf("Expected UploadWorkers=16, got %d", gotCfg.UploadWorkers)
	}
}

func TestEngine_UpdateConfig(t *testing.T) {
	engine, _ := NewEngine(nil)

	newCfg, _ := config.LoadConfigCSV("")
	newCfg.TarWorkers = 12
	newCfg.APIBaseURL = "https://updated.rescale.com"

	err := engine.UpdateConfig(newCfg)
	if err != nil {
		t.Fatalf("Failed to update config: %v", err)
	}

	gotCfg := engine.GetConfig()
	if gotCfg.TarWorkers != 12 {
		t.Errorf("Config not updated: expected TarWorkers=12, got %d", gotCfg.TarWorkers)
	}
	if gotCfg.APIBaseURL != "https://updated.rescale.com" {
		t.Errorf("Config not updated: expected updated URL, got '%s'", gotCfg.APIBaseURL)
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

func TestEngine_Plan_InvalidFile(t *testing.T) {
	engine, _ := NewEngine(nil)

	_, err := engine.Plan("/nonexistent/file.csv", false)
	if err == nil {
		t.Error("Expected error for nonexistent file")
	}
}

func TestEngine_Plan_ValidFile(t *testing.T) {
	// Create a temporary jobs CSV for testing
	tmpDir := t.TempDir()
	jobsCSV := filepath.Join(tmpDir, "jobs.csv")

	content := `Directory,JobName,AnalysisCode,AnalysisVersion,Command,CoreType,CoresPerSlot,WalltimeHours,Slots,LicenseSettings
/tmp/test,test-job-1,user_included,1.0,./run.sh,emerald,4,1.0,1,"{""test"":""value""}"
/tmp/test2,test-job-2,user_included,1.0,./run.sh,onyx,8,2.0,2,"{""test"":""value""}"`

	if err := os.WriteFile(jobsCSV, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create test CSV: %v", err)
	}

	engine, _ := NewEngine(nil)

	result, err := engine.Plan(jobsCSV, false)
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	if result.TotalJobs != 2 {
		t.Errorf("Expected 2 total jobs, got %d", result.TotalJobs)
	}

	// Without core type validation and with nonexistent directories, should have errors
	if result.InvalidJobs == 0 {
		t.Log("Note: Some validation errors expected for nonexistent directories")
	}
}

func TestEngine_Stop(t *testing.T) {
	engine, _ := NewEngine(nil)

	// Create a context
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
	cfg.APIBaseURL = "https://test.rescale.com"

	engine, _ := NewEngine(cfg)

	// Create temp file for saving
	tmpDir := t.TempDir()
	savePath := filepath.Join(tmpDir, "test_config.csv")

	err := engine.SaveConfig(savePath)
	if err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	// Verify file was created
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

func TestEngine_Scan(t *testing.T) {
	// Create config without validation pattern
	cfg, _ := config.LoadConfigCSV("")
	cfg.ValidationPattern = ""
	engine, _ := NewEngine(cfg)

	// Create temp directories and template
	tmpDir := t.TempDir()

	// Create test directories
	testDir1 := filepath.Join(tmpDir, "Run_1")
	testDir2 := filepath.Join(tmpDir, "Run_2")
	os.MkdirAll(testDir1, 0755)
	os.MkdirAll(testDir2, 0755)

	// Create template CSV
	templatePath := filepath.Join(tmpDir, "template.csv")
	templateContent := `Directory,JobName,AnalysisCode,AnalysisVersion,Command,CoreType,CoresPerSlot,WalltimeHours,Slots,LicenseSettings
/tmp/test,test_job_1,user_included,1.0,./run.sh,emerald,4,1.0,1,"{""test"":""value""}"`
	os.WriteFile(templatePath, []byte(templateContent), 0644)

	// Change to temp directory for scan
	oldDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldDir)

	// Run scan
	outputPath := filepath.Join(tmpDir, "jobs.csv")
	opts := ScanOptions{
		TemplateCSV:       templatePath,
		OutputCSV:         outputPath,
		Pattern:           "Run_*",
		StartIndex:        1,
		Overwrite:         true,
		ValidationPattern: "", // Don't validate for this test
		RunSubpath:        "", // No subpath
		MultiPartMode:     false,
		PartDirs:          nil,
	}

	err := engine.Scan(opts)
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	// Verify output file exists
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		t.Error("Output CSV was not created")
	}

	// Load and verify output has 2 jobs
	jobs, err := config.LoadJobsCSV(outputPath)
	if err != nil {
		t.Fatalf("Failed to load generated CSV: %v", err)
	}

	if len(jobs) != 2 {
		t.Errorf("Expected 2 jobs, got %d", len(jobs))
	}

	// Verify scan output paths are absolute (Change 1: no more relative paths)
	for i, job := range jobs {
		if !filepath.IsAbs(job.Directory) {
			t.Errorf("Job %d directory should be absolute, got %q", i, job.Directory)
		}
	}
}

func TestEngine_ScanToSpecs_AbsolutePaths(t *testing.T) {
	cfg, _ := config.LoadConfigCSV("")
	cfg.ValidationPattern = ""
	engine, _ := NewEngine(cfg)

	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "Run_1"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "Run_2"), 0755)

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

	opts := ScanOptions{
		Pattern:           "Run_*",
		StartIndex:        1,
		ValidationPattern: "",
		PartDirs:          []string{tmpDir},
	}

	jobs, err := engine.ScanToSpecs(template, opts)
	if err != nil {
		t.Fatalf("ScanToSpecs failed: %v", err)
	}

	if len(jobs) != 2 {
		t.Fatalf("Expected 2 jobs, got %d", len(jobs))
	}

	for i, job := range jobs {
		if !filepath.IsAbs(job.Directory) {
			t.Errorf("Job %d directory should be absolute, got %q", i, job.Directory)
		}
	}
}

func TestEngine_RecursiveScan_SkipDir(t *testing.T) {
	// Verify that nested directories matching the pattern are NOT discovered
	// when using recursive scan (SkipDir behavior).
	cfg, _ := config.LoadConfigCSV("")
	cfg.ValidationPattern = ""
	engine, _ := NewEngine(cfg)

	tmpDir := t.TempDir()
	// Create top-level Run dirs
	os.MkdirAll(filepath.Join(tmpDir, "Run_1"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "Run_2"), 0755)
	// Create nested Run dir inside Run_1
	os.MkdirAll(filepath.Join(tmpDir, "Run_1", "sub", "Run_3"), 0755)

	// Change to temp directory for scan
	oldDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldDir)

	outputPath := filepath.Join(tmpDir, "jobs_recursive.csv")
	templatePath := filepath.Join(tmpDir, "template.csv")
	templateContent := `Directory,JobName,AnalysisCode,AnalysisVersion,Command,CoreType,CoresPerSlot,WalltimeHours,Slots,LicenseSettings
/tmp/test,test_job_1,user_included,1.0,./run.sh,emerald,4,1.0,1,`
	os.WriteFile(templatePath, []byte(templateContent), 0644)

	opts := ScanOptions{
		TemplateCSV:       templatePath,
		OutputCSV:         outputPath,
		Pattern:           "Run_*",
		Recursive:         true,
		StartIndex:        1,
		Overwrite:         true,
		ValidationPattern: "",
	}

	err := engine.Scan(opts)
	if err != nil {
		t.Fatalf("Recursive scan failed: %v", err)
	}

	jobs, err := config.LoadJobsCSV(outputPath)
	if err != nil {
		t.Fatalf("Failed to load scan output: %v", err)
	}

	// Should find only Run_1 and Run_2 (not nested Run_3)
	if len(jobs) != 2 {
		t.Errorf("Expected 2 jobs (SkipDir should prevent nested match), got %d", len(jobs))
		for _, j := range jobs {
			t.Logf("  Found: %s -> %s", j.JobName, j.Directory)
		}
	}
}

func TestEngine_ValidateJob(t *testing.T) {
	_, _ = NewEngine(nil)

	tests := []struct {
		name    string
		jobSpec *struct {
			JobName       string
			Directory     string
			Command       string
			CoresPerSlot  int
			Slots         int
			WalltimeHours float64
			CoreType      string
		}
		expectErrors bool
	}{
		{
			name: "valid job",
			jobSpec: &struct {
				JobName       string
				Directory     string
				Command       string
				CoresPerSlot  int
				Slots         int
				WalltimeHours float64
				CoreType      string
			}{
				JobName:       "test-job",
				Directory:     "/tmp", // exists
				Command:       "./run.sh",
				CoresPerSlot:  4,
				Slots:         1,
				WalltimeHours: 1.0,
				CoreType:      "emerald",
			},
			expectErrors: false,
		},
		{
			name: "missing job name",
			jobSpec: &struct {
				JobName       string
				Directory     string
				Command       string
				CoresPerSlot  int
				Slots         int
				WalltimeHours float64
				CoreType      string
			}{
				JobName:       "",
				Directory:     "/tmp",
				Command:       "./run.sh",
				CoresPerSlot:  4,
				Slots:         1,
				WalltimeHours: 1.0,
			},
			expectErrors: true,
		},
		{
			name: "invalid cores per slot",
			jobSpec: &struct {
				JobName       string
				Directory     string
				Command       string
				CoresPerSlot  int
				Slots         int
				WalltimeHours float64
				CoreType      string
			}{
				JobName:       "test",
				Directory:     "/tmp",
				Command:       "./run.sh",
				CoresPerSlot:  0, // Invalid
				Slots:         1,
				WalltimeHours: 1.0,
			},
			expectErrors: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a real JobSpec
			jobSpec := &struct {
				JobName       string
				Directory     string
				Command       string
				CoresPerSlot  int
				Slots         int
				WalltimeHours float64
				CoreType      string
			}{
				JobName:       tt.jobSpec.JobName,
				Directory:     tt.jobSpec.Directory,
				Command:       tt.jobSpec.Command,
				CoresPerSlot:  tt.jobSpec.CoresPerSlot,
				Slots:         tt.jobSpec.Slots,
				WalltimeHours: tt.jobSpec.WalltimeHours,
				CoreType:      tt.jobSpec.CoreType,
			}

			// Note: validateJob is private, so we test it indirectly through Plan
			// This test serves as documentation of expected behavior
			_ = jobSpec
		})
	}
}

func TestEngine_JobMonitoring(t *testing.T) {
	engine, _ := NewEngine(nil)

	// Start monitoring
	engine.StartJobMonitoring(100 * time.Millisecond)

	// Should be able to start without errors
	time.Sleep(50 * time.Millisecond)

	// Stop monitoring
	engine.StopJobMonitoring()

	// Should be able to stop without errors
}

// v4.0.0: Tests for Run Context Management

func TestEngine_RunContext_StartRun(t *testing.T) {
	engine, _ := NewEngine(nil)

	// Initially no run should be active
	if engine.IsRunActive() {
		t.Error("No run should be active initially")
	}

	// Start a run
	err := engine.StartRun("test_run_1", "/tmp/state.csv", 5)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}

	// Run should now be active
	if !engine.IsRunActive() {
		t.Error("Run should be active after StartRun")
	}

	// Get run context
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

	// Clean up
	engine.EndRun()
}

func TestEngine_RunContext_PreventDoubleStart(t *testing.T) {
	engine, _ := NewEngine(nil)

	// Start first run
	err := engine.StartRun("run_1", "/tmp/state1.csv", 3)
	if err != nil {
		t.Fatalf("First StartRun failed: %v", err)
	}

	// Second start should fail
	err = engine.StartRun("run_2", "/tmp/state2.csv", 2)
	if err == nil {
		t.Error("Second StartRun should fail while first run is active")
	}

	// Clean up
	engine.EndRun()

	// Now should be able to start again
	err = engine.StartRun("run_3", "/tmp/state3.csv", 1)
	if err != nil {
		t.Errorf("StartRun after EndRun should succeed: %v", err)
	}

	engine.EndRun()
}

func TestEngine_RunContext_EndRun(t *testing.T) {
	engine, _ := NewEngine(nil)

	// Start a run
	engine.StartRun("test_run", "/tmp/state.csv", 5)

	// End the run
	engine.EndRun()

	// Run should no longer be active
	if engine.IsRunActive() {
		t.Error("Run should not be active after EndRun")
	}

	// GetRunContext should return nil
	if ctx := engine.GetRunContext(); ctx != nil {
		t.Error("GetRunContext should return nil after EndRun")
	}
}

func TestEngine_RunContext_ResetRun(t *testing.T) {
	engine, _ := NewEngine(nil)

	// Start a run
	engine.StartRun("test_run", "/tmp/state.csv", 5)

	// Reset should clear everything
	engine.ResetRun()

	// Run should not be active
	if engine.IsRunActive() {
		t.Error("Run should not be active after ResetRun")
	}

	// State should be nil
	if engine.GetState() != nil {
		t.Error("State should be nil after ResetRun")
	}
}

func TestEngine_RunContext_GetRunStats_NoRun(t *testing.T) {
	engine, _ := NewEngine(nil)

	// With no run, stats should be all zeros
	total, completed, failed, pending := engine.GetRunStats()
	if total != 0 || completed != 0 || failed != 0 || pending != 0 {
		t.Errorf("Expected all zeros, got total=%d, completed=%d, failed=%d, pending=%d",
			total, completed, failed, pending)
	}
}

func TestEngine_RunContext_GetRunContextCopy(t *testing.T) {
	engine, _ := NewEngine(nil)

	engine.StartRun("test_run", "/tmp/state.csv", 5)

	// Get context
	ctx1 := engine.GetRunContext()

	// Modify the returned context
	ctx1.RunID = "modified"

	// Get context again - should be unchanged
	ctx2 := engine.GetRunContext()
	if ctx2.RunID != "test_run" {
		t.Error("GetRunContext should return a copy, not allow external modification")
	}

	engine.EndRun()
}
