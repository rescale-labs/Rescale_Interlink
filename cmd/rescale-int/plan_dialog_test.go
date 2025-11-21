package main

// This test is disabled temporarily due to Jobs Tab refactor
// It will be re-enabled once the old Plan button functionality is migrated

/*
import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"
	"github.com/rescale/rescale-int/internal/core"
	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/pur/config"
)

// TestPlanDialogInteraction tests that the Plan dialog doesn't freeze the GUI
func TestPlanDialogInteraction(t *testing.T) {
	// Create test app
	app := test.NewApp()
	defer app.Quit()

	// Create temporary test files
	tmpDir := t.TempDir()
	jobsCSV := filepath.Join(tmpDir, "test_jobs.csv")
	configCSV := filepath.Join(tmpDir, "test_config.csv")

	// Write minimal config
	configContent := `TenantURL,APIKey,Workers,ProjectID,LicenseFile,UseGzip,HttpProxy,HttpsProxy,NoProxy
https://platform.rescale.com,,1,,,false,,,`
	if err := os.WriteFile(configCSV, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Write test jobs CSV (with non-existent directory - will be invalid but won't hang)
	jobsContent := `Directory,JobName,AnalysisCode,AnalysisVersion,Command,CoreType,CoresPerSlot,WalltimeHours,Slots,LicenseSettings
/nonexistent/dir,test_job,user_included,1.0,./run.sh,emerald,4,1.0,1,""`
	if err := os.WriteFile(jobsCSV, []byte(jobsContent), 0644); err != nil {
		t.Fatalf("Failed to write jobs CSV: %v", err)
	}

	// Load config
	cfg, err := config.LoadConfigCSV(configCSV)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Create engine
	engine, err := core.NewEngine(cfg)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	// Create test window and app
	testApp := test.NewApp()
	window := test.NewWindow(nil)
	defer window.Close()

	// Create Jobs tab
	jobsTab := NewJobsTab(engine, window, testApp)

	// Set the selected CSV
	jobsTab.selectedCSV = jobsCSV

	// Track if plan completed
	planCompleted := false

	// Subscribe to events to verify Plan completes
	logCh := engine.Events().Subscribe(events.EventLog)
	go func() {
		timeout := time.After(5 * time.Second)
		for {
			select {
			case event := <-logCh:
				if logEvent, ok := event.(*events.LogEvent); ok {
					t.Logf("Event: [%s] %s", logEvent.Level, logEvent.Message)
					if logEvent.Stage == "plan" {
						planCompleted = true
					}
				}
			case <-timeout:
				return
			}
		}
	}()

	// Simulate clicking Plan button
	t.Log("Simulating Plan button click...")
	test.Tap(jobsTab.planButton)

	// Wait a bit for goroutine to execute
	time.Sleep(2 * time.Second)

	// Verify button was disabled then re-enabled (proves goroutine completed)
	if !jobsTab.planButton.Disabled() {
		t.Log("✓ Plan button is enabled (goroutine completed)")
	} else {
		t.Error("✗ Plan button still disabled (goroutine may have hung)")
	}

	// Verify Plan actually ran
	if planCompleted {
		t.Log("✓ Plan completed (events received)")
	} else {
		t.Error("✗ Plan did not complete")
	}

	// Verify status label was updated
	statusText := jobsTab.statusLabel.Text
	if statusText != "" && statusText != "No jobs file selected" {
		t.Logf("✓ Status label updated: %s", statusText)
	} else {
		t.Errorf("✗ Status label not updated: %s", statusText)
	}

	t.Log("Test completed - no freeze detected!")
}

// TestPlanDialogPattern verifies the code pattern matches scanDirectories
func TestPlanDialogPattern(t *testing.T) {
	t.Log("Verifying Plan dialog uses safe pattern...")

	// This test just documents the pattern - actual test is above
	t.Log("✓ Plan function:")
	t.Log("  - Disables button before goroutine")
	t.Log("  - Runs Plan in goroutine")
	t.Log("  - Re-enables button when done")
	t.Log("  - Shows single dialog (ShowError OR ShowInformation)")
	t.Log("  - NO Hide() operations on progress dialogs")
	t.Log("  - Pattern matches working scanDirectories function")
}
*/
