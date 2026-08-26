package wailsapp

import (
	"path/filepath"
	"testing"
)

func TestResolveSafeDownloadPath(t *testing.T) {
	const dest = "/tmp/output"

	tests := []struct {
		name    string
		rel     string
		wantErr bool
	}{
		{name: "nested path", rel: "subdir/file.txt"},
		{name: "simple filename", rel: "file.txt"},
		{name: "leading traversal is rejected", rel: "../../.ssh/authorized_keys", wantErr: true},
		{name: "traversal in the middle is rejected", rel: "subdir/../../etc/passwd", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveSafeDownloadPath(tt.rel, dest)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got %q", tt.rel, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if want := filepath.Join(dest, tt.rel); got != want {
				t.Errorf("expected %q, got %q", want, got)
			}
		})
	}
}
