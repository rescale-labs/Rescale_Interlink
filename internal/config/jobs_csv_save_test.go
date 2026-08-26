package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rescale/rescale-int/internal/models"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	// Complex job with all fields
	originalJob := models.JobSpec{
		Directory:             "./Run_Complex",
		JobName:               "ComplexJob",
		AnalysisCode:          "user_included",
		AnalysisVersion:       "1.0",
		Command:               "./run.sh --verbose",
		CoreType:              "emerald",
		CoresPerSlot:          16,
		WalltimeHours:         120.5,
		Slots:                 4,
		LicenseSettings:       `{"RLM_LICENSE": "123@license-server", "FLEXLM_LICENSE": "456@flex-server"}`,
		ExtraInputFileIDs:     "file1,file2,file3",
		OnDemandLicenseSeller: "vendor",
		ProjectID:             "project123",
		Tags:                  []string{"tag1", "tag2", "tag3"},
		NoDecompress:          true,
		IsLowPriority:         true,
		SubmitMode:            "create_and_submit",
		TarSubpath:            "output/results",
	}

	// A second, simpler row keeps the multi-job write/read path covered.
	secondJob := models.JobSpec{
		Directory:       "./Run_2",
		JobName:         "Run_2",
		AnalysisCode:    "user_included",
		Command:         "./run.sh",
		CoreType:        "emerald",
		CoresPerSlot:    8,
		WalltimeHours:   2.5,
		Slots:           2,
		LicenseSettings: `{"LICENSE": "value"}`,
	}

	tmpDir := t.TempDir()
	csvPath := filepath.Join(tmpDir, "roundtrip.csv")

	// Save
	if err := SaveJobsCSV(csvPath, []models.JobSpec{originalJob, secondJob}); err != nil {
		t.Fatalf("SaveJobsCSV() failed: %v", err)
	}
	if _, err := os.Stat(csvPath); err != nil {
		t.Fatalf("SaveJobsCSV() did not create file at %s: %v", csvPath, err)
	}

	// Load
	loaded, err := LoadJobsCSV(csvPath)
	if err != nil {
		t.Fatalf("LoadJobsCSV() failed: %v", err)
	}

	if len(loaded) != 2 {
		t.Fatalf("LoadJobsCSV() loaded %d jobs, want 2", len(loaded))
	}
	if loaded[1].JobName != secondJob.JobName || loaded[1].CoresPerSlot != secondJob.CoresPerSlot {
		t.Errorf("second job = %+v, want JobName=%s CoresPerSlot=%d",
			loaded[1], secondJob.JobName, secondJob.CoresPerSlot)
	}

	reloaded := loaded[0]

	// Verify all fields
	if reloaded.Directory != originalJob.Directory {
		t.Errorf("Directory = %s, want %s", reloaded.Directory, originalJob.Directory)
	}
	if reloaded.JobName != originalJob.JobName {
		t.Errorf("JobName = %s, want %s", reloaded.JobName, originalJob.JobName)
	}
	if reloaded.AnalysisCode != originalJob.AnalysisCode {
		t.Errorf("AnalysisCode = %s, want %s", reloaded.AnalysisCode, originalJob.AnalysisCode)
	}
	if reloaded.AnalysisVersion != originalJob.AnalysisVersion {
		t.Errorf("AnalysisVersion = %s, want %s", reloaded.AnalysisVersion, originalJob.AnalysisVersion)
	}
	if reloaded.Command != originalJob.Command {
		t.Errorf("Command = %s, want %s", reloaded.Command, originalJob.Command)
	}
	if reloaded.CoreType != originalJob.CoreType {
		t.Errorf("CoreType = %s, want %s", reloaded.CoreType, originalJob.CoreType)
	}
	if reloaded.CoresPerSlot != originalJob.CoresPerSlot {
		t.Errorf("CoresPerSlot = %d, want %d", reloaded.CoresPerSlot, originalJob.CoresPerSlot)
	}
	if reloaded.WalltimeHours != originalJob.WalltimeHours {
		t.Errorf("WalltimeHours = %f, want %f", reloaded.WalltimeHours, originalJob.WalltimeHours)
	}
	if reloaded.Slots != originalJob.Slots {
		t.Errorf("Slots = %d, want %d", reloaded.Slots, originalJob.Slots)
	}
	if reloaded.ExtraInputFileIDs != originalJob.ExtraInputFileIDs {
		t.Errorf("ExtraInputFileIDs = %s, want %s", reloaded.ExtraInputFileIDs, originalJob.ExtraInputFileIDs)
	}
	if reloaded.OnDemandLicenseSeller != originalJob.OnDemandLicenseSeller {
		t.Errorf("OnDemandLicenseSeller = %s, want %s", reloaded.OnDemandLicenseSeller, originalJob.OnDemandLicenseSeller)
	}
	if reloaded.ProjectID != originalJob.ProjectID {
		t.Errorf("ProjectID = %s, want %s", reloaded.ProjectID, originalJob.ProjectID)
	}
	if reloaded.NoDecompress != originalJob.NoDecompress {
		t.Errorf("NoDecompress = %v, want %v", reloaded.NoDecompress, originalJob.NoDecompress)
	}
	if reloaded.IsLowPriority != originalJob.IsLowPriority {
		t.Errorf("IsLowPriority = %v, want %v", reloaded.IsLowPriority, originalJob.IsLowPriority)
	}

	// Tags comparison
	if len(reloaded.Tags) != len(originalJob.Tags) {
		t.Errorf("Tags length = %d, want %d", len(reloaded.Tags), len(originalJob.Tags))
	} else {
		for i := range originalJob.Tags {
			if reloaded.Tags[i] != originalJob.Tags[i] {
				t.Errorf("Tags[%d] = %s, want %s", i, reloaded.Tags[i], originalJob.Tags[i])
			}
		}
	}

	if reloaded.TarSubpath != originalJob.TarSubpath {
		t.Errorf("TarSubpath = %s, want %s", reloaded.TarSubpath, originalJob.TarSubpath)
	}
}
