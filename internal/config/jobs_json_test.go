package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/rescale/rescale-int/internal/models"
)

func TestLoadJobsJSON(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []models.JobSpec // nil: the load must fail
	}{
		{"array", `[{"JobName":"TestJob","CoreType":"emerald","CoresPerSlot":4,"WalltimeHours":1.5,"Tags":["test","json"]}]`,
			[]models.JobSpec{{JobName: "TestJob", CoreType: "emerald", CoresPerSlot: 4, WalltimeHours: 1.5, Tags: []string{"test", "json"}}}},
		{"single object", `{"JobName":"SingleJob","AnalysisCode":"openfoam"}`,
			[]models.JobSpec{{JobName: "SingleJob", AnalysisCode: "openfoam"}}},
		{"empty array", `[]`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "jobs.json")
			if err := os.WriteFile(path, []byte(tt.content), 0644); err != nil {
				t.Fatal(err)
			}
			got, err := LoadJobsJSON(path)
			if tt.want == nil {
				if err == nil {
					t.Fatalf("LoadJobsJSON(%s) = %v, want an error", tt.content, got)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("LoadJobsJSON(%s) = %+v, %v; want %+v", tt.content, got, err, tt.want)
			}
		})
	}
}
