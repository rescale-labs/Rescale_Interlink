package config

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"testing"

	"github.com/rescale/rescale-int/internal/models"
)

// Neither LicensesPerJob nor SSHPort has a meaningful zero: a license feature
// takes at least one seat, and port 0 is no port. So an unset value is written
// as an empty cell, which LoadJobsCSV reads back as unset (TestSaveLoadRoundTrip).
func TestSaveJobsCSV_LeavesUnsetCountsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.csv")
	if err := SaveJobsCSV(path, []models.JobSpec{{Directory: "./Run_1", JobName: "Run_1"}}); err != nil {
		t.Fatalf("SaveJobsCSV: %v", err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open the CSV: %v", err)
	}
	defer file.Close()
	records, err := csv.NewReader(file).ReadAll()
	if err != nil || len(records) != 2 {
		t.Fatalf("read the CSV: %d record(s), %v", len(records), err)
	}
	for i, column := range records[0] {
		if (column == "LicensesPerJob" || column == "SSHPort") && records[1][i] != "" {
			t.Errorf("%s = %q, want an empty cell", column, records[1][i])
		}
	}
}
