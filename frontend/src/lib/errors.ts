// Canonical ErrorCode values surfaced by the auto-download subsystem.
// This file mirrors internal/ipc/errors.go. Parity is enforced by
// internal/ipc/errors_ts_parity_test.go — if you change one side, change
// the other, or the Go test fails in CI.
//
// Compare on codes, not on wording. The canonical English text is in Go;
// the GUI renders whatever the DTO carries and pairs it with the hint
// returned by HintFor on the Go side.

export const CodeNoAPIKey = "no_api_key";
export const CodeDownloadFolderInaccessible = "download_folder_inaccessible";
export const CodeIPCNotResponding = "ipc_not_responding";
export const CodeCLINotFound = "cli_not_found";
export const CodeServiceAlreadyRunning = "service_already_running";
export const CodePermissionDenied = "permission_denied";
export const CodeTransientTimeout = "transient_timeout";
export const CodeConfigInvalid = "config_invalid";
export const CodeWorkspaceMissingField = "workspace_missing_field";
export const CodeWorkspaceFieldWrongType = "workspace_field_wrong_type";
export const CodeWorkspaceFieldMissingOptions = "workspace_field_missing_options";
export const CodeNoTokenFile = "no_token_file";
export const CodeScanFailed = "scan_failed";
