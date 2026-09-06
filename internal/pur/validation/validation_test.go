package validation

import (
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/models"
)

// validSpec is a job that passes every check, so a case only has to set the one
// field it is about.
func validSpec() models.JobSpec {
	return models.JobSpec{
		JobName:       "job_1",
		AnalysisCode:  "user_included",
		CoreType:      "emerald",
		Command:       "./run.sh",
		CoresPerSlot:  4,
		Slots:         1,
		WalltimeHours: 1,
	}
}

// A user-defined license needs both halves. Validating without this check
// passed a CSV that BuildJobRequest then refused once per job, after every one
// of them had already been archived and uploaded.
func TestValidateJobSpec_LicensePairNeedsBothHalves(t *testing.T) {
	for _, tt := range []struct {
		name    string
		mutate  func(*models.JobSpec)
		wantErr string
	}{
		{
			name:    "name without a count",
			mutate:  func(j *models.JobSpec) { j.LicenseFeatureName = "abaqus" },
			wantErr: `license feature "abaqus" needs a licenses-per-job count greater than zero`,
		},
		{
			name:    "count without a name",
			mutate:  func(j *models.JobSpec) { j.LicensesPerJob = 4 },
			wantErr: "licenses per job is set to 4 but no license feature name was given",
		},
		{
			// Not "> 0": the CSV reader accepts a negative integer, and reading
			// it as "unset" would drop the half-pair silently.
			name:    "negative count without a name",
			mutate:  func(j *models.JobSpec) { j.LicensesPerJob = -1 },
			wantErr: "licenses per job is set to -1 but no license feature name was given",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			job := validSpec()
			tt.mutate(&job)

			errs := ValidateJobSpec(job)
			if len(errs) == 0 {
				t.Fatalf("half a license pair was accepted")
			}
			if !slicesContain(errs, tt.wantErr) {
				t.Errorf("errors %v do not include %q", errs, tt.wantErr)
			}
		})
	}
}

func TestValidateJobSpec_CompleteLicensePairIsAccepted(t *testing.T) {
	job := validSpec()
	job.LicenseFeatureName = "abaqus"
	job.LicensesPerJob = 4

	if errs := ValidateJobSpec(job); len(errs) != 0 {
		t.Errorf("a complete license pair was rejected: %v", errs)
	}
}

func TestValidateJobSpec_NoLicenseIsAccepted(t *testing.T) {
	if errs := ValidateJobSpec(validSpec()); len(errs) != 0 {
		t.Errorf("a job with no license was rejected: %v", errs)
	}
}

// slicesContain reports whether any entry contains want, so a case can name the
// message without depending on where in the list it lands.
func slicesContain(errs []string, want string) bool {
	for _, e := range errs {
		if strings.Contains(e, want) {
			return true
		}
	}
	return false
}
