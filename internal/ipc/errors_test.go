package ipc

import (
	"strings"
	"testing"
)

// allCodes is the authoritative list of codes that must be supported by
// CanonicalText. When a new code is added to errors.go, add it here too; the
// TestCanonicalTextCoverage test enforces that every code has a canonical
// string.
var allCodes = []ErrorCode{
	CodeNoAPIKey,
	CodeDownloadFolderInaccessible,
	CodeIPCNotResponding,
	CodeCLINotFound,
	CodeServiceAlreadyRunning,
	CodePermissionDenied,
	CodeTransientTimeout,
	CodeConfigInvalid,
	CodeWorkspaceMissingField,
	CodeWorkspaceFieldWrongType,
	CodeWorkspaceFieldMissingOptions,
	CodeNoTokenFile,
	CodeScanFailed,
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

// Every text and hint, as the surfaces compose them, names the controls the
// app has: auto-download runs in the user's own session, not as a service,
// nothing raises a UAC prompt, and elevation cannot help a daemon that runs as
// the user; the API key is set in API Configuration.
func TestTextsNameCurrentControls(t *testing.T) {
	for _, code := range allCodes {
		composed := CanonicalText[code] + ". " + HintFor(code)
		for _, stale := range []string{"service", "connection settings", "uac", "as administrator", "administrator account"} {
			if strings.Contains(strings.ToLower(composed), stale) {
				t.Errorf("%s: %q mentions %q", code, composed, stale)
			}
		}
	}
	if hint := HintFor(CodeNoAPIKey); !strings.Contains(hint, "API Configuration") {
		t.Errorf("no-API-key hint %q does not say where the key is set", hint)
	}
	if hint := HintFor(CodePermissionDenied); !strings.Contains(hint, "security software") || !strings.Contains(hint, "rescale-int.exe") {
		t.Errorf("permission-denied hint %q does not name what refuses the start", hint)
	}
	// A daemon that does not answer IPC cannot be stopped through it: the
	// recovery goes through a command that stops it or names its process.
	const stuck = "Auto-download is not responding. Run 'rescale-int daemon stop', which stops the daemon or says how to end its process, then start auto-download from the Interlink app."
	if got := CanonicalText[CodeIPCNotResponding] + ". " + HintFor(CodeIPCNotResponding); got != stuck {
		t.Errorf("not-responding message = %q, want %q", got, stuck)
	}
}
