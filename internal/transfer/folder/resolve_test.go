package folder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
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

// fakeFolders is an httptest stand-in for the folder half of the API. It is the
// only way to exercise the check-then-create loop, which is the sole code here
// that writes to the account.
type fakeFolders struct {
	t *testing.T

	// children maps a folder ID to its subfolders by name. A segment already
	// present must be reused rather than duplicated.
	children map[string]map[string]string

	// failCreate names the segment whose creation fails, for the partial-failure
	// case.
	failCreate string

	created []string // Segment names actually created, in order.
}

func (f *fakeFolders) handler(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/contents/"):
		parent := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v3/folders/"), "/contents/")
		results := []map[string]any{}
		for name, id := range f.children[parent] {
			results = append(results, map[string]any{
				"type": "folder",
				"item": map[string]any{"id": id, "name": name},
			})
		}
		f.writeJSON(w, map[string]any{"results": results})

	case r.Method == http.MethodPost && strings.HasPrefix(path, "/api/v3/folders/"):
		parent := strings.Trim(strings.TrimPrefix(path, "/api/v3/folders/"), "/")
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Errorf("create body decode: %v", err)
		}
		if body.Name == f.failCreate {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		id := parent + "/" + body.Name
		if f.children[parent] == nil {
			f.children[parent] = map[string]string{}
		}
		f.children[parent][body.Name] = id
		f.created = append(f.created, body.Name)

		w.WriteHeader(http.StatusCreated)
		f.writeJSON(w, map[string]string{"id": id})

	default:
		f.t.Errorf("unexpected request: %s %s", r.Method, path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeFolders) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// newFakeFolders serves a library whose root already holds "sweeps".
func newFakeFolders(t *testing.T, failCreate string) (*fakeFolders, *api.Client) {
	t.Helper()

	fake := &fakeFolders{
		t:          t,
		children:   map[string]map[string]string{"root": {"sweeps": "root/sweeps"}},
		failCreate: failCreate,
	}

	server := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(server.Close)

	return fake, api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})
}

func TestResolveOrCreatePath_ReusesAndCreates(t *testing.T) {
	fake, client := newFakeFolders(t, "")

	got, err := ResolveOrCreatePath(context.Background(), client, "root", "sweeps/2026/run-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got != "root/sweeps/2026/run-1" {
		t.Errorf("got %q, want the deepest folder's ID", got)
	}
	// "sweeps" already existed, so only the two missing segments are created.
	if len(fake.created) != 2 || fake.created[0] != "2026" || fake.created[1] != "run-1" {
		t.Errorf("created = %v, want only the missing segments", fake.created)
	}
}

// A failure part way down leaves the segments already created in place: deleting
// them to roll back would destroy a folder that may not have been ours.
func TestResolveOrCreatePath_ReportsTheFailingSegment(t *testing.T) {
	fake, client := newFakeFolders(t, "run-1")

	_, err := ResolveOrCreatePath(context.Background(), client, "root", "sweeps/2026/run-1")
	if err == nil {
		t.Fatal("expected the failing segment to be reported")
	}
	if !strings.Contains(err.Error(), "run-1") {
		t.Errorf("error %q does not name the failing segment", err)
	}
	if len(fake.created) != 1 || fake.created[0] != "2026" {
		t.Errorf("created = %v, want the earlier segment left in place", fake.created)
	}
}
