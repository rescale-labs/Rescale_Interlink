package wailsapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Save as SGE and Load from SGE carry the template's license feature: the saved
// script holds it as the license directive, and loading the script gives the
// pair back. A script with more than one feature does not fit the template,
// which holds one, so loading it is refused naming the line.
func TestSGESaveLoadCarriesTheLicenseFeature(t *testing.T) {
	a := &App{}
	path := filepath.Join(t.TempDir(), "job.sh")
	job := JobSpecDTO{JobName: "lic-job", AnalysisCode: "user_included", Command: "./run.sh", CoreType: "emerald",
		CoresPerSlot: 4, WalltimeHours: 1, Slots: 1, LicenseFeatureName: "ansys_hpc", LicensesPerJob: 8}
	if err := a.SaveJobToSGE(path, job); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const directive = `#RESCALE_USER_DEFINED_LICENSE_SETTINGS {"featureSets":[{"name":"USER_SPECIFIED_0",` +
		`"features":[{"name":"ansys_hpc","count":8}]}]}`
	if !strings.Contains(string(script), directive+"\n") {
		t.Errorf("the saved script has no line\n%s\nscript:\n%s", directive, script)
	}
	loaded, err := a.LoadJobFromSGE(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LicenseFeatureName != "ansys_hpc" || loaded.LicensesPerJob != 8 {
		t.Errorf("loaded license feature %q x %d, want \"ansys_hpc\" x 8", loaded.LicenseFeatureName, loaded.LicensesPerJob)
	}

	// Half a pair, as a loaded jobs file can carry, loads back for the
	// template's validation to report; only a submit refuses it.
	job.LicensesPerJob = 0
	if err := a.SaveJobToSGE(path, job); err != nil {
		t.Fatal(err)
	}
	loaded, err = a.LoadJobFromSGE(path)
	if err != nil || loaded.LicenseFeatureName != "ansys_hpc" || loaded.LicensesPerJob != 0 {
		t.Errorf("half a pair loaded as %q x %d (%v), want \"ansys_hpc\" x 0",
			loaded.LicenseFeatureName, loaded.LicensesPerJob, err)
	}

	twoFeatures := "#!/bin/bash\n#RESCALE_NAME lic-job\n#RESCALE_COMMAND ./run.sh\n#RESCALE_ANALYSIS user_included\n" +
		"#RESCALE_CORES emerald\n#RESCALE_CORES_PER_SLOT 4\n#RESCALE_WALLTIME 1\n" +
		`#RESCALE_USER_DEFINED_LICENSE_SETTINGS={"featureSets":[{"name":"USER_SPECIFIED_0",` +
		`"features":[{"name":"ansys_hpc","count":8},{"name":"ansys_solver","count":1}]}]}` + "\n"
	if err := os.WriteFile(path, []byte(twoFeatures), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.LoadJobFromSGE(path); err == nil || !strings.Contains(err.Error(), "line 8") {
		t.Errorf("loading a script with two features: error %v, want a refusal naming line 8", err)
	}
}
