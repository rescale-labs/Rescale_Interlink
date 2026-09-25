package wailsapp

import (
	"errors"
	"testing"
)

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

// The message follows the status an API error states, never digits in a
// folder's name or ID.
func TestTranslateAPIErrorReadsOnlyStatedStatus(t *testing.T) {
	for msg, want := range map[string]string{
		`create folder failed: status 401: {"detail": "Invalid token."}`: "API key is invalid or expired - please update your API key",
		`list folder XyZ12 failed: status 404: {"detail": "Not found."}`: "Item not found - it may have been deleted or moved",
		`list folder Ab403 failed: status 500: oops`:                     "Server error - please try again later",
		`create folder "Run_404" failed: status 400: bad name`:           `create folder "Run_404" failed: status 400: bad name`,
		// the rest pass through, less their credentials
		`create folder failed: status 400: https://a.blob.core.windows.net/c?sv=1&sig=FAKESIG`: `create folder failed: status 400: https://a.blob.core.windows.net/c?sv=REDACTED&sig=REDACTED`,
	} {
		if got := translateAPIError(errors.New(msg)); got != want {
			t.Errorf("translateAPIError(%q) = %q, want %q", msg, got, want)
		}
	}
}
