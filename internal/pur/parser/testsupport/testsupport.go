// Package testsupport holds the fixtures that the SGE parser's tests and the
// tests of the two commands that submit SGE scripts share: a job script
// written for rescale-cli, the job request it makes, and a field-by-field
// comparison of job create requests. Stated here once instead of once per
// package.
package testsupport

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"testing"
)

// RescaleCLIScript is a job script written for rescale-cli: each directive in
// its "=" form, a value in quotes, the core type and the project by name, and
// the command in the script body.
const RescaleCLIScript = `#!/bin/bash
#RESCALE_NAME="Wing study"
#RESCALE_ANALYSIS=openfoam
#RESCALE_ANALYSIS_VERSION=v2312
#RESCALE_CORE_TYPE=Emerald
#RESCALE_CORES=8
#RESCALE_WALLTIME=12
#RESCALE_ENV_OMP_NUM_THREADS=8
#RESCALE_ENV_LM_LICENSE_FILE="27000@license.example.com"
#RESCALE_EXISTING_FILES=FILE1
#RESCALE_LOW_PRIORITY=true
#RESCALE_PROJECT_ID=CFD Program

mpirun -np 8 simpleFoam -parallel
`

// RescaleCLIRequest is the job create request RescaleCLIScript makes, given the
// command and the input files, which differ between the two commands.
func RescaleCLIRequest(command, inputFiles string) string {
	return fmt.Sprintf(`{
		"name": "Wing study",
		"isLowPriority": true,
		"projectId": "PROJ1",
		"jobanalyses": [{
			"command": %q,
			"analysis": {"code": "openfoam", "version": "v2312"},
			"hardware": {"coreType": {"code": "emerald"}, "coresPerSlot": 8, "slots": 1, "walltime": 12},
			"envVars": {"OMP_NUM_THREADS": "8", "LM_LICENSE_FILE": "27000@license.example.com"},
			"inputFiles": %s,
			"useRescaleLicense": false,
			"onDemandLicenseSeller": null,
			"userDefinedLicenseSettings": null
		}]
	}`, command, inputFiles)
}

// RequestFields flattens a job create's body into its fields and those of its
// one job analysis, each as canonical JSON.
func RequestFields(t *testing.T, body []byte) map[string]string {
	t.Helper()
	var req map[string]json.RawMessage
	var analyses []map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	if err := json.Unmarshal(req["jobanalyses"], &analyses); err != nil || len(analyses) != 1 {
		t.Fatalf("jobanalyses %s: %v, want one", req["jobanalyses"], err)
	}
	delete(req, "jobanalyses")
	fields := map[string]string{}
	add := func(prefix string, m map[string]json.RawMessage) {
		for k, raw := range m {
			var v any
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Fatalf("%s: %v", k, err)
			}
			canonical, _ := json.Marshal(v)
			fields[prefix+k] = string(canonical)
		}
	}
	add("", req)
	add("jobanalyses[0].", analyses[0])
	return fields
}

// CheckRequest compares a job create's body with want field by field, absent
// fields included.
func CheckRequest(t *testing.T, body []byte, want string) {
	t.Helper()
	got, wantFields := RequestFields(t, body), RequestFields(t, []byte(want))
	for k := range got {
		if _, ok := wantFields[k]; !ok {
			wantFields[k] = "(absent)"
		}
	}
	for _, k := range slices.Sorted(maps.Keys(wantFields)) {
		if g := cmp.Or(got[k], "(absent)"); g != wantFields[k] {
			t.Errorf("%s = %s, want %s", k, g, wantFields[k])
		}
	}
}
