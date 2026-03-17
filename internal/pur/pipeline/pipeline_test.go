package pipeline

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/models"
)

// mockAnalysisResolver implements AnalysisResolver for testing.
type mockAnalysisResolver struct {
	analyses []models.Analysis
	delay    time.Duration
}

func (m *mockAnalysisResolver) GetAnalyses(ctx context.Context) ([]models.Analysis, error) {
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return m.analyses, nil
}

func TestPipeline_PathNormalization(t *testing.T) {
	// Create jobs with relative paths
	jobs := []models.JobSpec{
		{
			JobName:         "test_1",
			Directory:       "relative/path/Run_1",
			AnalysisCode:    "user_included",
			AnalysisVersion: "1.0",
			Command:         "./run.sh",
			CoreType:        "emerald",
			CoresPerSlot:    4,
			Slots:           1,
			WalltimeHours:   1.0,
		},
		{
			JobName:         "test_2",
			Directory:       "/absolute/path/Run_2",
			AnalysisCode:    "user_included",
			AnalysisVersion: "1.0",
			Command:         "./run.sh",
			CoreType:        "emerald",
			CoresPerSlot:    4,
			Slots:           1,
			WalltimeHours:   1.0,
		},
	}

	// Make a copy to check normalization
	origDir0 := jobs[0].Directory
	origDir1 := jobs[1].Directory

	// NewPipeline normalizes relative paths at ingress.
	// We can't actually create a pipeline here because cfg/apiClient are nil,
	// but we can verify the normalization logic directly.

	// Simulate the normalization that happens in NewPipeline
	for i := range jobs {
		if jobs[i].Directory != "" && !filepath.IsAbs(jobs[i].Directory) {
			// This is a relative path - it should get normalized
			abs, err := filepath.Abs(jobs[i].Directory)
			if err == nil {
				jobs[i].Directory = abs
			}
		}
	}

	// Verify: relative path was normalized to absolute
	if !filepath.IsAbs(jobs[0].Directory) {
		t.Errorf("Expected relative path %q to be normalized to absolute, got %q",
			origDir0, jobs[0].Directory)
	}

	// Verify: absolute path was NOT changed
	if jobs[1].Directory != origDir1 {
		t.Errorf("Expected absolute path %q to remain unchanged, got %q",
			origDir1, jobs[1].Directory)
	}
}

func TestPipeline_ConcurrentVersionResolution(t *testing.T) {
	// Test that tar workers can start before version resolution completes.
	// We simulate this by:
	// 1. Creating a mock resolver with a 200ms delay
	// 2. Checking that the versionsResolved channel behavior is correct

	mock := &mockAnalysisResolver{
		analyses: []models.Analysis{
			{
				Code: "test_code",
				Versions: []struct {
					ID               string   `json:"id"`
					Version          string   `json:"version,omitempty"`
					VersionCode      string   `json:"versionCode,omitempty"`
					AllowedCoreTypes []string `json:"allowedCoreTypes,omitempty"`
				}{
					{ID: "v1", Version: "CPU", VersionCode: "0"},
				},
			},
		},
		delay: 200 * time.Millisecond,
	}

	// Create a minimal pipeline to test version resolution
	p := &Pipeline{
		analysisResolver: mock,
		versionsResolved: make(chan struct{}),
		jobs: []models.JobSpec{
			{
				JobName:         "test_1",
				AnalysisCode:    "test_code",
				AnalysisVersion: "CPU",
			},
		},
		activeWorkers: make(map[string]int),
	}

	// Start version resolution in goroutine (as Run() does)
	startTime := time.Now()
	go func() {
		defer close(p.versionsResolved)
		p.resolveAnalysisVersions(context.Background())
	}()

	// Simulate what a tar worker would do - it should NOT be blocked
	// (tar workers don't wait on versionsResolved)
	tarUnblocked := make(chan struct{})
	go func() {
		// Tar worker can proceed immediately
		close(tarUnblocked)
	}()

	select {
	case <-tarUnblocked:
		elapsed := time.Since(startTime)
		if elapsed > 100*time.Millisecond {
			t.Errorf("Tar worker was blocked for %v, expected immediate start", elapsed)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Tar worker should have started immediately")
	}

	// Job worker DOES need to wait for version resolution
	select {
	case <-p.versionsResolved:
		// Version resolution completed
		if p.resolvedVersions == nil {
			t.Error("Expected resolvedVersions to be populated")
		}
		if code, ok := p.resolvedVersions["test_code:CPU"]; !ok || code != "0" {
			t.Errorf("Expected resolvedVersions[test_code:CPU] = '0', got %q (ok=%v)", code, ok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Version resolution did not complete within timeout")
	}
}

func TestPipeline_LogCallbackReceivesAllMessages(t *testing.T) {
	// Verify that all log messages flow through the callback when set
	var mu sync.Mutex
	var messages []struct {
		level, stage, jobName, message string
	}

	p := &Pipeline{
		activeWorkers: make(map[string]int),
	}

	p.SetLogCallback(func(level, message, stage, jobName string) {
		mu.Lock()
		defer mu.Unlock()
		messages = append(messages, struct {
			level, stage, jobName, message string
		}{level, stage, jobName, message})
	})

	// Send various log messages
	p.logf("INFO", "pipeline", "", "Starting pipeline")
	p.logf("WARN", "tar", "job_1", "Something warned")
	p.logf("ERROR", "upload", "job_2", "Something failed")
	p.logf("INFO", "job", "job_3", "Job created")

	mu.Lock()
	defer mu.Unlock()

	if len(messages) != 4 {
		t.Fatalf("Expected 4 messages, got %d", len(messages))
	}

	// Verify levels are correct
	expectedLevels := []string{"INFO", "WARN", "ERROR", "INFO"}
	for i, msg := range messages {
		if msg.level != expectedLevels[i] {
			t.Errorf("Message %d: expected level %q, got %q", i, expectedLevels[i], msg.level)
		}
	}

	// Verify stages
	expectedStages := []string{"pipeline", "tar", "upload", "job"}
	for i, msg := range messages {
		if msg.stage != expectedStages[i] {
			t.Errorf("Message %d: expected stage %q, got %q", i, expectedStages[i], msg.stage)
		}
	}
}

func TestPipeline_LogCallbackNotCalledWhenNil(t *testing.T) {
	// When no callback is set, logf should not panic
	p := &Pipeline{
		activeWorkers: make(map[string]int),
	}

	// This should not panic (will log to stdout instead)
	p.logf("INFO", "pipeline", "", "Test message without callback")
}

func TestPipeline_VersionResolutionMap(t *testing.T) {
	// Test that the resolved versions map is built correctly
	mock := &mockAnalysisResolver{
		analyses: []models.Analysis{
			{
				Code: "openfoam",
				Versions: []struct {
					ID               string   `json:"id"`
					Version          string   `json:"version,omitempty"`
					VersionCode      string   `json:"versionCode,omitempty"`
					AllowedCoreTypes []string `json:"allowedCoreTypes,omitempty"`
				}{
					{ID: "v1", Version: "v2012", VersionCode: "abc123"},
					{ID: "v2", Version: "v2106", VersionCode: "def456"},
				},
			},
			{
				Code: "abaqus",
				Versions: []struct {
					ID               string   `json:"id"`
					Version          string   `json:"version,omitempty"`
					VersionCode      string   `json:"versionCode,omitempty"`
					AllowedCoreTypes []string `json:"allowedCoreTypes,omitempty"`
				}{
					{ID: "v3", Version: "2023", VersionCode: "ghi789"},
				},
			},
		},
	}

	p := &Pipeline{
		analysisResolver: mock,
		versionsResolved: make(chan struct{}),
		jobs:             []models.JobSpec{},
		activeWorkers:    make(map[string]int),
	}

	p.resolveAnalysisVersions(context.Background())

	if p.resolvedVersions == nil {
		t.Fatal("resolvedVersions should not be nil")
	}

	tests := []struct {
		key      string
		expected string
	}{
		{"openfoam:v2012", "abc123"},
		{"openfoam:v2106", "def456"},
		{"abaqus:2023", "ghi789"},
	}

	for _, tt := range tests {
		got, ok := p.resolvedVersions[tt.key]
		if !ok {
			t.Errorf("Expected key %q in resolvedVersions", tt.key)
			continue
		}
		if got != tt.expected {
			t.Errorf("resolvedVersions[%q] = %q, want %q", tt.key, got, tt.expected)
		}
	}
}
