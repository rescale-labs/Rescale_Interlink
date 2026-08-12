package wailsapp

import (
	"testing"

	"github.com/rescale/rescale-int/internal/models"
)

func TestLatestStatusReason(t *testing.T) {
	entry := func(date, reason string) models.JobStatusEntry {
		return models.JobStatusEntry{Status: "x", StatusDate: date, StatusReason: reason}
	}

	cases := []struct {
		name     string
		statuses []models.JobStatusEntry
		want     string
	}{
		{
			// "2026-01-02T00:00:00+09:00" is 2026-01-01T15:00:00Z — earlier in
			// absolute time than the Z entry, yet lexicographically later. A raw
			// string sort picks the wrong reason here.
			name: "mixed offsets compare chronologically, not lexicographically",
			statuses: []models.JobStatusEntry{
				entry("2026-01-02T00:00:00+09:00", "older-despite-later-string"),
				entry("2026-01-01T20:00:00Z", "actually-newest"),
			},
			want: "actually-newest",
		},
		{
			name: "empty reasons are skipped in favor of an older entry that has one",
			statuses: []models.JobStatusEntry{
				entry("2026-03-02T10:00:00Z", ""),
				entry("2026-03-01T10:00:00Z", "queued behind license"),
			},
			want: "queued behind license",
		},
		{
			name: "unparseable dates fall back to string ordering",
			statuses: []models.JobStatusEntry{
				entry("zzz-not-a-date", "later-by-string"),
				entry("aaa-not-a-date", "earlier-by-string"),
			},
			want: "later-by-string",
		},
		{
			name:     "no entries",
			statuses: nil,
			want:     "",
		},
		{
			name: "all reasons empty",
			statuses: []models.JobStatusEntry{
				entry("2026-03-02T10:00:00Z", ""),
				entry("2026-03-01T10:00:00Z", ""),
			},
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := latestStatusReason(tc.statuses); got != tc.want {
				t.Errorf("latestStatusReason() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestLatestStatusReason_OrderIndependent pins the transitivity fix: with a
// per-pair comparator that mixed time and string comparison, this input
// produced different winners depending on input order. Every permutation must
// agree, and a malformed date must never outrank a parseable one.
func TestLatestStatusReason_OrderIndependent(t *testing.T) {
	entries := []models.JobStatusEntry{
		{Status: "x", StatusDate: "2025-13-45", StatusReason: "from-malformed-date"},
		{Status: "x", StatusDate: "2025-12-31T20:00:00-05:00", StatusReason: "true-newest"}, // 2026-01-01T01:00:00Z
		{Status: "x", StatusDate: "2026-01-01T00:00:00+00:00", StatusReason: ""},
	}

	perms := [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}
	for _, p := range perms {
		input := []models.JobStatusEntry{entries[p[0]], entries[p[1]], entries[p[2]]}
		if got := latestStatusReason(input); got != "true-newest" {
			t.Errorf("permutation %v: latestStatusReason() = %q, want %q", p, got, "true-newest")
		}
	}
}
