package pipeline

import "testing"

// TestNextSkipStatus covers the pre-specified InputFiles branch logic.
// "pending" → "skipped" is the remoteFiles case; terminal values
// "success" / "failed" / "skipped" must be preserved so Single Job
// localFiles uploads (which set "success" or "failed" via
// Engine.ReportUploadProgress before RunFromSpecs starts) are not
// overwritten.
func TestNextSkipStatus(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"pending", "skipped"},
		{"success", "success"},
		{"failed", "failed"},
		{"skipped", "skipped"},
		{"", "skipped"},
	}
	for _, tc := range cases {
		if got := nextSkipStatus(tc.in); got != tc.want {
			t.Errorf("nextSkipStatus(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
