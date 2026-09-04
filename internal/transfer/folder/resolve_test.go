package folder

import (
	"context"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
)

// noopClient is a zero-value client used only to satisfy the non-nil check in
// ResolveOrCreatePath for cases that return before making any request.
var noopClient api.Client

func TestSplitFolderPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want []string
	}{
		{"empty", "", nil},
		{"single", "sweeps", []string{"sweeps"}},
		{"nested", "sweeps/alpha-beta", []string{"sweeps", "alpha-beta"}},
		{"backslashes", `sweeps\alpha-beta`, []string{"sweeps", "alpha-beta"}},
		{"mixed separators", `sweeps/2026\run-1`, []string{"sweeps", "2026", "run-1"}},
		{"leading and trailing slashes", "/sweeps/alpha/", []string{"sweeps", "alpha"}},
		{"collapses repeats", "sweeps///alpha", []string{"sweeps", "alpha"}},
		{"drops dot segments", "./sweeps/./alpha", []string{"sweeps", "alpha"}},
		{"trims whitespace", " sweeps / alpha ", []string{"sweeps", "alpha"}},
		{"only separators", "///", nil},
		{"spaces in name preserved", "my sweeps/case a", []string{"my sweeps", "case a"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SplitFolderPath(tt.path)
			if err != nil {
				t.Fatalf("SplitFolderPath(%q) returned error: %v", tt.path, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("SplitFolderPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("SplitFolderPath(%q)[%d] = %q, want %q", tt.path, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSplitFolderPath_RejectsParentSegments(t *testing.T) {
	for _, path := range []string{"..", "../sweeps", "sweeps/../alpha", `sweeps\..\alpha`} {
		got, err := SplitFolderPath(path)
		if err == nil {
			t.Errorf("SplitFolderPath(%q) = %v, want error", path, got)
			continue
		}
		if !strings.Contains(err.Error(), "..") {
			t.Errorf("SplitFolderPath(%q) error = %q, want it to mention %q", path, err, "..")
		}
	}
}

func TestResolveOrCreatePath_RequiresAPIClient(t *testing.T) {
	if _, err := ResolveOrCreatePath(context.Background(), nil, "parent-id", "sweeps"); err == nil {
		t.Error("expected error when api client is nil")
	}
}

// A path with no segments needs no API calls, so an explicit parent passes
// straight through. This lets callers forward user input unconditionally.
func TestResolveOrCreatePath_EmptyPathReturnsParent(t *testing.T) {
	got, err := ResolveOrCreatePath(context.Background(), &noopClient, "parent-id", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "parent-id" {
		t.Errorf("got %q, want %q", got, "parent-id")
	}
}

func TestResolveOrCreatePath_RejectsBadPathBeforeAPICalls(t *testing.T) {
	// A non-empty parent means no GetRootFolders call, and the path is rejected
	// before any folder lookup, so the zero-value client is never dialed.
	if _, err := ResolveOrCreatePath(context.Background(), &noopClient, "parent-id", "../escape"); err == nil {
		t.Error("expected error for a path containing \"..\"")
	}
}
