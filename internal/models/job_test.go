package models

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestJobAutomationRequest_Serialization(t *testing.T) {
	req := JobRequest{
		Name: "test-job",
		JobAutomations: []JobAutomationRequest{
			{Automation: AutomationRef{ID: "test123"}},
		},
	}
	req.NormalizeAutomations()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	// Verify "environmentVariables":{} is present, not omitted or null
	if !strings.Contains(string(data), `"environmentVariables":{}`) {
		t.Errorf("expected environmentVariables:{}, got: %s", string(data))
	}
}

func TestJobAutomationRequest_SerializationMultiple(t *testing.T) {
	req := JobRequest{
		Name: "test-job-multi",
		JobAutomations: []JobAutomationRequest{
			{Automation: AutomationRef{ID: "auto1"}},
			{Automation: AutomationRef{ID: "auto2"}},
		},
	}
	req.NormalizeAutomations()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	s := string(data)
	// Both automations should have environmentVariables:{}
	count := strings.Count(s, `"environmentVariables":{}`)
	if count != 2 {
		t.Errorf("expected 2 environmentVariables:{} entries, got %d in: %s", count, s)
	}
}

func TestNormalizeAutomations_NilMap(t *testing.T) {
	req := JobRequest{
		JobAutomations: []JobAutomationRequest{
			{Automation: AutomationRef{ID: "a1"}, EnvironmentVariables: nil},
		},
	}
	req.NormalizeAutomations()
	if req.JobAutomations[0].EnvironmentVariables == nil {
		t.Error("expected EnvironmentVariables to be initialized, got nil")
	}
	if len(req.JobAutomations[0].EnvironmentVariables) != 0 {
		t.Error("expected empty map, got non-empty")
	}
}

func TestNormalizeAutomations_EmptyMap(t *testing.T) {
	req := JobRequest{
		JobAutomations: []JobAutomationRequest{
			{Automation: AutomationRef{ID: "a1"}, EnvironmentVariables: map[string]string{}},
		},
	}
	req.NormalizeAutomations()
	if req.JobAutomations[0].EnvironmentVariables == nil {
		t.Error("expected EnvironmentVariables to remain initialized")
	}
	if len(req.JobAutomations[0].EnvironmentVariables) != 0 {
		t.Error("expected empty map, got non-empty")
	}
}

func TestNormalizeAutomations_PopulatedMap(t *testing.T) {
	req := JobRequest{
		JobAutomations: []JobAutomationRequest{
			{
				Automation:           AutomationRef{ID: "a1"},
				EnvironmentVariables: map[string]string{"KEY": "VALUE"},
			},
		},
	}
	req.NormalizeAutomations()
	if req.JobAutomations[0].EnvironmentVariables["KEY"] != "VALUE" {
		t.Error("expected populated map to be preserved")
	}
}

func TestNormalizeAutomations_NoAutomations(t *testing.T) {
	req := JobRequest{
		Name: "no-automations",
	}
	req.NormalizeAutomations() // Should not panic
	if len(req.JobAutomations) != 0 {
		t.Error("expected empty automations slice")
	}
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
