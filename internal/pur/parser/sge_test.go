package parser

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/models"
)

// fieldCheck is one (got, want) pin. The conversion helpers are almost entirely
// field mappings, so they read better as a list of pins than as a wall of
// if-blocks.
type fieldCheck struct {
	name      string
	got, want any
}

func checkFields(t *testing.T, checks []fieldCheck) {
	t.Helper()
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// requiredDirectives are the six fields validate() insists on.
var requiredDirectives = []string{
	"#RESCALE_NAME test",
	"#RESCALE_COMMAND ./run.sh",
	"#RESCALE_ANALYSIS openfoam",
	"#RESCALE_CORES emerald",
	"#RESCALE_CORES_PER_SLOT 16",
	"#RESCALE_WALLTIME 3",
}

// scriptOmitting builds a script carrying every required directive except omit.
// It has no body, so dropping #RESCALE_COMMAND leaves the command genuinely
// empty instead of falling back to the script body.
func scriptOmitting(t *testing.T, omit string) string {
	t.Helper()
	lines := []string{"#!/bin/bash"}
	for _, d := range requiredDirectives {
		if !strings.HasPrefix(d, "#"+omit+" ") {
			lines = append(lines, d)
		}
	}
	if len(lines) != len(requiredDirectives) {
		t.Fatalf("scriptOmitting(%q) dropped %d directives, want exactly 1",
			omit, len(requiredDirectives)+1-len(lines))
	}
	return strings.Join(lines, "\n") + "\n"
}

func TestSGEParser_Parse(t *testing.T) {
	tests := []struct {
		name        string
		script      string
		opts        *ParseOptions // when set, ParseWithOptions is used instead of Parse
		wantErr     bool
		errContains string
		validate    func(t *testing.T, metadata *SGEMetadata)
	}{
		{
			// One full fixture for the #RESCALE_* dialect: every directive the
			// parser understands except #RESCALE_AUTOMATION (unasserted here and
			// at HEAD). TAGS is deliberately padded to pin trimming. The body is
			// two lines so an explicit #RESCALE_COMMAND provably beats the body.
			name: "all RESCALE_ directives",
			script: `#!/bin/bash
#RESCALE_NAME test_simulation
#RESCALE_COMMAND ./run.sh
#RESCALE_ANALYSIS openfoam
#RESCALE_ANALYSIS_VERSION 8.0
#RESCALE_CORES emerald
#RESCALE_CORES_PER_SLOT 16
#RESCALE_SLOTS 2
#RESCALE_WALLTIME 24
#RESCALE_TAGS  simulation  ,  cfd  ,  production
#RESCALE_PROJECT_ID proj_abc123
#RESCALE_INBOUND_SSH_CIDR 0.0.0.0/0
#RESCALE_PUBLIC_KEY ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDexample
#USE_RESCALE_LICENSE true
#RESCALE_USER_DEFINED_LICENSE_SETTINGS port=1234@server.example.com
#RESCALE_ENV_OMP_NUM_THREADS 8
#RESCALE_ENV_LD_LIBRARY_PATH /opt/lib
#RESCALE_ENV_CUSTOM_VAR myvalue

echo "Running simulation"
./run.sh
`,
			validate: func(t *testing.T, m *SGEMetadata) {
				checkFields(t, []fieldCheck{
					{"Name", m.Name, "test_simulation"},
					{"Command", m.Command, "./run.sh"},
					{"Analysis", m.Analysis, "openfoam"},
					{"AnalysisVersion", m.AnalysisVersion, "8.0"},
					{"CoreType", m.CoreType, "emerald"},
					{"CoresPerSlot", m.CoresPerSlot, 16},
					{"Slots", m.Slots, 2},
					{"Walltime", m.Walltime, 24},
					{"ProjectID", m.ProjectID, "proj_abc123"},
					{"InboundSSHCIDR", m.InboundSSHCIDR, "0.0.0.0/0"},
					{"PublicKey", m.PublicKey, "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDexample"},
					{"UseLicense", m.UseLicense, true},
					{"UserDefinedLicenseSettings", m.UserDefinedLicenseSettings, "port=1234@server.example.com"},
					{"len(EnvVariables)", len(m.EnvVariables), 3},
					{"EnvVariables[OMP_NUM_THREADS]", m.EnvVariables["OMP_NUM_THREADS"], "8"},
					{"EnvVariables[LD_LIBRARY_PATH]", m.EnvVariables["LD_LIBRARY_PATH"], "/opt/lib"},
					{"EnvVariables[CUSTOM_VAR]", m.EnvVariables["CUSTOM_VAR"], "myvalue"},
				})
				if got := strings.Join(m.Tags, "|"); got != "simulation|cfd|production" {
					t.Errorf("Tags = %q, want %q (comma-split and trimmed)", got, "simulation|cfd|production")
				}
			},
		},
		{
			// One full fixture for the #$ -l dialect: every rescale_* key, split
			// across a multi-pair line and a second line. No #RESCALE_COMMAND, so
			// the multi-line script body becomes the command.
			name: "all qsub -l keys, body becomes the command",
			script: `#!/bin/bash
#$ -l rescale_name=qsub_job,rescale_code=openfoam,rescale_coretype=emerald
#$ -l rescale_cores=16,rescale_walltime=3

echo "Setting up"
./run.sh --input data.txt
echo "Done"
`,
			validate: func(t *testing.T, m *SGEMetadata) {
				checkFields(t, []fieldCheck{
					{"Name", m.Name, "qsub_job"},
					{"Analysis", m.Analysis, "openfoam"},
					{"CoreType", m.CoreType, "emerald"},
					{"CoresPerSlot", m.CoresPerSlot, 16},
					{"Walltime", m.Walltime, 3},
					{"Command", m.Command, "echo \"Setting up\"\n./run.sh --input data.txt\necho \"Done\""},
				})
			},
		},
		{
			// #RESCALE_* wins over #$ -l regardless of the order they appear in.
			name: "mixed format, RESCALE_* takes precedence",
			script: `#!/bin/bash
#RESCALE_NAME override_name
#RESCALE_COMMAND ./override.sh
#$ -l rescale_name=qsub_name,rescale_code=openfoam
#RESCALE_ANALYSIS fluent
#RESCALE_CORES emerald
#RESCALE_CORES_PER_SLOT 32
#RESCALE_WALLTIME 7

./override.sh
`,
			validate: func(t *testing.T, m *SGEMetadata) {
				checkFields(t, []fieldCheck{
					{"Name", m.Name, "override_name"},
					{"Analysis", m.Analysis, "fluent"},
					{"Command", m.Command, "./override.sh"},
				})
			},
		},
		{
			// RESCALE_NAME overwrites unconditionally, so it wins from either side.
			name: "RESCALE_NAME before qsub -N",
			script: `#!/bin/bash
#RESCALE_NAME RescaleWins
#$ -N QsubName
#$ -l rescale_code=openfoam,rescale_coretype=emerald,rescale_cores=4,rescale_walltime=3

./run.sh
`,
			validate: func(t *testing.T, m *SGEMetadata) {
				if m.Name != "RescaleWins" {
					t.Errorf("Name = %q, want %q", m.Name, "RescaleWins")
				}
			},
		},
		{
			name: "qsub -N before RESCALE_NAME",
			script: `#!/bin/bash
#$ -N QsubName
#RESCALE_NAME RescaleWins
#$ -l rescale_code=openfoam,rescale_coretype=emerald,rescale_cores=4,rescale_walltime=3

./run.sh
`,
			validate: func(t *testing.T, m *SGEMetadata) {
				if m.Name != "RescaleWins" {
					t.Errorf("Name = %q, want %q", m.Name, "RescaleWins")
				}
			},
		},
		{
			name: "RESCALE_CORES_PER_SLOT before qsub -pe smp",
			script: `#!/bin/bash
#RESCALE_NAME PrecTest
#RESCALE_CORES_PER_SLOT 16
#$ -pe smp 8
#$ -l rescale_code=openfoam,rescale_coretype=emerald,rescale_walltime=3

./run.sh
`,
			validate: func(t *testing.T, m *SGEMetadata) {
				if m.CoresPerSlot != 16 {
					t.Errorf("CoresPerSlot = %d, want %d", m.CoresPerSlot, 16)
				}
			},
		},
		{
			name: "qsub -pe smp before RESCALE_CORES_PER_SLOT",
			script: `#!/bin/bash
#RESCALE_NAME PrecTest
#$ -pe smp 8
#RESCALE_CORES_PER_SLOT 16
#$ -l rescale_code=openfoam,rescale_coretype=emerald,rescale_walltime=3

./run.sh
`,
			validate: func(t *testing.T, m *SGEMetadata) {
				if m.CoresPerSlot != 16 {
					t.Errorf("CoresPerSlot = %d, want %d", m.CoresPerSlot, 16)
				}
			},
		},
		{
			// Other #$ directives are silently ignored rather than rejected.
			// Also the only place -N and -pe smp supply values unopposed.
			name: "unsupported qsub directives are ignored",
			script: `#!/bin/bash
#$ -A accounting_string
#$ -o /dev/null
#$ -e /dev/null
#$ -S /bin/bash
#$ -N TestJob
#$ -pe smp 2
#$ -l rescale_code=openfoam,rescale_coretype=emerald,rescale_walltime=3

./run.sh
`,
			validate: func(t *testing.T, m *SGEMetadata) {
				checkFields(t, []fieldCheck{
					{"Name", m.Name, "TestJob"},
					{"CoresPerSlot", m.CoresPerSlot, 2},
				})
			},
		},
		{
			// A non-numeric value never matches the digits-only pattern, so the
			// field stays unset and surfaces as a missing-field error.
			name: "invalid CORES_PER_SLOT value",
			script: `#!/bin/bash
#RESCALE_NAME test
#RESCALE_COMMAND ./run.sh
#RESCALE_ANALYSIS openfoam
#RESCALE_CORES emerald
#RESCALE_CORES_PER_SLOT invalid
#RESCALE_WALLTIME 3

./run.sh
`,
			wantErr:     true,
			errContains: "RESCALE_CORES_PER_SLOT",
		},
		{
			name:        "missing required NAME field",
			script:      scriptOmitting(t, "RESCALE_NAME"),
			wantErr:     true,
			errContains: "RESCALE_NAME",
		},
		{
			name:        "missing required COMMAND field",
			script:      scriptOmitting(t, "RESCALE_COMMAND"),
			wantErr:     true,
			errContains: "RESCALE_COMMAND",
		},
		{
			name:        "missing required ANALYSIS field",
			script:      scriptOmitting(t, "RESCALE_ANALYSIS"),
			wantErr:     true,
			errContains: "RESCALE_ANALYSIS",
		},
		{
			name:        "missing required CORES field",
			script:      scriptOmitting(t, "RESCALE_CORES"),
			wantErr:     true,
			errContains: "RESCALE_CORES",
		},
		{
			name:        "missing required CORES_PER_SLOT field",
			script:      scriptOmitting(t, "RESCALE_CORES_PER_SLOT"),
			wantErr:     true,
			errContains: "RESCALE_CORES_PER_SLOT",
		},
		{
			name:        "missing required WALLTIME field",
			script:      scriptOmitting(t, "RESCALE_WALLTIME"),
			wantErr:     true,
			errContains: "RESCALE_WALLTIME",
		},
		{
			name: "compat defaults fill a bare script",
			script: `#!/bin/bash
echo "hello world"
`,
			opts: &ParseOptions{CompatDefaults: true},
			validate: func(t *testing.T, m *SGEMetadata) {
				checkFields(t, []fieldCheck{
					{"Name", m.Name, "Unnamed Job"},
					{"CoreType", m.CoreType, "emerald"},
					{"CoresPerSlot", m.CoresPerSlot, 1},
					{"Walltime", m.Walltime, 48},
					{"Analysis", m.Analysis, "user_included"},
					{"Command", m.Command, `echo "hello world"`},
				})
			},
		},
		{
			// The same bare script must fail without compat defaults.
			name: "bare script without compat defaults fails",
			script: `#!/bin/bash
echo "hello world"
`,
			wantErr:     true,
			errContains: "missing required field",
		},
		{
			// Compat defaults only fill gaps; explicit directives survive.
			name: "compat defaults do not override explicit values",
			script: `#!/bin/bash
#$ -N ExplicitName
#$ -pe smp 4
#$ -l rescale_code=star-ccm,rescale_coretype=beryl,rescale_walltime=7

./solve.sh
`,
			opts: &ParseOptions{CompatDefaults: true},
			validate: func(t *testing.T, m *SGEMetadata) {
				checkFields(t, []fieldCheck{
					{"Name", m.Name, "ExplicitName"},
					{"CoresPerSlot", m.CoresPerSlot, 4},
					{"Analysis", m.Analysis, "star-ccm"},
					{"CoreType", m.CoreType, "beryl"},
					{"Walltime", m.Walltime, 7},
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary script file
			tmpDir := t.TempDir()
			scriptPath := filepath.Join(tmpDir, "test_script.sh")
			if err := os.WriteFile(scriptPath, []byte(tt.script), 0644); err != nil {
				t.Fatalf("failed to create test script: %v", err)
			}

			// Parse the script
			parser := NewSGEParser()
			var metadata *SGEMetadata
			var err error
			if tt.opts != nil {
				metadata, err = parser.ParseWithOptions(scriptPath, *tt.opts)
			} else {
				metadata, err = parser.Parse(scriptPath)
			}

			if tt.wantErr {
				if err == nil {
					t.Errorf("Parse() error = nil, wantErr %v", tt.wantErr)
				} else if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("Parse() error = %q, want error containing %q", err.Error(), tt.errContains)
				}
				return
			}

			if err != nil {
				t.Errorf("Parse() unexpected error: %v", err)
				return
			}

			if tt.validate != nil {
				tt.validate(t, metadata)
			}
		})
	}
}

// TestSGEMetadata_ToJobRequest pins the metadata -> JobRequest mapping. The two
// SSH directives are included because the parser understood them long before
// ToJobRequest carried them, so a script asking for SSH access got a job with
// none; both have to reach the create call.
func TestSGEMetadata_ToJobRequest(t *testing.T) {
	metadata := &SGEMetadata{
		Name:            "test_job",
		Command:         "./run.sh",
		Analysis:        "openfoam",
		AnalysisVersion: "8.0",
		CoreType:        "emerald",
		CoresPerSlot:    16,
		Slots:           2,
		Walltime:        24,
		Tags:            []string{"simulation", "cfd"},
		ProjectID:       "proj_123",
		InboundSSHCIDR:  "10.0.0.0/8",
		PublicKey:       "ssh-rsa AAAAB3NzaC1yc2E",
	}

	jobReq := metadata.ToJobRequest()

	if len(jobReq.JobAnalyses) != 1 {
		t.Fatalf("JobRequest.JobAnalyses length = %d, want 1", len(jobReq.JobAnalyses))
	}
	analysis := jobReq.JobAnalyses[0]

	checkFields(t, []fieldCheck{
		{"Name", jobReq.Name, metadata.Name},
		{"Analysis.Command", analysis.Command, metadata.Command},
		{"Analysis.Code", analysis.Analysis.Code, metadata.Analysis},
		{"Analysis.Version", analysis.Analysis.Version, metadata.AnalysisVersion},
		{"Hardware.CoreType", analysis.Hardware.CoreType.Code, metadata.CoreType},
		{"Hardware.CoresPerSlot", analysis.Hardware.CoresPerSlot, metadata.CoresPerSlot},
		{"Hardware.Slots", analysis.Hardware.Slots, metadata.Slots},
		{"Hardware.Walltime", analysis.Hardware.Walltime, metadata.Walltime},
		{"len(Tags)", len(jobReq.Tags), len(metadata.Tags)},
		{"ProjectID", jobReq.ProjectID, metadata.ProjectID},
		{"CIDRRule", jobReq.CIDRRule, metadata.InboundSSHCIDR},
		{"PublicKey", jobReq.PublicKey, metadata.PublicKey},
	})
}

func TestSGEMetadata_ToJobRequest_DefaultSlots(t *testing.T) {
	metadata := &SGEMetadata{
		Name:         "test_job",
		Command:      "./run.sh",
		Analysis:     "openfoam",
		CoreType:     "emerald",
		CoresPerSlot: 16,
		Slots:        0, // Not specified
		Walltime:     3,
	}

	jobReq := metadata.ToJobRequest()

	if jobReq.JobAnalyses[0].Hardware.Slots != 1 {
		t.Errorf("Default Slots = %d, want %d", jobReq.JobAnalyses[0].Hardware.Slots, 1)
	}
}

func TestSGEMetadata_String(t *testing.T) {
	metadata := &SGEMetadata{
		Name:            "test_simulation",
		Command:         "./run.sh",
		Analysis:        "openfoam",
		AnalysisVersion: "8.0",
		CoreType:        "emerald",
		CoresPerSlot:    16,
		Slots:           2,
		Walltime:        24,
		Tags:            []string{"simulation", "cfd"},
		ProjectID:       "proj_123",
		EnvVariables:    map[string]string{"OMP_NUM_THREADS": "16"},
	}

	str := metadata.String()

	expectedContains := []string{
		"test_simulation",
		"./run.sh",
		"openfoam",
		"v8.0",
		"emerald",
		"16 cores/slot",
		"2 slots",
		"24 hours",
		"simulation, cfd",
		"proj_123",
		"OMP_NUM_THREADS=16",
	}

	for _, expected := range expectedContains {
		if !strings.Contains(str, expected) {
			t.Errorf("String() output missing %q", expected)
		}
	}
}

func TestSGEMetadata_ToSGEScript(t *testing.T) {
	metadata := &SGEMetadata{
		Name:            "test_simulation",
		Command:         "./run.sh --input data.txt",
		Analysis:        "openfoam",
		AnalysisVersion: "8.0",
		CoreType:        "emerald",
		CoresPerSlot:    16,
		Slots:           2,
		Walltime:        24,
		Tags:            []string{"simulation", "cfd"},
		ProjectID:       "proj_123",
		UseLicense:      true,
		EnvVariables:    map[string]string{"OMP_NUM_THREADS": "16"},
	}

	script := metadata.ToSGEScript()

	// Check required elements
	expectedContains := []string{
		"#!/bin/bash",
		"#RESCALE_NAME test_simulation",
		"#RESCALE_ANALYSIS openfoam",
		"#RESCALE_ANALYSIS_VERSION 8.0",
		"#RESCALE_CORES emerald",
		"#RESCALE_CORES_PER_SLOT 16",
		"#RESCALE_SLOTS 2",
		"#RESCALE_WALLTIME 24",
		"#RESCALE_TAGS simulation,cfd",
		"#RESCALE_PROJECT_ID proj_123",
		"#USE_RESCALE_LICENSE true",
		"#RESCALE_ENV_OMP_NUM_THREADS 16",
		"#RESCALE_COMMAND ./run.sh --input data.txt",
		"./run.sh --input data.txt", // Command as executable body
	}

	for _, expected := range expectedContains {
		if !strings.Contains(script, expected) {
			t.Errorf("ToSGEScript() output missing %q\n\nScript:\n%s", expected, script)
		}
	}
}

func TestSGEMetadata_ToSGEScript_MinimalFields(t *testing.T) {
	metadata := &SGEMetadata{
		Name:         "minimal_job",
		Command:      "./run.sh",
		Analysis:     "openfoam",
		CoreType:     "emerald",
		CoresPerSlot: 8,
		Walltime:     3,
	}

	script := metadata.ToSGEScript()

	// Should have required fields
	expectedContains := []string{
		"#!/bin/bash",
		"#RESCALE_NAME minimal_job",
		"#RESCALE_COMMAND ./run.sh",
		"#RESCALE_ANALYSIS openfoam",
		"#RESCALE_CORES emerald",
		"#RESCALE_CORES_PER_SLOT 8",
		"#RESCALE_WALLTIME 3",
	}

	for _, expected := range expectedContains {
		if !strings.Contains(script, expected) {
			t.Errorf("ToSGEScript() output missing %q", expected)
		}
	}

	// Should NOT have optional fields when not set
	notExpected := []string{
		"#RESCALE_ANALYSIS_VERSION",
		"#RESCALE_SLOTS",
		"#RESCALE_TAGS",
		"#RESCALE_PROJECT_ID",
		"#USE_RESCALE_LICENSE",
	}

	for _, notExp := range notExpected {
		if strings.Contains(script, notExp) {
			t.Errorf("ToSGEScript() should not contain %q when not set", notExp)
		}
	}
}

// TestJobSpecToSGEMetadata pins the GUI -> script direction, including the SSH
// settings the GUI can carry.
func TestJobSpecToSGEMetadata(t *testing.T) {
	job := models.JobSpec{
		JobName:         "test_job",
		Command:         "./solve.sh",
		AnalysisCode:    "openfoam",
		AnalysisVersion: "10.0",
		CoreType:        "emerald_max",
		CoresPerSlot:    32,
		Slots:           4,
		WalltimeHours:   24.0,
		Tags:            []string{"production", "cfd"},
		ProjectID:       "proj_xyz",
		CIDRRule:        "0.0.0.0/0",
		PublicKey:       "ssh-ed25519 AAAAC3Nza",
	}

	metadata := JobSpecToSGEMetadata(job)

	checkFields(t, []fieldCheck{
		{"Name", metadata.Name, job.JobName},
		{"Command", metadata.Command, job.Command},
		{"Analysis", metadata.Analysis, job.AnalysisCode},
		{"AnalysisVersion", metadata.AnalysisVersion, job.AnalysisVersion},
		{"CoreType", metadata.CoreType, job.CoreType},
		{"CoresPerSlot", metadata.CoresPerSlot, job.CoresPerSlot},
		{"Slots", metadata.Slots, job.Slots},
		// Walltime is in hours (the Rescale API unit), not seconds.
		{"Walltime", metadata.Walltime, int(job.WalltimeHours)},
		{"len(Tags)", len(metadata.Tags), len(job.Tags)},
		{"ProjectID", metadata.ProjectID, job.ProjectID},
		{"InboundSSHCIDR", metadata.InboundSSHCIDR, job.CIDRRule},
		{"PublicKey", metadata.PublicKey, job.PublicKey},
	})
}

// TestSGEMetadataToJobSpec pins the script -> GUI direction, including the SSH
// settings, which must survive a load-then-save round trip through the job
// config.
func TestSGEMetadataToJobSpec(t *testing.T) {
	metadata := &SGEMetadata{
		Name:            "test_job",
		Command:         "./solve.sh",
		Analysis:        "openfoam",
		AnalysisVersion: "10.0",
		CoreType:        "emerald_max",
		CoresPerSlot:    32,
		Slots:           4,
		Walltime:        24, // 24 hours (Rescale API unit)
		Tags:            []string{"production", "cfd"},
		ProjectID:       "proj_xyz",
		InboundSSHCIDR:  "0.0.0.0/0",
		PublicKey:       "ssh-ed25519 AAAAC3Nza",
	}

	job := SGEMetadataToJobSpec(metadata)

	checkFields(t, []fieldCheck{
		{"JobName", job.JobName, metadata.Name},
		{"Command", job.Command, metadata.Command},
		{"AnalysisCode", job.AnalysisCode, metadata.Analysis},
		{"AnalysisVersion", job.AnalysisVersion, metadata.AnalysisVersion},
		{"CoreType", job.CoreType, metadata.CoreType},
		{"CoresPerSlot", job.CoresPerSlot, metadata.CoresPerSlot},
		{"Slots", job.Slots, metadata.Slots},
		// Walltime is in hours; no unit conversion.
		{"WalltimeHours", job.WalltimeHours, float64(metadata.Walltime)},
		{"len(Tags)", len(job.Tags), len(metadata.Tags)},
		{"ProjectID", job.ProjectID, metadata.ProjectID},
		{"CIDRRule", job.CIDRRule, metadata.InboundSSHCIDR},
		{"PublicKey", job.PublicKey, metadata.PublicKey},
	})

	// Slots and walltime are both defaulted rather than passed through as zero,
	// which would make the spec unsubmittable.
	bare := SGEMetadataToJobSpec(&SGEMetadata{})
	checkFields(t, []fieldCheck{
		{"default Slots", bare.Slots, 1},
		{"default WalltimeHours", bare.WalltimeHours, 1.0},
	})
}

func TestSGEScriptParseRoundTrip(t *testing.T) {
	// Test round-trip: SGEMetadata -> Script -> Parse -> SGEMetadata
	original := &SGEMetadata{
		Name:            "script_roundtrip",
		Command:         "./run.sh --input data.txt",
		Analysis:        "openfoam",
		AnalysisVersion: "10.0",
		CoreType:        "emerald",
		CoresPerSlot:    16,
		Slots:           2,
		Walltime:        7,
		Tags:            []string{"test", "roundtrip"},
		ProjectID:       "proj_srt",
		EnvVariables:    make(map[string]string),
	}

	script := original.ToSGEScript()

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "roundtrip.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0644); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}

	parsed, err := NewSGEParser().Parse(scriptPath)
	if err != nil {
		t.Fatalf("failed to parse generated script: %v", err)
	}

	checkFields(t, []fieldCheck{
		{"Name", parsed.Name, original.Name},
		{"Command", parsed.Command, original.Command},
		{"Analysis", parsed.Analysis, original.Analysis},
		{"AnalysisVersion", parsed.AnalysisVersion, original.AnalysisVersion},
		{"CoreType", parsed.CoreType, original.CoreType},
		{"CoresPerSlot", parsed.CoresPerSlot, original.CoresPerSlot},
		{"Slots", parsed.Slots, original.Slots},
		{"Walltime", parsed.Walltime, original.Walltime},
		{"ProjectID", parsed.ProjectID, original.ProjectID},
		{"len(Tags)", len(parsed.Tags), len(original.Tags)},
	})
}

// TestSGEParser_RejectsLegacySecondsWalltime verifies the guard that rejects
// implausibly large walltime values (in hours) that are almost certainly legacy
// seconds-style input. 3600 (1h), 7200 (2h), and 86400 (24h) as seconds become
// 150/300/3650-day jobs if interpreted as hours.
func TestSGEParser_RejectsLegacySecondsWalltime(t *testing.T) {
	parseWalltime := func(t *testing.T, wt int) error {
		t.Helper()
		script := fmt.Sprintf(`#!/bin/bash
#RESCALE_NAME wt_test
#RESCALE_COMMAND ./run.sh
#RESCALE_ANALYSIS openfoam
#RESCALE_CORES emerald
#RESCALE_CORES_PER_SLOT 8
#RESCALE_WALLTIME %d

./run.sh
`, wt)
		path := filepath.Join(t.TempDir(), "wt.sh")
		if err := os.WriteFile(path, []byte(script), 0644); err != nil {
			t.Fatalf("write script: %v", err)
		}
		_, err := NewSGEParser().Parse(path)
		return err
	}

	for _, wt := range []int{3600, 7200, 86400, 337} {
		err := parseWalltime(t, wt)
		if err == nil {
			t.Errorf("walltime %d should be rejected (looks like legacy seconds), but parsed OK", wt)
			continue
		}
		if !strings.Contains(err.Error(), "HOURS") {
			t.Errorf("walltime %d error = %q, want a 'HOURS not seconds' message", wt, err.Error())
		}
	}

	// Boundary: 336 (2 weeks) is the max allowed and must still parse.
	if err := parseWalltime(t, 336); err != nil {
		t.Errorf("walltime 336 (boundary) should parse, got error: %v", err)
	}
}
