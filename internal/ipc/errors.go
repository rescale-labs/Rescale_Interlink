package ipc

// ErrorCode is a stable, machine-readable identifier for a user-visible error
// surfaced by the auto-download subsystem. Codes travel over IPC alongside
// canonical English text (CanonicalText); UI surfaces compare on codes, not on
// text, so wording can evolve without breaking clients.
//
// Codes are the public contract. Adding a new code requires adding an entry to
// CanonicalText and (optionally) HintFor, and mirroring the code in
// frontend/src/lib/errors.ts. A Go test enforces TS parity.
type ErrorCode string

const (
	// CodeNoAPIKey indicates no Rescale API key is available to the daemon.
	// Common cause: the user entered a key in the GUI but the token file was
	// not persisted before the daemon started polling.
	CodeNoAPIKey ErrorCode = "no_api_key"

	// CodeDownloadFolderInaccessible indicates the configured download folder
	// cannot be created, reached or written by the process that will use it.
	CodeDownloadFolderInaccessible ErrorCode = "download_folder_inaccessible"

	// CodeIPCNotResponding indicates the daemon's IPC endpoint exists but is
	// not responding to requests within the client timeout. Typically a
	// daemon in a bad state, or a stale pipe from a crashed daemon.
	CodeIPCNotResponding ErrorCode = "ipc_not_responding"

	// CodeCLINotFound indicates the rescale-int CLI binary required to spawn a
	// subprocess daemon cannot be found in the expected location.
	CodeCLINotFound ErrorCode = "cli_not_found"

	// CodeServiceAlreadyRunning indicates a daemon is already running;
	// starting another would conflict.
	CodeServiceAlreadyRunning ErrorCode = "service_already_running"

	// CodePermissionDenied indicates Windows denied access, as when security
	// software blocks rescale-int.exe. Elevation does not help: the daemon
	// runs as the user.
	CodePermissionDenied ErrorCode = "permission_denied"

	// CodeTransientTimeout indicates a transient pending state that has
	// exceeded the 10-second timeout and is now treated as an error. Surfaces
	// when the daemon does not come up quickly, or when IPC is unavailable
	// despite the daemon appearing to run.
	CodeTransientTimeout ErrorCode = "transient_timeout"

	// CodeConfigInvalid indicates the user's daemon configuration is missing
	// required fields or has validation errors.
	CodeConfigInvalid ErrorCode = "config_invalid"

	// CodeWorkspaceMissingField indicates the Rescale workspace does not have
	// the "Auto Download" custom field configured. Auto-download cannot
	// function until a workspace administrator adds it.
	CodeWorkspaceMissingField ErrorCode = "workspace_missing_field"

	// CodeWorkspaceFieldWrongType indicates the "Auto Download" custom field
	// exists but is not a select-list (the only type the daemon understands).
	CodeWorkspaceFieldWrongType ErrorCode = "workspace_field_wrong_type"

	// CodeWorkspaceFieldMissingOptions indicates the "Auto Download" custom
	// field exists and is a select-list, but is missing one or more required
	// option values (Enabled / Disabled / Conditional).
	CodeWorkspaceFieldMissingOptions ErrorCode = "workspace_field_missing_options"

	// CodeNoTokenFile indicates the GUI has an API key in memory but the
	// token file on disk is missing or out of sync. Distinct from
	// CodeNoAPIKey: the user has entered a key, the persistence layer did
	// not carry it across the process-identity handoff.
	CodeNoTokenFile ErrorCode = "no_token_file"

	// CodeScanFailed indicates the daemon is alive but its most recent job
	// scan failed (API error, timeout, expired credentials). Without it the
	// only symptom is a last-scan timestamp that silently stops advancing.
	CodeScanFailed ErrorCode = "scan_failed"
)

// CanonicalText returns the user-facing English string for a given ErrorCode.
// Every exported code MUST have an entry. Wording may evolve; the code is the
// stable identifier.
var CanonicalText = map[ErrorCode]string{
	CodeNoAPIKey:                     "No API key configured",
	CodeDownloadFolderInaccessible:   "Download folder is not accessible",
	CodeIPCNotResponding:             "Auto-download is not responding",
	CodeCLINotFound:                  "Interlink CLI not found",
	CodeServiceAlreadyRunning:        "Auto-download is already running",
	CodePermissionDenied:             "Permission denied",
	CodeTransientTimeout:             "Auto-download is taking longer than expected to respond",
	CodeConfigInvalid:                "Configuration is invalid",
	CodeWorkspaceMissingField:        "Workspace is missing the 'Auto Download' custom field",
	CodeWorkspaceFieldWrongType:      "Workspace 'Auto Download' custom field has the wrong type",
	CodeWorkspaceFieldMissingOptions: "Workspace 'Auto Download' custom field is missing required options",
	CodeNoTokenFile:                  "API key is configured in the GUI but has not been written to disk",
	CodeScanFailed:                   "The most recent job scan failed",
}

// hintText is the actionable hint shown alongside the canonical error text.
// Not every code needs one — some errors speak for themselves. Returning an
// empty string means "no hint needed."
var hintText = map[ErrorCode]string{
	CodeNoAPIKey:                     "Set your API key in API Configuration and run Test Connection.",
	CodeDownloadFolderInaccessible:   "Choose a folder you can create and write to.",
	CodeIPCNotResponding:             "Run 'rescale-int daemon stop', which stops the daemon or says how to end its process, then start auto-download from the Interlink app.",
	CodeCLINotFound:                  "Reinstall Interlink or verify rescale-int.exe is next to the GUI binary.",
	CodeServiceAlreadyRunning:        "",
	CodePermissionDenied:             "Check that security software is not blocking rescale-int.exe, then try again.",
	CodeTransientTimeout:             "If this persists, click Retry, or Open Logs to see the daemon's startup log.",
	CodeConfigInvalid:                "Review the Setup tab for fields highlighted as invalid.",
	CodeWorkspaceMissingField:        "A workspace administrator must add an 'Auto Download' custom field (select-list with Enabled/Disabled/Conditional options) in Rescale workspace settings.",
	CodeWorkspaceFieldWrongType:      "A workspace administrator must change the 'Auto Download' custom field to a select-list with Enabled/Disabled/Conditional options.",
	CodeWorkspaceFieldMissingOptions: "A workspace administrator must add the missing Enabled/Disabled/Conditional options to the 'Auto Download' custom field.",
	CodeNoTokenFile:                  "Save your configuration and retry. The daemon reads the API key from a token file that the GUI writes on save.",
	CodeScanFailed:                   "Open Logs for the scan error. The daemon retries on the next poll; a repeating failure usually means an API key, network, or proxy problem.",
}

// HintFor returns an actionable hint for an ErrorCode, or an empty string if
// no hint is defined. Unknown codes also return empty.
func HintFor(code ErrorCode) string {
	return hintText[code]
}

// CodeFromCanonicalText looks up an ErrorCode by its canonical English text,
// for a daemon of an earlier version that sends the text without its code.
// Returns empty ErrorCode on miss.
func CodeFromCanonicalText(text string) ErrorCode {
	for code, canonical := range CanonicalText {
		if canonical == text {
			return code
		}
	}
	return ""
}
