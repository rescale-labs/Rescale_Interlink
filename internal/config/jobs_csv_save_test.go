package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/models"
)

// Everything a jobs CSV carries, written and read back. The comparison is
// against a whole expected spec rather than field by field, so a column the
// writer gains without a matching read shows up here instead of going unnoticed.
func TestSaveLoadRoundTrip(t *testing.T) {
	// A fully populated job and a minimal one, so the multi-row write and read
	// path is covered alongside every field.
	complete := models.JobSpec{
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
		OrgCode:               "acme",
		Tags:                  []string{"tag1", "tag2", "tag3"},
		NoDecompress:          true,
		IsLowPriority:         true,
		SubmitMode:            "create_and_submit",
		TarSubpath:            "output/results",
	}
	minimal := models.JobSpec{
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
	// Both file-scan columns are load-bearing at submit time: without
	// LocalInputFiles the scan-files -> jobs.csv -> pur run flow loses each job's
	// file list and every job falls back to archiving its whole directory, and
	// without the license feature the same flow submits jobs that quietly take
	// no license.
	fileScan := models.JobSpec{
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

	// The one value the round trip does not preserve: an unset Submit column
	// reads as "yes", which is the documented default.
	minimalLoaded := minimal
	minimalLoaded.SubmitMode = "yes"
	fileScanLoaded := fileScan
	fileScanLoaded.SubmitMode = "yes"

	tests := []struct {
		name string
		jobs []models.JobSpec
		want []models.JobSpec
	}{
		{
			name: "every field, and a second row alongside it",
			jobs: []models.JobSpec{complete, minimal},
			want: []models.JobSpec{complete, minimalLoaded},
		},
		{
			name: "the file-scan columns",
			jobs: []models.JobSpec{fileScan},
			want: []models.JobSpec{fileScanLoaded},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			csvPath := filepath.Join(t.TempDir(), "roundtrip.csv")

			if err := SaveJobsCSV(csvPath, tt.jobs); err != nil {
				t.Fatalf("SaveJobsCSV: %v", err)
			}
			loaded, err := LoadJobsCSV(csvPath)
			if err != nil {
				t.Fatalf("LoadJobsCSV: %v", err)
			}

			if !reflect.DeepEqual(loaded, tt.want) {
				t.Errorf("round trip changed the jobs:\n got %+v\nwant %+v", loaded, tt.want)
			}
		})
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

// A path the jobs CSV cannot carry has to be refused while it is being written,
// for the reasons SaveJobsCSV gives: by load time the original is gone.
func TestSaveJobsCSV_RejectsUnrepresentableInputFiles(t *testing.T) {
	tests := []struct {
		name string
		file string
	}{
		{"a semicolon splits the entry in two", filepath.Join("scratch", "a;b.inp")},
		{"an invisible character is stripped on load", filepath.Join("scratch", "case\u200b1.inp")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			csvPath := filepath.Join(t.TempDir(), "jobs.csv")
			// Seeded, so a refusal that had already truncated the file is visible.
			const existing = "previous contents"
			if err := os.WriteFile(csvPath, []byte(existing), 0644); err != nil {
				t.Fatalf("seed CSV: %v", err)
			}

			err := SaveJobsCSV(csvPath, []models.JobSpec{{
				JobName:         "case1",
				Directory:       "scratch",
				AnalysisCode:    "user_included",
				Command:         "./run.sh",
				CoreType:        "emerald",
				CoresPerSlot:    1,
				Slots:           1,
				WalltimeHours:   1.0,
				LicenseSettings: `{"LICENSE": "value"}`,
				LocalInputFiles: []string{tt.file},
			}})
			if err == nil {
				t.Fatal("SaveJobsCSV wrote a path it cannot read back")
			}
			// The job and the path, because the user has to find the row, and
			// the format that does carry it. The path is quoted, which is what
			// makes an invisible character visible in the message at all.
			for _, want := range []string{"case1", fmt.Sprintf("%q", tt.file), "JSON"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}

			data, readErr := os.ReadFile(csvPath)
			if readErr != nil {
				t.Fatalf("read CSV back: %v", readErr)
			}
			if string(data) != existing {
				t.Errorf("a refused save still rewrote the file: %q", data)
			}
		})
	}
}
