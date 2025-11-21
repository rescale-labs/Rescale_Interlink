package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rescale/rescale-int/internal/models"
)

func TestSaveJobsCSV(t *testing.T) {
	tests := []struct {
		name    string
		jobs    []models.JobSpec
		wantErr bool
	}{
		{
			name: "single job",
			jobs: []models.JobSpec{
				{
					Directory:       "./Run_1",
					JobName:         "Run_1",
					AnalysisCode:    "powerflow",
					AnalysisVersion: "6-2024-hf1 Intel MPI 2021.13",
					Command:         "pf2ens -c _Moving_Belt_CSYS -v p:Cp,pt:Cp Run_1.avg.fnc",
					CoreType:        "calcitev2",
					CoresPerSlot:    4,
					WalltimeHours:   48.0,
					Slots:           1,
					LicenseSettings: `{"RLM_LICENSE": "123@license-server"}`,
					SubmitMode:      "create_and_submit",
					Tags:            []string{"pur_test"},
				},
			},
			wantErr: false,
		},
		{
			name: "multiple jobs",
			jobs: []models.JobSpec{
				{
					Directory:       "./Run_1",
					JobName:         "Run_1",
					AnalysisCode:    "user_included",
					Command:         "./run.sh",
					CoreType:        "emerald",
					CoresPerSlot:    4,
					WalltimeHours:   1.0,
					Slots:           1,
					LicenseSettings: `{"LICENSE": "value"}`,
				},
				{
					Directory:       "./Run_2",
					JobName:         "Run_2",
					AnalysisCode:    "user_included",
					Command:         "./run.sh",
					CoreType:        "emerald",
					CoresPerSlot:    8,
					WalltimeHours:   2.5,
					Slots:           2,
					LicenseSettings: `{"LICENSE": "value"}`,
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp file
			tmpDir := t.TempDir()
			csvPath := filepath.Join(tmpDir, "test_jobs.csv")

			// Save
			err := SaveJobsCSV(csvPath, tt.jobs)
			if (err != nil) != tt.wantErr {
				t.Fatalf("SaveJobsCSV() error = %v, wantErr %v", err, tt.wantErr)
			}

			if tt.wantErr {
				return
			}

			// Verify file exists
			if _, err := os.Stat(csvPath); os.IsNotExist(err) {
				t.Fatalf("SaveJobsCSV() did not create file at %s", csvPath)
			}

			// Load it back
			loaded, err := LoadJobsCSV(csvPath)
			if err != nil {
				t.Fatalf("LoadJobsCSV() failed to load saved CSV: %v", err)
			}

			// Verify count
			if len(loaded) != len(tt.jobs) {
				t.Fatalf("LoadJobsCSV() loaded %d jobs, want %d", len(loaded), len(tt.jobs))
			}

			// Verify key fields of first job
			if len(loaded) > 0 {
				original := tt.jobs[0]
				reloaded := loaded[0]

				if reloaded.JobName != original.JobName {
					t.Errorf("JobName = %s, want %s", reloaded.JobName, original.JobName)
				}
				if reloaded.Directory != original.Directory {
					t.Errorf("Directory = %s, want %s", reloaded.Directory, original.Directory)
				}
				if reloaded.AnalysisCode != original.AnalysisCode {
					t.Errorf("AnalysisCode = %s, want %s", reloaded.AnalysisCode, original.AnalysisCode)
				}
				if reloaded.CoreType != original.CoreType {
					t.Errorf("CoreType = %s, want %s", reloaded.CoreType, original.CoreType)
				}
				if reloaded.CoresPerSlot != original.CoresPerSlot {
					t.Errorf("CoresPerSlot = %d, want %d", reloaded.CoresPerSlot, original.CoresPerSlot)
				}
				if reloaded.WalltimeHours != original.WalltimeHours {
					t.Errorf("WalltimeHours = %f, want %f", reloaded.WalltimeHours, original.WalltimeHours)
				}
				if reloaded.Slots != original.Slots {
					t.Errorf("Slots = %d, want %d", reloaded.Slots, original.Slots)
				}
			}
		})
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	// Complex job with all fields
	originalJob := models.JobSpec{
		Directory:             "./Run_Complex",
		JobName:               "ComplexJob",
		AnalysisCode:          "powerflow",
		AnalysisVersion:       "6-2024-hf1 Intel MPI 2021.13",
		Command:               "pf2ens -c _Moving_Belt_CSYS -v p:Cp,pt:Cp Run_1.avg.fnc",
		CoreType:              "calcitev2",
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
	}

	tmpDir := t.TempDir()
	csvPath := filepath.Join(tmpDir, "roundtrip.csv")

	// Save
	if err := SaveJobsCSV(csvPath, []models.JobSpec{originalJob}); err != nil {
		t.Fatalf("SaveJobsCSV() failed: %v", err)
	}

	// Load
	loaded, err := LoadJobsCSV(csvPath)
	if err != nil {
		t.Fatalf("LoadJobsCSV() failed: %v", err)
	}

	if len(loaded) != 1 {
		t.Fatalf("LoadJobsCSV() loaded %d jobs, want 1", len(loaded))
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
}
