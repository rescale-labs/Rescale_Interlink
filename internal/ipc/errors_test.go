package ipc

import "testing"

// allCodes is the authoritative list of codes that must be supported by
// CanonicalText. When a new code is added to errors.go, add it here too; the
// TestCanonicalTextCoverage test enforces that every code has a canonical
// string.
var allCodes = []ErrorCode{
	CodeNoAPIKey,
	CodeDownloadFolderInaccessible,
	CodeServiceDisabledInSCM,
	CodeIPCNotResponding,
	CodeCLINotFound,
	CodeServiceAlreadyRunning,
	CodePermissionDenied,
	CodeServiceNotInstalled,
	CodeServiceStopped,
	CodeTransientTimeout,
	CodeConfigInvalid,
	CodeWorkspaceMissingField,
	CodeWorkspaceFieldWrongType,
	CodeWorkspaceFieldMissingOptions,
	CodeNoTokenFile,
}

func TestCanonicalTextCoverage(t *testing.T) {
	for _, code := range allCodes {
		text, ok := CanonicalText[code]
		if !ok {
			t.Errorf("code %q has no entry in CanonicalText", code)
			continue
		}
		if text == "" {
			t.Errorf("code %q has empty canonical text", code)
		}
	}
	if len(CanonicalText) != len(allCodes) {
		t.Errorf("CanonicalText has %d entries, expected %d — an orphan entry was likely left behind or allCodes is stale",
			len(CanonicalText), len(allCodes))
	}
}

func TestHintForKnownCodes(t *testing.T) {
	// Every code should resolve without panicking. Empty hints are allowed
	// (some errors speak for themselves).
	for _, code := range allCodes {
		_ = HintFor(code)
	}
}

func TestHintForUnknownCode(t *testing.T) {
	if got := HintFor(ErrorCode("this_code_does_not_exist")); got != "" {
		t.Errorf("HintFor unknown code = %q, want \"\"", got)
	}
}

func TestCodeFromCanonicalTextRoundTrip(t *testing.T) {
	for _, code := range allCodes {
		text := CanonicalText[code]
		got := CodeFromCanonicalText(text)
		if got != code {
			t.Errorf("CodeFromCanonicalText(%q) = %q, want %q", text, got, code)
		}
	}
}

func TestCodeFromCanonicalTextUnknown(t *testing.T) {
	if got := CodeFromCanonicalText("something the daemon would never say"); got != "" {
		t.Errorf("CodeFromCanonicalText unknown = %q, want \"\"", got)
	}
}
