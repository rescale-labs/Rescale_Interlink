package parser

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The summary jobs submit --script prints before it creates the job states the
// slot count the request carries. A script that sets none asks for one slot,
// and the summary said 0.
func TestSGEMetadata_StringStatesTheSlotsSent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.sh")
	if err := os.WriteFile(path, []byte(strings.Join(requiredDirectives, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := NewSGEParser().Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	req, err := m.ToJobRequest(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	if slots := req.JobAnalyses[0].Hardware.Slots; slots != 1 {
		t.Fatalf("the request carries %d slots, want 1", slots)
	}
	for summary, want := range map[string]string{
		m.String():                               "(16 cores/slot, 1 slot)",
		(&SGEMetadata{CoresPerSlot: 1}).String(): "(1 core/slot, 1 slot)",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary\n%s\ndoes not say %q", summary, want)
		}
	}
}
