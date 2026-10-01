package parser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/pur/parser/testsupport"
	"github.com/rescale/rescale-int/internal/reporting"
)

// fakeLookup answers the two listings ToJobRequest asks for, failing with err
// when it is set, and records what it was asked.
type fakeLookup struct {
	err   error
	asked []string
}

func (f *fakeLookup) ListProjects(context.Context) ([]api.Project, error) {
	f.asked = append(f.asked, "projects")
	return []api.Project{{ID: "PROJ0", Name: "Other"}, {ID: "PROJ1", Name: "CFD Program"}}, f.err
}

func (f *fakeLookup) GetCoreTypes(context.Context, bool) ([]models.CoreType, error) {
	f.asked = append(f.asked, "core types")
	return []models.CoreType{{Code: "emerald", Name: "Emerald", Cores: []int{2, 4, 8}}}, f.err
}

// parseCompat parses script as compat submit does, with its defaults.
func parseCompat(t *testing.T, script string) (*SGEMetadata, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "job.sh")
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	return NewSGEParser().ParseWithOptions(path, ParseOptions{CompatDefaults: true})
}

// compatRequest is the request compat submit builds from script, with lookups
// that fail: none of the scripts it is given names a project or a core type.
func compatRequest(t *testing.T, script string) *models.JobRequest {
	t.Helper()
	m, err := parseCompat(t, script)
	if err != nil {
		t.Fatalf("%q: %v", script, err)
	}
	req, err := m.ToJobRequest(context.Background(), &fakeLookup{err: errors.New("not asked for")})
	if err != nil {
		t.Fatalf("%q: %v", script, err)
	}
	return req
}

// A script written for rescale-cli builds the job rescale-cli would: each "="
// directive fills the field rescale-cli fills with it, the core type and the
// project are looked up by name, and the quotes around a value are not part of
// it.
func TestSGEParser_RescaleCLIScript(t *testing.T) {
	m, err := parseCompat(t, testsupport.RescaleCLIScript)
	if err != nil {
		t.Fatal(err)
	}
	req, err := m.ToJobRequest(context.Background(), &fakeLookup{})
	if err != nil {
		t.Fatal(err)
	}
	testsupport.CheckRequest(t, []byte(asJSON(t, req)),
		testsupport.RescaleCLIRequest("mpirun -np 8 simpleFoam -parallel", `[{"id":"FILE1","decompress":true}]`))
}

// A directive Interlink cannot honour, or a known directive whose value does
// not fit it, is refused naming its line, in either spelling, instead of being
// left out of the job.
func TestSGEParser_RefusesWhatTheJobCannotCarry(t *testing.T) {
	for _, tt := range []struct {
		lines  string // from line 2
		line   int
		reason string
	}{
		{"#RESCALE_CORE_TYPE_SET=set-1", 2, "core type set"},
		{`#RESCALE_ONDEMAND_LICENSE="seller"`, 2, "on-demand license seller"},
		{"#RESCALE_START_JOB_ON_HOUR=on", 2, "when the job starts"},
		{"#RESCALE_PRIORITY=RESERVED", 2, "ON_DEMAND or INSTANT"},
		{"#RESCALE_PRIORITY=", 2, "ON_DEMAND or INSTANT"},
		{"#RESCALE_ANALYSIS=openfoam\n#RESCALE_ANALYSIS=abaqus", 3, "one analysis"},
		{"#RESCALE_ANALYSIS_VERSION=v1\n#RESCALE_ANALYSIS_VERSION=v2", 3, "one analysis"},
		{"#RESCALE_ANALYSIS=", 2, "no value"},
		{"#RESCALE_CORES=eight", 2, "whole number"},
		{"#RESCALE_CORES=0", 2, "whole number"},
		{"#RESCALE_WALLTIME=half", 2, "whole number"},
		{"#RESCALE_INBOUND_SSH_CIDR=", 2, "profile"},
		{"#USE_RESCALE_LICENSE=false", 2, "delete the line"},
		// rescale-cli's name runs to the line's last "=".
		{"#RESCALE_ENV_A=b=c", 2, `"A=b"`},
		{"#RESCALE_ENV_=x", 2, "no name"},
		// Interlink's own spelling, where such a value used to be skipped and
		// the job ran on a default.
		{"#RESCALE_WALLTIME twelve", 2, "whole number"},
		{"#RESCALE_CORES_PER_SLOT 0", 2, "whole number"},
		{"#RESCALE_SLOTS two", 2, "whole number"},
		// Spaces in quotes are blank only for rescale-cli's count directives.
		{`#RESCALE_SLOTS="   "`, 2, "whole number"},
		{`#RESCALE_CORES_PER_SLOT="   "`, 2, "whole number"},
		{"#USE_RESCALE_LICENSE yes", 2, "true or false"},
	} {
		_, err := parseCompat(t, "#!/bin/bash\n"+tt.lines+"\n./run.sh\n")
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("at line %d", tt.line)) ||
			!strings.Contains(err.Error(), tt.reason) {
			t.Errorf("%q: error %v, want a refusal naming line %d and saying %q", tt.lines, err, tt.line, tt.reason)
		}
	}
}

// rescale-cli reads most "=" directives with nothing after "=" as no setting,
// and so does Interlink; where rescale-cli tests a value for being blank,
// spaces in quotes are blank too. The license directive is on with no value,
// or with one that says so, and an environment variable may be empty.
func TestSGEParser_RescaleCLIBlankValues(t *testing.T) {
	want := asJSON(t, compatRequest(t, "#!/bin/bash\n./run.sh\n"))
	for _, d := range []string{"NAME", "ANALYSIS_VERSION", "CORES", "CORE_TYPE", "CORE_TYPE_SET",
		"ONDEMAND_LICENSE", "WALLTIME", "AUTO_TERMINATE_CLUSTER", "START_JOB_ON_HOUR", "EXISTING_FILES",
		"LOW_PRIORITY", "PROJECT_ID", "PUBLIC_KEY", "USER_DEFINED_LICENSE_SETTINGS"} {
		if got := asJSON(t, compatRequest(t, "#!/bin/bash\n#RESCALE_"+d+"=\n./run.sh\n")); got != want {
			t.Errorf("#RESCALE_%s= with no value: request %s\nwant %s", d, got, want)
		}
	}
	for _, d := range []string{"NAME", "CORES", "CORE_TYPE", "CORE_TYPE_SET", "ONDEMAND_LICENSE", "WALLTIME",
		"AUTO_TERMINATE_CLUSTER", "START_JOB_ON_HOUR", "EXISTING_FILES", "LOW_PRIORITY",
		"USER_DEFINED_LICENSE_SETTINGS"} {
		line := "#RESCALE_" + d + `="   "`
		if got := asJSON(t, compatRequest(t, "#!/bin/bash\n"+line+"\n./run.sh\n")); got != want {
			t.Errorf("%s: request %s\nwant %s", line, got, want)
		}
	}
	for _, line := range []string{"#USE_RESCALE_LICENSE", "#USE_RESCALE_LICENSE\r", "#USE_RESCALE_LICENSE=",
		"#USE_RESCALE_LICENSE=yes", "#USE_RESCALE_LICENSE True"} {
		if req := compatRequest(t, "#!/bin/bash\n"+line+"\n./run.sh\n"); !req.JobAnalyses[0].UseRescaleLicense {
			t.Errorf("%q: useRescaleLicense false, want true", line)
		}
	}
	if got := asJSON(t, compatRequest(t, "#!/bin/bash\n#RESCALE_ENV_EMPTY=\n./run.sh\n").JobAnalyses[0].EnvVars); got != `{"EMPTY":""}` {
		t.Errorf("#RESCALE_ENV_EMPTY=: envVars %s, want {\"EMPTY\":\"\"}", got)
	}
}

// How rescale-cli reads a script beyond each directive's meaning: of a
// directive given twice in its form, the first line counts; an environment
// variable given twice is joined PATH-style; an indented directive counts; and
// the core count also comes from a #$ -pe line of any parallel environment.
// Interlink's #RESCALE_CORES names the core type where rescale-cli's
// #RESCALE_CORES= counts cores, so one script can hold both.
func TestSGEParser_RescaleCLIReading(t *testing.T) {
	m, err := parseCompat(t, `#!/bin/bash
#RESCALE_NAME="first"
#RESCALE_NAME=second
#RESCALE_ENV_PATH=/opt/a
#RESCALE_ENV_PATH="/opt/b"
#RESCALE_CORES emerald
#RESCALE_CORES=4
#RESCALE_CORES=16
  #RESCALE_WALLTIME=6
./run.sh
`)
	if err != nil {
		t.Fatal(err)
	}
	qsub, err := parseCompat(t, "#!/bin/bash\n#$ -N \"qsub job\"\n#$ -pe mpi 16 # one node\n./run.sh\n")
	if err != nil {
		t.Fatal(err)
	}
	env, err := parseCompat(t, "#!/bin/bash\n#RESCALE_ENV_A-B=x\n#RESCALE_ENV_JAVA_OPTS -Dx=1\n./run.sh\n")
	if err != nil {
		t.Fatal(err)
	}
	checkFields(t, []fieldCheck{
		{"Name", m.Name, "first"},
		{"EnvVariables[PATH]", m.EnvVariables["PATH"], "/opt/a:/opt/b"},
		{"CoreType", m.CoreType, "emerald"},
		{"CoresPerSlot", m.CoresPerSlot, 4},
		{"Walltime", m.Walltime, 6},
		{"qsub Name", qsub.Name, "qsub job"},
		{"qsub CoresPerSlot", qsub.CoresPerSlot, 16},
		// A name rescale-cli takes, hyphen and all; Interlink's own form, a
		// name and a space, keeps an "=" in its value.
		{"EnvVariables[A-B]", env.EnvVariables["A-B"], "x"},
		{"EnvVariables[JAVA_OPTS]", env.EnvVariables["JAVA_OPTS"], "-Dx=1"},
	})

	// rescale-cli takes the first #RESCALE_CORES= line that holds a count, and
	// only then a #$ -pe line.
	for _, tt := range []struct {
		lines string
		cores int
	}{
		{"#RESCALE_CORES=\n#RESCALE_CORES=8", 8},
		{`#RESCALE_CORES="  "` + "\n#RESCALE_CORES=8", 8},
		{"#RESCALE_CORES=4\n#RESCALE_CORES=16", 4},
		{"#$ -pe mpi 16\n#RESCALE_CORES=8", 8},
		{"#RESCALE_CORES=8\n#$ -pe mpi 16", 8},
		{"#RESCALE_CORES=\n#$ -pe mpi 16", 16},
	} {
		m, err := parseCompat(t, "#!/bin/bash\n"+tt.lines+"\n./run.sh\n")
		if err != nil {
			t.Errorf("%q: %v", tt.lines, err)
		} else if m.CoresPerSlot != tt.cores {
			t.Errorf("%q: cores %d, want %d", tt.lines, m.CoresPerSlot, tt.cores)
		}
	}

	// #RESCALE_PRIORITY= decides when both are given, in either order.
	for _, lines := range []string{"#RESCALE_PRIORITY=INSTANT\n#RESCALE_LOW_PRIORITY=true",
		"#RESCALE_LOW_PRIORITY=true\n#RESCALE_PRIORITY=INSTANT"} {
		m, err := parseCompat(t, "#!/bin/bash\n"+lines+"\n./run.sh\n")
		if err != nil {
			t.Errorf("%q: %v", lines, err)
		} else if m.IsLowPriority {
			t.Errorf("%q: IsLowPriority true, want false", lines)
		}
	}

	// The platform no longer reads #RESCALE_AUTO_TERMINATE_CLUSTER=, so it is
	// accepted whatever it says, nothing is sent, and a warning says so.
	want := asJSON(t, compatRequest(t, "#!/bin/bash\n./run.sh\n"))
	for _, v := range []string{"false", "True"} {
		script := "#!/bin/bash\n#RESCALE_AUTO_TERMINATE_CLUSTER=" + v + "\n./run.sh\n"
		m, err := parseCompat(t, script)
		if err != nil {
			t.Fatalf("%q: %v", v, err)
		}
		if got := asJSON(t, compatRequest(t, script)); got != want || len(m.Warnings) != 1 ||
			!strings.Contains(m.Warnings[0], "at line 2") {
			t.Errorf("=%s: request %s, warnings %q; want %s and one warning naming line 2", v, got, m.Warnings, want)
		}
	}
}

// rescale-cli's #RESCALE_PROJECT_ID= names one of the user's projects, and its
// #RESCALE_CORE_TYPE= gives a core type's code or name; the request carries
// the project's ID and the core type's code. The API is asked only for what
// the script names, and a project it does not have is refused.
func TestSGEMetadata_ToJobRequestLooksUpNames(t *testing.T) {
	for _, tt := range []struct {
		line, project, coreType string
		cores                   int
		asked                   string
	}{
		{"#RESCALE_PROJECT_ID=CFD Program", "PROJ1", "emerald", 1, "projects"},
		{"#RESCALE_PROJECT_ID PROJ9", "PROJ9", "emerald", 1, ""},
		// Of the two spellings the later line counts, and of two "=" lines the
		// first.
		{"#RESCALE_PROJECT_ID=CFD Program\n#RESCALE_PROJECT_ID PROJ9", "PROJ9", "emerald", 1, ""},
		{"#RESCALE_PROJECT_ID PROJ9\n#RESCALE_PROJECT_ID=CFD Program", "PROJ1", "emerald", 1, "projects"},
		{"#RESCALE_PROJECT_ID=CFD Program\n#RESCALE_PROJECT_ID=Other", "PROJ1", "emerald", 1, "projects"},
		{"#RESCALE_PROJECT_ID=Nope\n#RESCALE_PROJECT_ID PROJ9", "PROJ9", "emerald", 1, ""},
		// With no count, a core type the lookup finds gives its smallest, as
		// rescale-cli takes it; otherwise compat asks for one core.
		{"#RESCALE_CORE_TYPE=Emerald", "", "emerald", 2, "core types"},
		{"#RESCALE_CORE_TYPE=emerald\n#RESCALE_CORES=4", "", "emerald", 4, "core types"},
		{"#RESCALE_CORE_TYPE=emerald\n#$ -pe smp 4", "", "emerald", 4, "core types"},
		{"#RESCALE_CORE_TYPE=nickel", "", "nickel", 1, "core types"}, // unknown: sent as written, as rescale-cli does
		{"#RESCALE_CORES onyx", "", "onyx", 1, ""},
	} {
		m, err := parseCompat(t, "#!/bin/bash\n"+tt.line+"\n./run.sh\n")
		if err != nil {
			t.Fatalf("%q: %v", tt.line, err)
		}
		lookup := &fakeLookup{}
		req, err := m.ToJobRequest(context.Background(), lookup)
		if err != nil {
			t.Fatalf("%q: %v", tt.line, err)
		}
		checkFields(t, []fieldCheck{
			{tt.line + ": projectId", req.ProjectID, tt.project},
			{tt.line + ": coreType", req.JobAnalyses[0].Hardware.CoreType.Code, tt.coreType},
			{tt.line + ": coresPerSlot", req.JobAnalyses[0].Hardware.CoresPerSlot, tt.cores},
			{tt.line + ": asked", strings.Join(lookup.asked, ","), tt.asked},
		})
	}

	for _, tt := range []struct {
		line   string
		err    error
		reason string
	}{
		{"#RESCALE_PROJECT_ID=Nope", nil, `no project named "Nope"`},
		{"#RESCALE_PROJECT_ID=CFD Program", errors.New("listing failed"), "listing failed"},
		{"#RESCALE_CORE_TYPE=Emerald", errors.New("listing failed"), "listing failed"},
	} {
		m, err := parseCompat(t, "#!/bin/bash\n"+tt.line+"\n./run.sh\n")
		if err != nil {
			t.Fatalf("%q: %v", tt.line, err)
		}
		_, err = m.ToJobRequest(context.Background(), &fakeLookup{err: tt.err})
		if err == nil || !strings.Contains(err.Error(), "at line 2") || !strings.Contains(err.Error(), tt.reason) {
			t.Errorf("%q: error %v, want a refusal naming line 2 and saying %q", tt.line, err, tt.reason)
		}
		// The script's mistake is the user's to put right; a failed listing is
		// passed on, for its error report.
		if got, want := reporting.IsUsageError(err), tt.err == nil; got != want {
			t.Errorf("%q: IsUsageError(%v) = %v, want %v", tt.line, err, got, want)
		}
	}
}

// Load from SGE carries a rescale-cli script's low priority and existing files
// into the job template, the files where every run of the template attaches
// them, and refuses a project given by name, which it cannot look up, rather
// than load the job without it.
func TestSGEMetadataToJobSpec_RescaleCLIScript(t *testing.T) {
	m, err := parseCompat(t, strings.Replace(testsupport.RescaleCLIScript, "#RESCALE_PROJECT_ID=CFD Program\n", "", 1))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := SGEMetadataToJobSpec(m)
	if err != nil {
		t.Fatal(err)
	}
	checkFields(t, []fieldCheck{
		{"IsLowPriority", spec.IsLowPriority, true},
		{"ExtraInputFileIDs", spec.ExtraInputFileIDs, "FILE1"},
		{"InputFiles", len(spec.InputFiles), 0},
	})

	if m, err = parseCompat(t, testsupport.RescaleCLIScript); err != nil {
		t.Fatal(err)
	}
	if _, err := SGEMetadataToJobSpec(m); err == nil || !strings.Contains(err.Error(), "at line 12") {
		t.Errorf("error %v, want a refusal naming line 12", err)
	}
}
