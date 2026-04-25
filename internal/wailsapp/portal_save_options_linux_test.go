//go:build linux

package wailsapp

import (
	"errors"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// TestBuildPortalSaveOptions verifies the option map produced for
// FileChooser.SaveFile matches the portal spec: current_name is 's',
// filters is a(sa(us)) with each Wails pattern split on ';' and
// trimmed. Empty defaults produce no key.
func TestBuildPortalSaveOptions(t *testing.T) {
	t.Run("current_name is type s and populated", func(t *testing.T) {
		opts := buildPortalSaveOptions("my-file.json", nil)
		v, ok := opts["current_name"]
		if !ok {
			t.Fatal("missing current_name key")
		}
		if sig := v.Signature().String(); sig != "s" {
			t.Errorf("current_name signature = %q, want %q", sig, "s")
		}
		if got, _ := v.Value().(string); got != "my-file.json" {
			t.Errorf("current_name value = %q, want %q", got, "my-file.json")
		}
	})

	t.Run("empty default name produces no current_name key", func(t *testing.T) {
		opts := buildPortalSaveOptions("", nil)
		if _, ok := opts["current_name"]; ok {
			t.Error("current_name present despite empty default")
		}
	})

	t.Run("filters has signature a(sa(us)) and splits semicolons", func(t *testing.T) {
		filters := []runtime.FileFilter{
			{DisplayName: "JSON (*.json)", Pattern: "*.json"},
			{DisplayName: "Text files", Pattern: "*.txt ; *.log"},
		}
		opts := buildPortalSaveOptions("", filters)
		v, ok := opts["filters"]
		if !ok {
			t.Fatal("missing filters key")
		}
		// a(sa(us)) is how the portal spec describes the filters list.
		if sig := v.Signature().String(); sig != "a(sa(us))" {
			t.Errorf("filters signature = %q, want %q", sig, "a(sa(us))")
		}
		tuples, ok := v.Value().([]filterTuple)
		if !ok {
			t.Fatalf("filters value type = %T, want []filterTuple", v.Value())
		}
		if len(tuples) != 2 {
			t.Fatalf("len(filters) = %d, want 2", len(tuples))
		}
		if tuples[1].DisplayName != "Text files" {
			t.Errorf("second filter name = %q", tuples[1].DisplayName)
		}
		if len(tuples[1].Patterns) != 2 {
			t.Errorf("semicolon-split yielded %d patterns, want 2", len(tuples[1].Patterns))
		}
		if tuples[1].Patterns[0].Pattern != "*.txt" || tuples[1].Patterns[1].Pattern != "*.log" {
			t.Errorf("split patterns = %+v, want [*.txt *.log]", tuples[1].Patterns)
		}
		// Every pattern is type 0 (glob).
		for _, tup := range tuples {
			for _, p := range tup.Patterns {
				if p.Type != 0 {
					t.Errorf("pattern %q type = %d, want 0 (glob)", p.Pattern, p.Type)
				}
			}
		}
	})

	t.Run("empty filters list produces no filters key", func(t *testing.T) {
		opts := buildPortalSaveOptions("", nil)
		if _, ok := opts["filters"]; ok {
			t.Error("filters key present for nil filters")
		}
		opts2 := buildPortalSaveOptions("", []runtime.FileFilter{})
		if _, ok := opts2["filters"]; ok {
			t.Error("filters key present for empty filters slice")
		}
	})

	t.Run("filter whose pattern is only a semicolon produces no filter entry", func(t *testing.T) {
		filters := []runtime.FileFilter{{DisplayName: "bad", Pattern: " ; "}}
		opts := buildPortalSaveOptions("", filters)
		if _, ok := opts["filters"]; ok {
			t.Error("filters key added despite all patterns being empty")
		}
	})
}

// TestIsPortalUnavailable_classification — each classified dbus.Error
// name returns true; generic and timeout-shaped errors return false.
func TestIsPortalUnavailable_classification(t *testing.T) {
	unavailable := []string{
		"org.freedesktop.DBus.Error.ServiceUnknown",
		"org.freedesktop.DBus.Error.UnknownMethod",
		"org.freedesktop.DBus.Error.UnknownInterface",
		"org.freedesktop.DBus.Error.UnknownObject",
		"org.freedesktop.DBus.Error.NoReply",
		"org.freedesktop.DBus.Error.NotSupported",
		"org.freedesktop.DBus.Error.Spawn.ServiceNotFound",
	}
	for _, name := range unavailable {
		err := dbus.Error{Name: name}
		if !isPortalUnavailable(err) {
			t.Errorf("isPortalUnavailable(%q) = false, want true", name)
		}
	}

	notUnavailable := []struct {
		name string
		err  error
	}{
		{"nil", nil},
		{"generic error", errors.New("something went wrong")},
		{"dbus timeout-shaped", dbus.Error{Name: "org.freedesktop.DBus.Error.Timeout"}},
		{"dbus access-denied", dbus.Error{Name: "org.freedesktop.DBus.Error.AccessDenied"}},
	}
	for _, tc := range notUnavailable {
		if isPortalUnavailable(tc.err) {
			t.Errorf("isPortalUnavailable(%s) = true, want false", tc.name)
		}
	}

	// The sentinel itself must always classify as unavailable (it's what
	// we wrap session-bus-connect failures with).
	if !isPortalUnavailable(errPortalUnavailable) {
		t.Error("isPortalUnavailable(errPortalUnavailable) = false")
	}
}
