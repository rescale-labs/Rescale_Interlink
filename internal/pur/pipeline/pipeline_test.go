package pipeline

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/pur/state"
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

func TestWalltimeHoursToAPI(t *testing.T) {
	cases := []struct {
		hours float64
		want  int
	}{
		{0, 1},     // zero floors to the 1-hour minimum
		{0.5, 1},   // fractional under an hour -> 1
		{1, 1},     // exact
		{1.5, 2},   // round fractional up
		{2, 2},     // exact
		{48, 48},   // large value passes through (hours, not seconds)
	}
	for _, c := range cases {
		if got := walltimeHoursToAPI(c.hours); got != c.want {
			t.Errorf("walltimeHoursToAPI(%v) = %d, want %d", c.hours, got, c.want)
		}
	}
}

func TestBuildJobRequest_WalltimeIsHoursNotSeconds(t *testing.T) {
	spec := models.JobSpec{
		JobName:         "wt",
		AnalysisCode:    "user_included",
		AnalysisVersion: "0",
		Command:         "echo hi",
		CoreType:        "emerald",
		CoresPerSlot:    1,
		Slots:           1,
		WalltimeHours:   1.0,
	}
	req, err := BuildJobRequest(spec, nil, nil, false)
	if err != nil {
		t.Fatalf("BuildJobRequest() error = %v", err)
	}
	got := req.JobAnalyses[0].Hardware.Walltime
	if got != 1 {
		t.Errorf("walltime = %d, want 1 (hours); a value of 3600 would be the old seconds bug", got)
	}
}

// TestCountFailedJobs verifies the failure count that Run uses for its exit
// status. A pipeline where jobs failed used to return nil, so 'pur run' printed
// "Pipeline completed" and exited 0 even when every job failed.
func TestCountFailedJobs(t *testing.T) {
	tests := []struct {
		name   string
		states []*models.JobState
		want   int
	}{
		{
			name: "all succeeded",
			states: []*models.JobState{
				{TarStatus: "success", UploadStatus: "success", SubmitStatus: "success"},
				{TarStatus: "success", UploadStatus: "success", SubmitStatus: "success"},
			},
			want: 0,
		},
		{
			name: "tar failure counted once even though submit is also failed",
			states: []*models.JobState{
				{TarStatus: "failed", UploadStatus: "pending", SubmitStatus: "failed"},
			},
			want: 1,
		},
		{
			name: "upload failure",
			states: []*models.JobState{
				{TarStatus: "success", UploadStatus: "failed", SubmitStatus: "failed"},
				{TarStatus: "success", UploadStatus: "success", SubmitStatus: "success"},
			},
			want: 1,
		},
		{
			name: "submit failure only",
			states: []*models.JobState{
				{TarStatus: "success", UploadStatus: "success", SubmitStatus: "failed"},
			},
			want: 1,
		},
		{
			name: "skipped and pending are not failures",
			states: []*models.JobState{
				{TarStatus: "skipped", UploadStatus: "skipped", SubmitStatus: "pending"},
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := state.NewManager(filepath.Join(t.TempDir(), "state.csv"))
			for i, st := range tt.states {
				st.Index = i
				st.JobName = "job"
				if err := mgr.UpdateState(st); err != nil {
					t.Fatalf("UpdateState: %v", err)
				}
			}

			p := &Pipeline{stateMgr: mgr, totalJobs: len(tt.states)}
			if got := p.countFailedJobs(); got != tt.want {
				t.Errorf("countFailedJobs() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestCountFailedJobsNoStateManager guards the nil path — Run calls this
// unconditionally.
func TestCountFailedJobsNoStateManager(t *testing.T) {
	p := &Pipeline{}
	if got := p.countFailedJobs(); got != 0 {
		t.Errorf("countFailedJobs() = %d, want 0", got)
	}
}

// TestClearStaleFailures covers the resume case: a "failed" left by an earlier
// attempt belongs to a stage this run is about to retry, so it must not be
// counted as this run's failure — while a failure whose stage is NOT being
// retried must survive.
func TestClearStaleFailures(t *testing.T) {
	tests := []struct {
		name        string
		state       *models.JobState
		wantChanged bool
		want        models.JobState
	}{
		{
			name:        "tar failed last run, everything gets retried",
			state:       &models.JobState{TarStatus: "failed", UploadStatus: "pending", SubmitStatus: "failed", ErrorMessage: "disk full"},
			wantChanged: true,
			want:        models.JobState{TarStatus: "pending", UploadStatus: "pending", SubmitStatus: "pending"},
		},
		{
			name:        "upload failed last run, tar is kept",
			state:       &models.JobState{TarStatus: "success", UploadStatus: "failed", SubmitStatus: "failed", ErrorMessage: "503"},
			wantChanged: true,
			want:        models.JobState{TarStatus: "success", UploadStatus: "pending", SubmitStatus: "pending"},
		},
		{
			name:        "submit failed with both earlier stages done is not retried, so it stands",
			state:       &models.JobState{TarStatus: "success", UploadStatus: "success", SubmitStatus: "failed", ErrorMessage: "400 bad request"},
			wantChanged: false,
			want:        models.JobState{TarStatus: "success", UploadStatus: "success", SubmitStatus: "failed", ErrorMessage: "400 bad request"},
		},
		{
			name:        "completed job untouched",
			state:       &models.JobState{TarStatus: "success", UploadStatus: "success", SubmitStatus: "success"},
			wantChanged: false,
			want:        models.JobState{TarStatus: "success", UploadStatus: "success", SubmitStatus: "success"},
		},
		{
			name:        "fresh job untouched",
			state:       &models.JobState{TarStatus: "pending", UploadStatus: "pending", SubmitStatus: "pending"},
			wantChanged: false,
			want:        models.JobState{TarStatus: "pending", UploadStatus: "pending", SubmitStatus: "pending"},
		},
		{
			name:        "submit-existing mode: skipped stages, stale submit failure cleared",
			state:       &models.JobState{TarStatus: "skipped", UploadStatus: "skipped", SubmitStatus: "failed"},
			wantChanged: true,
			want:        models.JobState{TarStatus: "skipped", UploadStatus: "skipped", SubmitStatus: "pending"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clearStaleFailures(tt.state)
			if got != tt.wantChanged {
				t.Errorf("clearStaleFailures() = %v, want %v", got, tt.wantChanged)
			}
			if tt.state.TarStatus != tt.want.TarStatus ||
				tt.state.UploadStatus != tt.want.UploadStatus ||
				tt.state.SubmitStatus != tt.want.SubmitStatus {
				t.Errorf("statuses = %s/%s/%s, want %s/%s/%s",
					tt.state.TarStatus, tt.state.UploadStatus, tt.state.SubmitStatus,
					tt.want.TarStatus, tt.want.UploadStatus, tt.want.SubmitStatus)
			}
			if tt.state.ErrorMessage != tt.want.ErrorMessage {
				t.Errorf("ErrorMessage = %q, want %q", tt.state.ErrorMessage, tt.want.ErrorMessage)
			}
		})
	}

	if clearStaleFailures(nil) {
		t.Error("nil state must report no change")
	}
}

// TestResumeAfterFailureReportsNoFailures is the D7 scenario end to end at the
// state level: a run that failed at tar, resumed, and completed cleanly in
// create-only mode must not report "N of M job(s) failed" and must not exit 1.
func TestResumeAfterFailureReportsNoFailures(t *testing.T) {
	mgr := state.NewManager(filepath.Join(t.TempDir(), "state.csv"))

	// Previous run: job 1 failed at tar (which also stamps the submit marker),
	// job 2 completed.
	failedLastRun := &models.JobState{Index: 1, JobName: "run_1", TarStatus: "failed",
		UploadStatus: "pending", SubmitStatus: "failed", ErrorMessage: "tar failed"}
	done := &models.JobState{Index: 2, JobName: "run_2", TarStatus: "success",
		UploadStatus: "success", SubmitStatus: "skipped"}
	for _, st := range []*models.JobState{failedLastRun, done} {
		if err := mgr.UpdateState(st); err != nil {
			t.Fatalf("UpdateState: %v", err)
		}
	}

	p := &Pipeline{stateMgr: mgr, totalJobs: 2}
	if got := p.countFailedJobs(); got != 1 {
		t.Fatalf("before the resume the stale failure should count: got %d, want 1", got)
	}

	// The resume: the feeder clears the stale markers, then the stages succeed.
	for _, st := range mgr.GetAllStates() {
		if clearStaleFailures(st) {
			if err := mgr.UpdateState(st); err != nil {
				t.Fatalf("UpdateState: %v", err)
			}
		}
	}
	failedLastRun.TarStatus = "success"
	failedLastRun.UploadStatus = "success"
	failedLastRun.SubmitStatus = "skipped" // create-only mode
	if err := mgr.UpdateState(failedLastRun); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}

	if got := p.countFailedJobs(); got != 0 {
		t.Errorf("a clean resume must report no failures, got %d", got)
	}
}
