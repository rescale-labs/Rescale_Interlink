package wailsapp

import "testing"

// TestMapSortToOrdering covers the frontend sort field/direction pairs the file
// browser can send, and the two cases that must produce no ordering at all so
// the API client falls back to its own default.
func TestMapSortToOrdering(t *testing.T) {
	tests := []struct {
		field     string
		direction string
		want      string
	}{
		{"name", "asc", "name"},
		{"name", "desc", "-name"},
		{"size", "asc", "decryptedSize"},
		{"size", "desc", "-decryptedSize"},
		{"created", "asc", "dateUploaded"},
		{"created", "desc", "-dateUploaded"},

		// No field means no ordering, whatever the direction says.
		{"", "asc", ""},
		{"", "desc", ""},

		// A field the API does not support must not be forwarded.
		{"modTime", "asc", ""},
		{"typeCode", "desc", ""},
	}

	for _, tc := range tests {
		if got := mapSortToOrdering(tc.field, tc.direction); got != tc.want {
			t.Errorf("mapSortToOrdering(%q, %q) = %q, want %q", tc.field, tc.direction, got, tc.want)
		}
	}
}
