package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestListAutomations_DecodesObjectEnvironmentVariables verifies that the
// automations array decodes when environmentVariables is an array of objects
// ({name, defaultValue}) rather than strings. This mirrors a real captured
// response that previously failed to decode (defaultValue may be null).
func TestListAutomations_DecodesObjectEnvironmentVariables(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/automations/" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// Bare array (not paginated), with object-shaped environmentVariables.
		w.Write([]byte(`[
			{
				"id": "ejoVk",
				"name": "Metadata Extraction Automator",
				"executeOn": "post",
				"scriptName": "extract.sh",
				"executionFrequency": 0,
				"environmentVariables": [
					{"name": "CUSTOM_FIELD_1", "defaultValue": "\"NA\""},
					{"name": "NAME", "defaultValue": null}
				],
				"analysisDependencies": ["ansys_mechanical"],
				"command": ""
			}
		]`))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	automations, err := client.ListAutomations(context.Background())
	if err != nil {
		t.Fatalf("ListAutomations() error = %v", err)
	}
	if len(automations) != 1 {
		t.Fatalf("len(automations) = %d, want 1", len(automations))
	}
	a := automations[0]
	if len(a.EnvironmentVariables) != 2 {
		t.Fatalf("len(EnvironmentVariables) = %d, want 2", len(a.EnvironmentVariables))
	}
	if a.EnvironmentVariables[0].Name != "CUSTOM_FIELD_1" {
		t.Errorf("env[0].Name = %q, want CUSTOM_FIELD_1", a.EnvironmentVariables[0].Name)
	}
	if a.EnvironmentVariables[0].DefaultValue != `"NA"` {
		t.Errorf("env[0].DefaultValue = %q, want \"NA\"", a.EnvironmentVariables[0].DefaultValue)
	}
	// null defaultValue must decode to "" without error.
	if a.EnvironmentVariables[1].Name != "NAME" || a.EnvironmentVariables[1].DefaultValue != "" {
		t.Errorf("env[1] = %+v, want {NAME, \"\"}", a.EnvironmentVariables[1])
	}
	if len(a.AnalysisDependencies) != 1 || a.AnalysisDependencies[0] != "ansys_mechanical" {
		t.Errorf("AnalysisDependencies = %v, want [ansys_mechanical]", a.AnalysisDependencies)
	}
}

// TestListAutomations_DecodesEmptyEnvironmentVariables verifies the common case
// where environmentVariables is an empty array still decodes cleanly.
func TestListAutomations_DecodesEmptyEnvironmentVariables(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"id": "a1", "name": "Simple", "executeOn": "post", "scriptName": "s.sh",
			 "executionFrequency": 0, "environmentVariables": [], "command": ""}
		]`))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	automations, err := client.ListAutomations(context.Background())
	if err != nil {
		t.Fatalf("ListAutomations() error = %v", err)
	}
	if len(automations) != 1 || len(automations[0].EnvironmentVariables) != 0 {
		t.Fatalf("got %+v, want 1 automation with empty env vars", automations)
	}
}
