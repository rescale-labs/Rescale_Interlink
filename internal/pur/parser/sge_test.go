package parser

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSGEParser_Parse(t *testing.T) {
	tests := []struct {
		name        string
		script      string
		wantErr     bool
		errContains string
		validate    func(t *testing.T, metadata *SGEMetadata)
	}{
		{
			name: "basic job with all required fields",
			script: `#!/bin/bash
#RESCALE_NAME test_simulation
#RESCALE_COMMAND ./run.sh
#RESCALE_ANALYSIS openfoam
#RESCALE_ANALYSIS_VERSION 8.0
#RESCALE_CORES emerald
#RESCALE_CORES_PER_SLOT 16
#RESCALE_SLOTS 2
#RESCALE_WALLTIME 86400

echo "Running simulation"
./run.sh
`,
			wantErr: false,
			validate: func(t *testing.T, m *SGEMetadata) {
				if m.Name != "test_simulation" {
					t.Errorf("Name = %q, want %q", m.Name, "test_simulation")
				}
				if m.Command != "./run.sh" {
					t.Errorf("Command = %q, want %q", m.Command, "./run.sh")
				}
				if m.Analysis != "openfoam" {
					t.Errorf("Analysis = %q, want %q", m.Analysis, "openfoam")
				}
				if m.AnalysisVersion != "8.0" {
					t.Errorf("AnalysisVersion = %q, want %q", m.AnalysisVersion, "8.0")
				}
				if m.CoreType != "emerald" {
					t.Errorf("CoreType = %q, want %q", m.CoreType, "emerald")
				}
				if m.CoresPerSlot != 16 {
					t.Errorf("CoresPerSlot = %d, want %d", m.CoresPerSlot, 16)
				}
				if m.Slots != 2 {
					t.Errorf("Slots = %d, want %d", m.Slots, 2)
				}
				if m.Walltime != 86400 {
					t.Errorf("Walltime = %d, want %d", m.Walltime, 86400)
				}
			},
		},
		{
			name: "job with tags and project",
			script: `#!/bin/bash
#RESCALE_NAME cfd_analysis
#RESCALE_COMMAND ./solve.sh
#RESCALE_ANALYSIS ansys-fluent
#RESCALE_CORES emerald_max
#RESCALE_CORES_PER_SLOT 32
#RESCALE_WALLTIME 7200
#RESCALE_TAGS simulation,cfd,production
#RESCALE_PROJECT_ID proj_abc123

./solve.sh
`,
			wantErr: false,
			validate: func(t *testing.T, m *SGEMetadata) {
				if len(m.Tags) != 3 {
					t.Errorf("Tags length = %d, want %d", len(m.Tags), 3)
				}
				expectedTags := []string{"simulation", "cfd", "production"}
				for i, tag := range expectedTags {
					if m.Tags[i] != tag {
						t.Errorf("Tags[%d] = %q, want %q", i, m.Tags[i], tag)
					}
				}
				if m.ProjectID != "proj_abc123" {
					t.Errorf("ProjectID = %q, want %q", m.ProjectID, "proj_abc123")
				}
			},
		},
		{
			name: "job with environment variables",
			script: `#!/bin/bash
#RESCALE_NAME env_test
#RESCALE_COMMAND ./run.sh
#RESCALE_ANALYSIS custom-solver
#RESCALE_CORES emerald
#RESCALE_CORES_PER_SLOT 8
#RESCALE_WALLTIME 3600
#RESCALE_ENV_OMP_NUM_THREADS 8
#RESCALE_ENV_LD_LIBRARY_PATH /opt/lib
#RESCALE_ENV_CUSTOM_VAR myvalue

./run.sh
`,
			wantErr: false,
			validate: func(t *testing.T, m *SGEMetadata) {
				if len(m.EnvVariables) != 3 {
					t.Errorf("EnvVariables length = %d, want %d", len(m.EnvVariables), 3)
				}
				if m.EnvVariables["OMP_NUM_THREADS"] != "8" {
					t.Errorf("OMP_NUM_THREADS = %q, want %q", m.EnvVariables["OMP_NUM_THREADS"], "8")
				}
				if m.EnvVariables["LD_LIBRARY_PATH"] != "/opt/lib" {
					t.Errorf("LD_LIBRARY_PATH = %q, want %q", m.EnvVariables["LD_LIBRARY_PATH"], "/opt/lib")
				}
				if m.EnvVariables["CUSTOM_VAR"] != "myvalue" {
					t.Errorf("CUSTOM_VAR = %q, want %q", m.EnvVariables["CUSTOM_VAR"], "myvalue")
				}
			},
		},
		{
			name: "job with SSH settings",
			script: `#!/bin/bash
#RESCALE_NAME ssh_enabled_job
#RESCALE_COMMAND ./run.sh
#RESCALE_ANALYSIS matlab
#RESCALE_CORES emerald
#RESCALE_CORES_PER_SLOT 4
#RESCALE_WALLTIME 1800
#RESCALE_INBOUND_SSH_CIDR 0.0.0.0/0
#RESCALE_PUBLIC_KEY ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDexample

./run.sh
`,
			wantErr: false,
			validate: func(t *testing.T, m *SGEMetadata) {
				if m.InboundSSHCIDR != "0.0.0.0/0" {
					t.Errorf("InboundSSHCIDR = %q, want %q", m.InboundSSHCIDR, "0.0.0.0/0")
				}
				if m.PublicKey != "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDexample" {
					t.Errorf("PublicKey = %q", m.PublicKey)
				}
			},
		},
		{
			name: "job with license settings",
			script: `#!/bin/bash
#RESCALE_NAME licensed_app
#RESCALE_COMMAND ./app
#RESCALE_ANALYSIS ansys
#RESCALE_CORES emerald
#RESCALE_CORES_PER_SLOT 16
#RESCALE_WALLTIME 3600
#USE_RESCALE_LICENSE true
#RESCALE_USER_DEFINED_LICENSE_SETTINGS port=1234@server.example.com

./app
`,
			wantErr: false,
			validate: func(t *testing.T, m *SGEMetadata) {
				if !m.UseLicense {
					t.Errorf("UseLicense = %v, want %v", m.UseLicense, true)
				}
				if m.UserDefinedLicenseSettings != "port=1234@server.example.com" {
					t.Errorf("UserDefinedLicenseSettings = %q", m.UserDefinedLicenseSettings)
				}
			},
		},
		{
			name: "missing required NAME field",
			script: `#!/bin/bash
#RESCALE_COMMAND ./run.sh
#RESCALE_ANALYSIS openfoam
#RESCALE_CORES emerald
#RESCALE_CORES_PER_SLOT 16
#RESCALE_WALLTIME 3600

./run.sh
`,
			wantErr:     true,
			errContains: "RESCALE_NAME",
		},
		{
			name: "missing required COMMAND field",
			script: `#!/bin/bash
#RESCALE_NAME test
#RESCALE_ANALYSIS openfoam
#RESCALE_CORES emerald
#RESCALE_CORES_PER_SLOT 16
#RESCALE_WALLTIME 3600

./run.sh
`,
			wantErr:     true,
			errContains: "RESCALE_COMMAND",
		},
		{
			name: "missing required ANALYSIS field",
			script: `#!/bin/bash
#RESCALE_NAME test
#RESCALE_COMMAND ./run.sh
#RESCALE_CORES emerald
#RESCALE_CORES_PER_SLOT 16
#RESCALE_WALLTIME 3600

./run.sh
`,
			wantErr:     true,
			errContains: "RESCALE_ANALYSIS",
		},
		{
			name: "missing required CORES field",
			script: `#!/bin/bash
#RESCALE_NAME test
#RESCALE_COMMAND ./run.sh
#RESCALE_ANALYSIS openfoam
#RESCALE_CORES_PER_SLOT 16
#RESCALE_WALLTIME 3600

./run.sh
`,
			wantErr:     true,
			errContains: "RESCALE_CORES",
		},
		{
			name: "missing required CORES_PER_SLOT field",
			script: `#!/bin/bash
#RESCALE_NAME test
#RESCALE_COMMAND ./run.sh
#RESCALE_ANALYSIS openfoam
#RESCALE_CORES emerald
#RESCALE_WALLTIME 3600

./run.sh
`,
			wantErr:     true,
			errContains: "RESCALE_CORES_PER_SLOT",
		},
		{
			name: "missing required WALLTIME field",
			script: `#!/bin/bash
#RESCALE_NAME test
#RESCALE_COMMAND ./run.sh
#RESCALE_ANALYSIS openfoam
#RESCALE_CORES emerald
#RESCALE_CORES_PER_SLOT 16

./run.sh
`,
			wantErr:     true,
			errContains: "RESCALE_WALLTIME",
		},
		{
			name: "invalid CORES_PER_SLOT value",
			script: `#!/bin/bash
#RESCALE_NAME test
#RESCALE_COMMAND ./run.sh
#RESCALE_ANALYSIS openfoam
#RESCALE_CORES emerald
#RESCALE_CORES_PER_SLOT invalid
#RESCALE_WALLTIME 3600

./run.sh
`,
			wantErr:     true,
			errContains: "RESCALE_CORES_PER_SLOT",
		},
		{
			name: "tags with spaces trimmed",
			script: `#!/bin/bash
#RESCALE_NAME test
#RESCALE_COMMAND ./run.sh
#RESCALE_ANALYSIS openfoam
#RESCALE_CORES emerald
#RESCALE_CORES_PER_SLOT 8
#RESCALE_WALLTIME 3600
#RESCALE_TAGS  tag1  ,  tag2  ,  tag3

./run.sh
`,
			wantErr: false,
			validate: func(t *testing.T, m *SGEMetadata) {
				if len(m.Tags) != 3 {
					t.Errorf("Tags length = %d, want %d", len(m.Tags), 3)
				}
				for i, tag := range m.Tags {
					if tag != fmt.Sprintf("tag%d", i+1) {
						t.Errorf("Tags[%d] = %q, want %q", i, tag, fmt.Sprintf("tag%d", i+1))
					}
				}
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
			metadata, err := parser.Parse(scriptPath)

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

func TestSGEMetadata_ToJobRequest(t *testing.T) {
	metadata := &SGEMetadata{
		Name:            "test_job",
		Command:         "./run.sh",
		Analysis:        "openfoam",
		AnalysisVersion: "8.0",
		CoreType:        "emerald",
		CoresPerSlot:    16,
		Slots:           2,
		Walltime:        86400,
		Tags:            []string{"simulation", "cfd"},
		ProjectID:       "proj_123",
	}

	jobReq := metadata.ToJobRequest()

	if jobReq.Name != metadata.Name {
		t.Errorf("JobRequest.Name = %q, want %q", jobReq.Name, metadata.Name)
	}

	if len(jobReq.JobAnalyses) != 1 {
		t.Fatalf("JobRequest.JobAnalyses length = %d, want 1", len(jobReq.JobAnalyses))
	}

	analysis := jobReq.JobAnalyses[0]
	if analysis.Command != metadata.Command {
		t.Errorf("Analysis.Command = %q, want %q", analysis.Command, metadata.Command)
	}
	if analysis.Analysis.Code != metadata.Analysis {
		t.Errorf("Analysis.Code = %q, want %q", analysis.Analysis.Code, metadata.Analysis)
	}
	if analysis.Analysis.Version != metadata.AnalysisVersion {
		t.Errorf("Analysis.Version = %q, want %q", analysis.Analysis.Version, metadata.AnalysisVersion)
	}
	if analysis.Hardware.CoreType.Code != metadata.CoreType {
		t.Errorf("Hardware.CoreType = %q, want %q", analysis.Hardware.CoreType.Code, metadata.CoreType)
	}
	if analysis.Hardware.CoresPerSlot != metadata.CoresPerSlot {
		t.Errorf("Hardware.CoresPerSlot = %d, want %d", analysis.Hardware.CoresPerSlot, metadata.CoresPerSlot)
	}
	// Slots are in Hardware, not JobAnalysis
	if analysis.Hardware.Slots != metadata.Slots {
		t.Errorf("Hardware.Slots = %d, want %d", analysis.Hardware.Slots, metadata.Slots)
	}
	if analysis.Hardware.Walltime != metadata.Walltime {
		t.Errorf("Hardware.Walltime = %d, want %d", analysis.Hardware.Walltime, metadata.Walltime)
	}

	if len(jobReq.Tags) != len(metadata.Tags) {
		t.Errorf("JobRequest.Tags length = %d, want %d", len(jobReq.Tags), len(metadata.Tags))
	}
	if jobReq.ProjectID != metadata.ProjectID {
		t.Errorf("JobRequest.ProjectID = %q, want %q", jobReq.ProjectID, metadata.ProjectID)
	}
}

func TestSGEMetadata_ToJobRequest_DefaultSlots(t *testing.T) {
	metadata := &SGEMetadata{
		Name:         "test_job",
		Command:      "./run.sh",
		Analysis:     "openfoam",
		CoreType:     "emerald",
		CoresPerSlot: 16,
		Slots:        0, // Not specified
		Walltime:     3600,
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
		Walltime:        86400,
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
		"86400 seconds",
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
