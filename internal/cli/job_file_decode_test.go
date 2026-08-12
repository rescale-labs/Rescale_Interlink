package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// writeJobFile writes a job specification to a temp file and returns its path.
func writeJobFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "job.json")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write job file: %v", err)
	}
	return path
}

// TestDecodeJobFileKeepsSSHAccessFields covers the fields a --job-file used to
// lose: typed decoding dropped them, so they never reached the create call and
// the platform never saw them to accept or reject.
func TestDecodeJobFileKeepsSSHAccessFields(t *testing.T) {
	path := writeJobFile(t, `{
	  "name": "ws-job",
	  "jobanalyses": [{"command": "./run.sh",
	                   "analysis": {"code": "user_included"},
	                   "hardware": {"coreType": {"code": "emerald"}, "coresPerSlot": 1, "walltime": 4}}],
	  "cidrRule": "10.0.0.0/8",
	  "publicKey": "ssh-rsa AAAAB3NzaC1yc2E",
	  "sshPort": 32100
	}`)

	req, ignored, err := decodeJobFile(path)
	if err != nil {
		t.Fatalf("decodeJobFile: %v", err)
	}
	if len(ignored) != 0 {
		t.Errorf("ignored = %v, want none", ignored)
	}
	if req.CIDRRule != "10.0.0.0/8" {
		t.Errorf("CIDRRule = %q", req.CIDRRule)
	}
	if req.PublicKey != "ssh-rsa AAAAB3NzaC1yc2E" {
		t.Errorf("PublicKey = %q", req.PublicKey)
	}
	if req.SSHPort != 32100 {
		t.Errorf("SSHPort = %d, want 32100", req.SSHPort)
	}
}

// TestDecodeJobFileReportsIgnoredFields covers the visibility half of the fix.
// The decode still succeeds — a spec may carry fields Interlink has no reason to
// send — but the caller now learns which keys went nowhere.
func TestDecodeJobFileReportsIgnoredFields(t *testing.T) {
	path := writeJobFile(t, `{
	  "name": "ws-job",
	  "jobanalyses": [],
	  "sessionTimeout": 3600,
	  "cidrRuel": "10.0.0.0/8"
	}`)

	req, ignored, err := decodeJobFile(path)
	if err != nil {
		t.Fatalf("decodeJobFile: %v", err)
	}
	if req.Name != "ws-job" {
		t.Errorf("Name = %q, want ws-job; the decode must still succeed", req.Name)
	}
	want := []string{"cidrRuel", "sessionTimeout"}
	if len(ignored) != len(want) {
		t.Fatalf("ignored = %v, want %v", ignored, want)
	}
	for i := range want {
		if ignored[i] != want[i] {
			t.Fatalf("ignored = %v, want %v", ignored, want)
		}
	}
}

func TestDecodeJobFileErrors(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		if _, _, err := decodeJobFile(filepath.Join(t.TempDir(), "absent.json")); err == nil {
			t.Fatal("expected an error for a missing job file")
		}
	})

	t.Run("malformed JSON", func(t *testing.T) {
		if _, _, err := decodeJobFile(writeJobFile(t, `{"name":`)); err == nil {
			t.Fatal("expected an error for malformed JSON")
		}
	})
}
