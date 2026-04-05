package analysis

import (
	"testing"
)

func TestResolveVersion_EmptyInput(t *testing.T) {
	result := ResolveVersion(nil, nil, "openfoam", "")
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

// Additional tests require a mock API client and are covered by
// integration tests and the existing cli/jobs.go test suite.
