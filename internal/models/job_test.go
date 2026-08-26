package models

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestJobAutomationRequestSerialization pins the wire shape the platform
// expects: environmentVariables must appear as {} for every automation, never
// omitted and never null, or job creation is rejected.
func TestJobAutomationRequestSerialization(t *testing.T) {
	tests := []struct {
		name        string
		automations []JobAutomationRequest
		wantCount   int // occurrences of `"environmentVariables":{}`
	}{
		{
			name:        "single automation",
			automations: []JobAutomationRequest{{Automation: AutomationRef{ID: "test123"}}},
			wantCount:   1,
		},
		{
			name: "multiple automations",
			automations: []JobAutomationRequest{
				{Automation: AutomationRef{ID: "auto1"}},
				{Automation: AutomationRef{ID: "auto2"}},
			},
			wantCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := JobRequest{Name: tt.name, JobAutomations: tt.automations}
			req.NormalizeAutomations()

			data, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}
			if got := strings.Count(string(data), `"environmentVariables":{}`); got != tt.wantCount {
				t.Errorf("environmentVariables:{} count = %d, want %d in: %s", got, tt.wantCount, data)
			}
		})
	}
}

func TestNormalizeAutomations(t *testing.T) {
	tests := []struct {
		name    string
		in      []JobAutomationRequest
		wantEnv map[string]string // nil means: expect an initialized empty map
	}{
		{
			name: "nil map is initialized",
			in:   []JobAutomationRequest{{Automation: AutomationRef{ID: "a1"}, EnvironmentVariables: nil}},
		},
		{
			name: "empty map stays initialized",
			in:   []JobAutomationRequest{{Automation: AutomationRef{ID: "a1"}, EnvironmentVariables: map[string]string{}}},
		},
		{
			name:    "populated map is preserved",
			in:      []JobAutomationRequest{{Automation: AutomationRef{ID: "a1"}, EnvironmentVariables: map[string]string{"KEY": "VALUE"}}},
			wantEnv: map[string]string{"KEY": "VALUE"},
		},
		{
			name: "no automations does not panic",
			in:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := JobRequest{Name: tt.name, JobAutomations: tt.in}
			req.NormalizeAutomations()

			if len(req.JobAutomations) != len(tt.in) {
				t.Fatalf("automations = %d, want %d", len(req.JobAutomations), len(tt.in))
			}
			for i, got := range req.JobAutomations {
				if got.EnvironmentVariables == nil {
					t.Errorf("automation %d: EnvironmentVariables is nil, want an initialized map", i)
					continue
				}
				if !reflect.DeepEqual(got.EnvironmentVariables, orEmpty(tt.wantEnv)) {
					t.Errorf("automation %d: EnvironmentVariables = %v, want %v", i, got.EnvironmentVariables, orEmpty(tt.wantEnv))
				}
			}
		})
	}
}

// orEmpty treats a nil expectation as "an initialized empty map".
func orEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func TestJobAutomationRequest_NilEnvVarsSerializesToNull(t *testing.T) {
	// Without NormalizeAutomations, a nil map serializes to "null" (not omitted,
	// since we removed omitempty). This test verifies the raw behavior.
	entry := JobAutomationRequest{
		Automation:           AutomationRef{ID: "x"},
		EnvironmentVariables: nil,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	// Without omitempty, nil map marshals to "null"
	if !strings.Contains(string(data), `"environmentVariables":null`) {
		t.Errorf("expected environmentVariables:null for nil map, got: %s", string(data))
	}
}

func TestJobAutomationRequest_NestedAutomationFormat(t *testing.T) {
	// Verify the automation field serializes as nested {"id": "..."} not flat string
	req := JobAutomationRequest{
		Automation:           AutomationRef{ID: "YYnVk"},
		EnvironmentVariables: map[string]string{},
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, `"automation":{"id":"YYnVk"}`) {
		t.Errorf("expected nested automation format, got: %s", s)
	}
}

func TestJobRequest_SSHAccessFieldsSerialize(t *testing.T) {
	req := JobRequest{
		Name:      "ssh-job",
		CIDRRule:  "10.0.0.0/8,76.238.240.39/32",
		PublicKey: "ssh-rsa AAAAB3NzaC1yc2E",
		SSHPort:   22,
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	s := string(data)
	for _, want := range []string{
		`"cidrRule":"10.0.0.0/8,76.238.240.39/32"`,
		`"publicKey":"ssh-rsa AAAAB3NzaC1yc2E"`,
		`"sshPort":22`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("expected %s in payload, got: %s", want, s)
		}
	}
}

// TestJobRequest_SSHAccessFieldsOmittedWhenUnset guards existing submits: a job
// that sets none of the SSH fields must produce the same payload as before, so
// the platform keeps applying its own defaults.
func TestJobRequest_SSHAccessFieldsOmittedWhenUnset(t *testing.T) {
	data, err := json.Marshal(JobRequest{Name: "plain-job"})
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	s := string(data)
	for _, absent := range []string{"cidrRule", "publicKey", "sshPort"} {
		if strings.Contains(s, absent) {
			t.Errorf("expected %q to be omitted when unset, got: %s", absent, s)
		}
	}
}

func TestJobRequest_RoundTripSSHAccessFields(t *testing.T) {
	// A user-authored job file: the SSH fields must survive the decode that
	// previously discarded them.
	const spec = `{
	  "name": "ws",
	  "jobanalyses": [],
	  "cidrRule": "0.0.0.0/0",
	  "publicKey": "ssh-ed25519 AAAAC3Nza",
	  "sshPort": 32100
	}`
	var req JobRequest
	if err := json.Unmarshal([]byte(spec), &req); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if req.CIDRRule != "0.0.0.0/0" {
		t.Errorf("CIDRRule = %q", req.CIDRRule)
	}
	if req.PublicKey != "ssh-ed25519 AAAAC3Nza" {
		t.Errorf("PublicKey = %q", req.PublicKey)
	}
	if req.SSHPort != 32100 {
		t.Errorf("SSHPort = %d, want 32100", req.SSHPort)
	}
}

func TestUnknownJobRequestFields(t *testing.T) {
	tests := []struct {
		name string
		spec string
		want []string
	}{
		{
			name: "all keys known",
			spec: `{"name":"j","jobanalyses":[],"isLowPriority":false,"tags":["a"],
			        "projectId":"p","clusterId":"c","jobAutomations":[],
			        "cidrRule":"0.0.0.0/0","publicKey":"k","sshPort":22}`,
			want: nil,
		},
		{
			name: "unsupported and misspelled keys reported, sorted",
			spec: `{"name":"j","sessionTimeout":3600,"isInteractive":true,"cidrRuel":"x"}`,
			want: []string{"cidrRuel", "isInteractive", "sessionTimeout"},
		},
		{
			// encoding/json matches keys case-insensitively, so a differently
			// cased key IS decoded and must not be reported as ignored.
			name: "case-insensitive match is not unknown",
			spec: `{"Name":"j","ProjectID":"p","CIDRRule":"0.0.0.0/0"}`,
			want: nil,
		},
		{
			name: "nested keys are not inspected",
			spec: `{"name":"j","jobanalyses":[{"command":"c","madeUpKey":1}]}`,
			want: nil,
		},
		{
			name: "non-object input reports nothing",
			spec: `[{"name":"j"}]`,
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := UnknownJobRequestFields([]byte(tt.spec))
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}
