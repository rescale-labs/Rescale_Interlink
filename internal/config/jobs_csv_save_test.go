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

// Both columns arrived with file-scan mode and both are load-bearing at submit
// time: without LocalInputFiles the CLI's scan-files -> jobs.csv -> pur run flow
// loses each job's file list and every job falls back to archiving its whole
// directory, and without the license feature the same flow submits jobs that
// quietly take no license.
func TestSaveLoadRoundTrip_FileScanColumns(t *testing.T) {
	original := models.JobSpec{
		Directory:       filepath.Join("scratch", "inputs"),
		JobName:         "case1",
		AnalysisCode:    "user_included",
		Command:         "abaqus job=case1 input=case1.inp",
		CoreType:        "emerald",
		CoresPerSlot:    4,
		WalltimeHours:   1.0,
		Slots:           1,
		LicenseSettings: `{"LICENSE": "value"}`,
		LocalInputFiles: []string{
			filepath.Join("scratch", "inputs", "case1.inp"),
			filepath.Join("scratch", "inputs", "case1.mesh"),
			// Outside Directory, which is the whole point of the field.
			filepath.Join("scratch", "meshes", "case1.cfg"),
		},
		LicenseFeatureName: "ansys_hpc",
		LicensesPerJob:     8,
	}

	csvPath := filepath.Join(t.TempDir(), "filescan.csv")
	if err := SaveJobsCSV(csvPath, []models.JobSpec{original}); err != nil {
		t.Fatalf("SaveJobsCSV() failed: %v", err)
	}

	loaded, err := LoadJobsCSV(csvPath)
	if err != nil {
		t.Fatalf("LoadJobsCSV() failed: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded %d jobs, want 1", len(loaded))
	}

	got := loaded[0].LocalInputFiles
	if len(got) != len(original.LocalInputFiles) {
		t.Fatalf("LocalInputFiles = %v, want %v", got, original.LocalInputFiles)
	}
	for i, want := range original.LocalInputFiles {
		if got[i] != want {
			t.Errorf("LocalInputFiles[%d] = %s, want %s", i, got[i], want)
		}
	}
	if loaded[0].LicenseFeatureName != original.LicenseFeatureName {
		t.Errorf("LicenseFeatureName = %q, want %q", loaded[0].LicenseFeatureName, original.LicenseFeatureName)
	}
	if loaded[0].LicensesPerJob != original.LicensesPerJob {
		t.Errorf("LicensesPerJob = %d, want %d", loaded[0].LicensesPerJob, original.LicensesPerJob)
	}
}

// A CSV written before the columns existed must still load, since users keep
// their jobs.csv files around.
func TestLoadJobsCSV_WithoutFileScanColumns(t *testing.T) {
	csvPath := filepath.Join(t.TempDir(), "legacy.csv")
	content := "Directory,JobName,AnalysisCode,Command,CoreType,CoresPerSlot,WalltimeHours,Slots,LicenseSettings\n" +
		"./Run_1,Run_1,user_included,./run.sh,emerald,4,1.0,1,\"{\"\"LICENSE\"\": \"\"value\"\"}\"\n"

	if err := os.WriteFile(csvPath, []byte(content), 0644); err != nil {
		t.Fatalf("write CSV: %v", err)
	}

	loaded, err := LoadJobsCSV(csvPath)
	if err != nil {
		t.Fatalf("LoadJobsCSV() failed on a CSV without the columns: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded %d jobs, want 1", len(loaded))
	}
	if len(loaded[0].LocalInputFiles) != 0 {
		t.Errorf("LocalInputFiles = %v, want empty", loaded[0].LocalInputFiles)
	}
	if loaded[0].LicenseFeatureName != "" || loaded[0].LicensesPerJob != 0 {
		t.Errorf("license feature = %q x %d, want empty",
			loaded[0].LicenseFeatureName, loaded[0].LicensesPerJob)
	}
}
