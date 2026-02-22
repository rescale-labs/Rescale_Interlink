# Release Notes - Rescale Interlink

## v4.7.2 - February 21, 2026

### Consistent Load/Save UI

- **PUR "Load Existing Base Job Settings" dropdown**: PUR tab now has a dropdown matching SingleJob's pattern with CSV, JSON, and SGE format options (previously was a single button that only opened a JSON file dialog).
- **PUR "Save As..." dropdown**: Template can now be saved as CSV, JSON, or SGE from the scan configuration step, matching SingleJob's save functionality.
- **SingleJob label update**: "Load From..." button renamed to "Load Existing Job Settings" for clarity.

### Label Improvements

- **PUR subtitle**: "Parallel Upload and Run" subtitle added to the progress bar header area, visible in all workflow states.
- **PUR label clarity**: "Configure Job Settings" → "Configure Base Job Settings", "Create New Settings" → "Configure New Base Job Settings", "Scan Directories" → "Scan to Create Jobs" throughout the PUR workflow (button text, progress bar step label, and headings).
- **SetupTab inner card**: Renamed inner "Advanced Settings" card heading to "Logging Settings" to eliminate redundant naming with the outer collapsible section.

### Bug Fixes

- **orgCode preservation**: Fixed `loadJobFromJSON` and `loadJobFromSGE` in jobStore.ts to include `orgCode` in the returned object. Previously `orgCode` was silently dropped when loading templates via JSON or SGE in both SingleJob and PUR workflows.

### Files Changed
| File | Changes |
|------|---------|
| `frontend/src/components/tabs/PURTab.tsx` | Load dropdown, save dropdown, subtitle, label renames, new handlers, WORKFLOW_STEPS update |
| `frontend/src/components/tabs/SingleJobTab.tsx` | "Load From..." → "Load Existing Job Settings" |
| `frontend/src/components/tabs/SetupTab.tsx` | Inner "Advanced Settings" → "Logging Settings" |
| `frontend/src/stores/jobStore.ts` | Added `orgCode` to `loadJobFromJSON` and `loadJobFromSGE` return objects |
| `internal/version/version.go` | Version bump to v4.7.2 |
| `main.go` | Version comment |
| `wails.json` | productVersion |
| `frontend/package.json` | version |
| `frontend/package-lock.json` | version |

---

## v4.7.1 - February 21, 2026

### Disk Space Error UX

- **Disk space error banner**: Downloads that fail due to insufficient disk space now trigger a prominent amber banner at the top of the Transfers tab showing the number of failed downloads, available space, and space needed. The banner persists after "Clear Completed" and requires explicit dismiss. New failures re-show the banner automatically.
- **Short error labels**: Failed transfers with disk space errors show "No disk space" instead of a truncated long error string. Full error text is available via hover tooltip on all transfer rows.
- **Error classification**: Added `classifyError()` and `extractDiskSpaceInfo()` utilities in `transferStore.ts` to detect disk space errors from backend error strings (matches `insufficient disk space`, `ENOSPC`, `disk quota exceeded`, etc.).

### Settings Reorganization

- **PUR-specific settings moved from Setup tab**: Worker Configuration (tar/upload/job workers), Tar Options (exclude/include patterns, compression, flatten), and Directory Scan Settings (scan prefix, validation pattern) have been removed from Setup tab's Advanced Settings.
- **Pipeline Settings in PUR tab**: Workers and tar options now appear as "Pipeline Settings" in the PUR tab, visible in both the scan step and the jobs-validated step (ensuring CSV-loaded workflows also have access).
- **Tar options in SingleJob directory mode**: When using directory input mode in SingleJob tab, tar options (exclude/include patterns, compression, flatten) are now visible inline below the directory selector.
- **Scan option persistence**: Validation pattern and scan prefix are now persisted to config.csv when changed on the PUR tab, ensuring values survive app restarts.
- **Compression normalization**: Legacy `gz` values in config.csv are normalized to `gzip` when loaded, ensuring consistent frontend display.
- **Engine fallback removal**: `Scan()` and `ScanToSpecs()` in `engine.go` no longer fall back to `config.ValidationPattern` when the scan options don't specify one. The PUR tab is now the single source of truth for these values.

### Files Changed
| File | Changes |
|------|---------|
| `frontend/src/stores/transferStore.ts` | Error classification, `extractDiskSpaceInfo`, `errorType` on TransferTask |
| `frontend/src/stores/index.ts` | Export new types and functions |
| `frontend/src/components/tabs/TransfersTab.tsx` | DiskSpaceBanner, improved TransferRow error display, hover tooltips |
| `frontend/src/components/tabs/SetupTab.tsx` | Removed Directory Scan Settings, Worker Configuration, Tar Options sections |
| `frontend/src/components/tabs/PURTab.tsx` | PipelineSettings component, scan option persistence to config |
| `frontend/src/components/tabs/SingleJobTab.tsx` | Tar options section for directory mode |
| `internal/core/engine.go` | Removed validation pattern fallback to config in Scan/ScanToSpecs |
| `internal/config/csv_config.go` | Updated TarCompression comment |
| `internal/wailsapp/config_bindings.go` | Normalize `gz` to `gzip` in GetConfig() |

---

## v4.7.0 - February 21, 2026

### PUR Performance & Reliability

Major improvements to the PUR (Parallel Upload and Run) pipeline addressing directory scanning speed, startup latency, path reliability, and GUI log visibility.

#### Directory Path Fix
- **Absolute path normalization**: Replaced relative path generation in `Scan()` and `ScanToSpecs()` with `pathutil.ResolveAbsolutePath()`. Relative paths failed when CWD at tar-creation time differed from CWD at scan time (especially in GUI mode where CWD is the application install directory). Paths are now normalized to absolute at three points: engine scan output, pipeline ingress (`NewPipeline`), and tar worker (belt-and-suspenders for legacy CSV/state files).

#### Directory Scanning Speed
- **`filepath.WalkDir` migration**: Replaced `filepath.Walk` with `filepath.WalkDir` in `engine.go` (recursive scan) and `multipart.go` (`ValidateRunDirectory`). `WalkDir` avoids per-entry `os.Stat` syscalls, significantly reducing I/O on directories with many files.
- **`SkipDir` after match**: When a directory matches the scan pattern (e.g., `Run_*`), the walker now returns `filepath.SkipDir` to avoid descending into matched directories. This is the primary source of slow recursive scans — without it, the walker traverses all files inside each matched run directory. Nested matches (e.g., `Run_1/sub/Run_2`) are intentionally not discovered; run directories are expected to be siblings.

#### PUR Startup Speed
- **Concurrent version resolution**: `resolveAnalysisVersions()` (which calls the paginated `GetAnalyses` API) now runs in a goroutine concurrent with tar and upload workers. Previously this was a blocking call that delayed pipeline start. Tar and upload workers start immediately; only job workers wait on the `versionsResolved` channel before creating jobs. Data race safe via single write before channel close.

#### Activity Log Routing
- **Log callback duplicate fix**: `p.logf()` previously called both the log callback AND `log.Printf`, causing duplicate stdout output when the Engine's `LogCallback` (which itself calls `log.Printf` via `publishLog`) was set. Now `logf` only calls `log.Printf` when no callback is set (CLI mode without Engine).
- **Full pipeline log routing**: Converted ~35 `log.Printf` calls across `resolveAnalysisVersions`, `ResolveSharedFiles`, `tarWorker`, `uploadWorker`, `jobWorker`, `progressReporter`, and `Run` completion to use `p.logf()` with appropriate level/stage/jobName. All pipeline messages now flow through the EventBus callback to the GUI Activity Log.

#### Phase Timing & Observability
- Added `pipelineStart` timestamp and timing logs at shared file resolution, worker startup, first tarball creation (`sync.Once`), and pipeline completion for concrete before/after measurement of startup delay improvements.

#### Test Infrastructure
- **`AnalysisResolver` interface**: Added narrow interface for the `GetAnalyses` API call, enabling mock injection for deterministic pipeline testing without real API calls.
- **New `pipeline_test.go`**: 5 tests covering path normalization at ingress, concurrent version resolution timing, log callback delivery, and version resolution map correctness. All pass with `-race`.
- **Updated `engine_test.go`**: 3 new tests verifying scan output paths are absolute and that `SkipDir` prevents nested directory matches.

#### Files Changed
| File | Changes |
|------|---------|
| `internal/pur/pipeline/pipeline.go` | All changes: path normalization, concurrent version resolution, log routing, timing logs, `AnalysisResolver` interface |
| `internal/core/engine.go` | Absolute paths in Scan/ScanToSpecs, WalkDir + SkipDir for recursive scan |
| `internal/util/multipart/multipart.go` | WalkDir in ValidateRunDirectory |
| `internal/core/engine_test.go` | 3 new tests for absolute paths and SkipDir |
| `internal/pur/pipeline/pipeline_test.go` | New file: 5 tests for pipeline behavior |

#### Known Issues (Fixed in v4.7.1)
- **Download errors under low disk space**: Download failures due to insufficient disk space showed truncated error messages in the narrow status column, making the cause invisible. **Fixed in v4.7.1** with a prominent amber banner showing available/needed space, short "No disk space" labels, and hover tooltips for full error details.

---

## v4.6.8 - February 18, 2026

### Bug Fixes

- **Automation JSON format (Critical)**: Fixed job creation with automations. Two issues: (1) API expects `{"automation": {"id": "..."}}` (nested object) but we sent `{"automation": "..."}` (flat string); (2) API requires `"environmentVariables": {}` in each automation entry — omitting it triggers HTTP 500. Added `NormalizeAutomations()` choke point in `CreateJob` and initialized `EnvironmentVariables` at all construction sites. Removed `omitempty` from the JSON tag to prevent `json.Marshal` from dropping the key.
- **Retry safety for job creation**: Job creation and submission POST requests no longer retry on 5xx server errors (non-idempotent). Prevents 5+ minute hangs and potential duplicate jobs. Custom `ErrorHandler` preserves actual API error messages instead of generic "giving up after N attempt(s)".
- **Single job submission (All modes)**: Fixed single job tab for `localFiles` and `remoteFiles` input modes, which were completely non-functional. `localFiles` now uploads each file via `cloud/upload.UploadFile()` before creating the job. `remoteFiles` passes file IDs directly to the pipeline, skipping tar/upload. Pipeline feeder and job worker updated to handle pre-specified `InputFiles` when `Directory` is empty.
- **GTK ibus warnings on Linux**: Suppressed `Gtk-WARNING: im-ibus.so: cannot open shared object file` messages by setting `GTK_IM_MODULE=none` before Wails GUI launch on Linux (only if not already set by user).

### Terminology & UX

- Renamed "Configure Job Template" → "Configure Job Settings", "Create New Template" → "Create New Settings", "Load Template" → "Load Settings" in PUR tab
- Renamed "Run Folders Subpath and Validation Configuration" → "Directory Scan Settings" in Setup tab
- Added tooltip "PUR = Parallel Upload and Run" to PUR tab
- Updated CLI help text: "PUR (Parallel Upload and Run) for Rescale"
- Updated CLI_GUIDE.md and FEATURE_SUMMARY.md: "PUR (Parallel Upload and Run)" terminology throughout

### Default Template Fix

- Changed default job template `directory` from `./Run_${index}` to empty string, preventing single job submission from trying to tar the current working directory

---

## v4.6.7 - February 17, 2026

### Code Quality: Audit Remediation

Comprehensive audit remediation across security, code quality, dead code removal, and documentation accuracy.

#### Security Fixes
- **SECURITY.md accuracy**: Corrected crypto claims from AES-256-GCM to AES-256-CBC with PKCS7 padding; updated key derivation documentation
- **URL sanitization (R2)**: Replaced substring-based FedRAMP URL detection with proper hostname parsing in both frontend (SetupTab.tsx) and backend (csv_config.go) to prevent URL spoofing; added 9 unit tests

#### Bug Fixes
- **Race condition fix (R3)**: Added mutex synchronization to queue retry test to eliminate data race flagged by `go test -race`
- **Context leak fix (R4)**: Added `defer cancel()` in daemon.go to ensure context cleanup on all exit paths
- **IPC mock fix (R9)**: Fixed mock signature to match ServiceHandler interface (added userID parameter)

#### Code Quality
- **Shared FIPS init (R10)**: Extracted duplicated FIPS 140-3 initialization from both main.go files into `internal/fips/init.go`
- **stdlib consolidation (R11)**: Replaced local `contains()` with `slices.Contains` from Go 1.21+ stdlib
- **Dead code removal**: Removed `ValidateForConnection` (R5), `UpdateJobRow` (R6), backward-compat aliases (R7), custom `equalIgnoreCase` (R8)
- **version.go cleanup (R12)**: Removed redundant changelog comments from version.go
- **Makefile fix (R17)**: Fixed `make build` to output to versioned platform directory instead of project root

#### Documentation
- Updated all .md files to v4.6.7 version consistency (R13)
- Fixed stale 16MB → 32MB chunk size references in FEATURE_SUMMARY.md and CLI_GUIDE.md (R14)

---

## v4.6.6 - February 17, 2026

### Fix: Shared Job Download Support (Azure)

Interlink failed when downloading outputs from shared jobs stored in Azure. The root cause was a type mismatch in `AzureCredentials.Paths`: the struct defined `[]string` but the API returns `[]object` (each with `path`, `pathParts`, `sasToken`) for shared-file credential requests. This caused `json.Unmarshal` to fail with a 7-layer deep Go-internal error message.

#### Bug Fix

**Azure Credential Parsing:** Added `AzureCredentialPath` struct and changed `AzureCredentials.Paths` from `[]string` to `[]AzureCredentialPath`. The API has never returned string values in `paths` — it returns either an empty array or per-file credential objects. The old `Paths[0]`-as-URL branch in `buildSASURL()` was dead code and has been removed. (Files: `internal/models/credentials.go`, `internal/cloud/providers/azure/client.go`)

**Per-File SAS Token Support:** Added `GetPerFileSASToken()` helper and wired per-file SAS tokens into `buildSASURL()`. For shared files, the API returns blob-scoped SAS tokens in the credential response. The service client URL now uses the per-file token (falling back to container-level when no match). The credential cache key was also updated to include the file path, preventing shared-file credentials from being incorrectly reused across different files. (Files: `internal/cloud/providers/azure/client.go`, `internal/cloud/credentials/manager.go`)

#### Error Message Improvements

**Structured Download Errors:** All CLI download paths (batch job download, batch file download, single-file download) now use `formatDownloadError()` which:
- Classifies the failed step (credential fetch, client creation, download, checksum, decrypt)
- Extracts the root cause from deep error chains
- Sanitizes Go-internal messages (e.g., `json: cannot unmarshal...` → `unexpected credential response format`)
- Redacts secrets (SAS tokens, AWS keys) via `sanitizeErrorString()`
- Includes actionable guidance (`--debug`, access verification)

(Files: `internal/cli/download_helper.go`, `internal/cli/jobs.go`)

#### Tests Added

- 5 credential struct deserialization tests (shared file, empty paths, missing paths, multiple paths, expiration)
- 6 Azure client tests (buildSASURL with AccountName/StorageAccount/neither, GetPerFileSASToken match/no-match/empty)
- 12 download error formatting tests (chain collapse, context inclusion, Go-internal sanitization, step classification, secret redaction, credential failure simulation)
- 3 API client credential tests (shared-file response, permission denied, malformed JSON)

#### Regression Command

```bash
rescale-int jobs download -j "BWuHag" -d "." --search "rescale-ai"
```

---

## v4.6.5 - February 16, 2026

### PUR Parity: Close All Gaps vs Old Python PUR

Comprehensive gap analysis and parity fixes to match or exceed old Python PUR (pur.py v0.7.6) behavior. Tested against real user scenario (Linux, Basic proxy, Azure, 3 DOE directories, hundreds of runs).

#### New Features

**Per-Upload Proxy Warmup (P0):** Basic proxy sessions expire after 30-60 minutes, causing long batch runs to fail. Added `WarmupProxyConnection()` that creates a standalone HTTP client and GETs the tenant URL before each upload and on retry after timeout. Scoped to Basic proxy mode for v4.6.5. (Files: `internal/http/proxy.go`, `internal/pur/pipeline/pipeline.go`)

**Multi-Part `make-dirs-csv` (P1):** Added `--part-dirs` flag to scan multiple project directories (e.g., DOE_1, DOE_2, DOE_3). Creates unique job names with project suffix. Uses recursive validation (Walk) instead of top-level-only Glob, matching old PUR's rglob behavior. Shared `multipart.ScanDirectories()` helper prevents CLI/GUI logic drift. (Files: `internal/util/multipart/scan.go`, `internal/cli/pur.go`)

**OrgCode Project Assignment (P2):** Added `OrgCode` field to JobSpec, jobs CSV, and config CSV. New `AssignProjectToJob()` API method calls `/api/v2/organizations/{org_code}/jobs/{job_id}/project-assignment/` after job creation. Per-job OrgCode overrides config default. Non-fatal on failure. GUI fields added to PUR and SingleJob tabs. (Files: `internal/models/job.go`, `internal/config/jobs_csv.go`, `internal/config/csv_config.go`, `internal/api/client.go`, `internal/pur/pipeline/pipeline.go`)

**`--dry-run` on `pur run` and `pur resume` (P2):** `pur run --dry-run` shows a summary table of all jobs without executing. `pur resume --dry-run` loads state and groups jobs by remaining stage (need tar, upload, create, submit, complete), mirroring the actual feeder routing logic. (File: `internal/cli/pur.go`)

**`submit-existing --ids` (P3):** Added `--ids` flag for direct job submission by ID without CSV or pipeline. Mutually exclusive with `--jobs-csv`. Simple loop with per-ID success/failure reporting. (File: `internal/cli/pur.go`)

#### Infrastructure

- Exported `BuildProxyURL()` in proxy.go (renamed from unexported `buildProxyURL`)
- Added `multipart.ScanDirectories()` shared scan helper with full test coverage (8 tests)
- Added `multipart.ScanResult` and `ScanOpts` types for unified scan interface

---

## v4.6.4 - February 12, 2026

### PUR Feature Parity, Bug Fixes, and Enhancements

Comprehensive gap analysis between old Python PUR (v0.7.6) and Interlink PUR, with fixes for blocking bugs and feature gaps.

#### Bug Fixes

**Pattern Regex Fix (#24):** Current regexes missed filenames like `Run_335_Fluid_Meas.avg.snc` because `\b` after `\d+` doesn't match when followed by `_` (a word character). Added Pattern 4 regex for number-followed-by-separator-text. (File: `internal/pur/pattern/pattern.go`)

**GUI Template Crash Fix:** Wails app window crashed (Go panic) when saving/loading PUR templates with null or missing fields. Added `normalizeJobSpecDTO()` for nil-safe slice defaults, panic recovery on all template methods, and atomic writes via temp-file-then-rename. (File: `internal/wailsapp/job_bindings.go`)

**Azure Proxy Timeout Fix:** Azure uploads failed with "context deadline exceeded" on block 0 in proxy basic mode. Replaced blocking `time.Sleep()` in `ExecuteWithRetry()` with context-aware `select`, added early deadline checks before retries, and added proxy bypass debug logging. (Files: `internal/http/retry.go`, `internal/http/proxy.go`, `internal/cloud/providers/azure/streaming_concurrent.go`)

#### New Features

**`--extra-input-files`:** Upload local files once, attach to every job in PUR batch. Supports comma-separated local paths and/or `id:<fileId>` references. Includes `--decompress-extras` flag (default: false). GUI support added to PUR tab with file picker and decompress checkbox. (Files: `internal/pur/pipeline/pipeline.go`, `internal/cli/pur.go`, `frontend/src/components/tabs/PURTab.tsx`)

**`--iterate-command-patterns`:** Revived dead `ScanOptions.IteratePatterns` code path. Added CLI flags for `pur make-dirs-csv`: `--iterate-command-patterns` and `--command-pattern-test` (preview mode). GUI support with "Vary command across runs" toggle and pattern preview. Added `PreviewCommandPatterns()` Go binding for GUI. (Files: `internal/cli/pur.go`, `internal/wailsapp/job_bindings.go`, `frontend/src/components/tabs/PURTab.tsx`)

**Missing CLI Flags:** Exposed config-level features as CLI flags on `pur run` and `pur resume`:
- `--include-pattern` / `--exclude-pattern` — Tar file filtering
- `--flatten-tar` — Remove subdirectory structure in tarball
- `--tar-compression` — "none" or "gzip" (accepts legacy "gz")
- `--tar-workers` / `--upload-workers` / `--job-workers` — Worker pool sizes
- `--rm-tar-on-success` — Delete local tar after successful upload

(Files: `internal/cli/pur.go`, `internal/pur/pipeline/pipeline.go`)

#### Tests Added

- 9 new pattern tests (complex filenames, multiline commands, number-followed-by-text)
- 10 new template tests (nil fields, corrupt JSON, atomic writes, Wails roundtrip)
- 5 new proxy bypass tests (exact blob host, host:443, wildcard domain, spacing in no_proxy)
- 4 new retry tests (context cancellation, deadline checks, success/fatal paths)

---

## v4.6.3 - February 10, 2026

### Fix: S3 Upload "Stream Not Seekable" Failure During PUR

During PUR testing on Linux v4.6.2, some parallel uploads failed intermittently with:
```
S3Storage upload failed: failed to upload part X: operation error S3: UploadPart,
failed to rewind transport stream for retry, request stream is not seekable
```

A transient network error triggered the AWS SDK's built-in retry, which couldn't rewind the upload stream. Some jobs in a batch succeeded while others failed.

#### Root Cause

Two compounding problems in `internal/cloud/providers/s3/streaming_concurrent.go`:

1. **`progressReader` doesn't implement `io.Seeker`**: The existing `progressReader` wraps `bytes.NewReader` to track upload progress but only implements `Read()`, not `Seek()`. When the AWS SDK encounters a transient network error mid-upload and tries its built-in retry, it can't `Seek(0, 0)` to rewind. The same `progressReader` type is used for downloads where the inner reader is `resp.Body` (not seekable), so the existing struct couldn't be changed.

2. **`bodyReader` created outside the retry closure**: The reader was created once before `RetryWithBackoff`. On application-level retry, the same consumed reader was reused with an empty body.

#### Bug Fixes

**Fix 1: New `uploadProgressReader` type with `io.ReadSeeker` support**
- Added upload-specific `uploadProgressReader` that embeds `*bytes.Reader` for seek support, mirroring Azure's `progressReadSeekCloser` pattern
- `Seek()` implementation rolls back reported progress to prevent double-counting on retry
- Existing `progressReader` left unchanged for download use
- **File**: `internal/cloud/providers/s3/streaming_concurrent.go`

**Fix 2: Reader creation moved inside retry closure**
- Each retry attempt now gets a fresh `bytes.NewReader` (or `uploadProgressReader`), matching the pattern already used in `UploadStreamingPart` and Azure's `UploadCiphertext`
- **File**: `internal/cloud/providers/s3/streaming_concurrent.go`

#### Tests

- `TestUploadProgressReaderSeek` — Verifies `io.ReadSeeker` compliance: read, seek to 0, re-read
- `TestUploadProgressReaderSeekRollsBackProgress` — Verifies progress rollback on seek
- `TestUploadProgressReaderThreshold` — Verifies threshold-based callback reporting
- **File**: `internal/cloud/providers/s3/streaming_concurrent_test.go`

---

## v4.6.2 - February 10, 2026

### Fix: Windows Auto-Download Daemon Failures and Connection Test Errors

On a fresh Windows install (subprocess mode), the auto-download daemon failed silently: "My Downloads" stuck on "Activating...", daemon scans always failed with "Failed to find completed jobs", and after a GUI restart, Test Connection failed with `unsupported protocol scheme ""`. Three separate bugs combined to produce this behavior.

#### Bug Fixes

**Fix 1: Empty `tenant_url` overwrites `APIBaseURL` in config.csv parsing**
- `SaveConfigCSV` wrote `tenant_url,` (empty) after `api_base_url,https://...`. On reload, the empty `tenant_url` overwrote `APIBaseURL` to `""`, breaking all API calls.
- Split the combined case handler, added post-parse normalization, and symmetric sync in both save and update paths.
- Added fail-fast validation in `api.NewClient` (clear error instead of cryptic "unsupported protocol scheme") and fail-hard check at daemon startup.
- **Files**: `internal/config/csv_config.go`, `internal/api/client.go`, `internal/wailsapp/config_bindings.go`, `internal/cli/daemon.go`

**Fix 2: Windows subprocess daemon doesn't set SID in UserStatus**
- The subprocess IPC handler returned `UserStatus` without the SID field. The GUI matches by SID first, which always failed, causing permanent "Activating..." state.
- Populated SID using `user.Current().Uid` (returns the SID string on Windows).
- **File**: `internal/daemon/ipc_handler_windows.go`

**Fix 3: Windows username format mismatch**
- Daemon returned `"DESKTOP-PC\Peter Klein"` but GUI compared with `"Peter Klein"`. Direct equality failed.
- Added `matchesWindowsUsername()` helper handling `DOMAIN\user`, `user@domain` (UPN), and case-insensitive comparisons.
- Added subprocess hardening: in single-user mode, if exactly 1 user is returned and no match is found, treat it as the current user.
- **File**: `internal/wailsapp/daemon_bindings_windows.go`

**Fix 4: Scan failure hides error details**
- Changed scan failure logging to include the error text in the message (visible in Activity tab) instead of only in structured log fields.
- **File**: `internal/daemon/daemon.go`

### Fix: Build Script Errors

**Fix 5: WiX extension version incompatibility (caused v4.5.9 GitHub Actions failure)**
- `wix extension add WixToolset.UI.wixext -g` (no version pin) pulled v7.0.0-rc.1 which is incompatible with WiX v6. Pinned to 6.0.2.
- **Files**: `build/build_dist.ps1`, `scripts/release-windows-msi-build.sh`, `installer/build-installer.ps1`

**Fix 6: Wrong ldflags package path in all build scripts**
- All build scripts used `-X main.Version=...` but the version constant is in `internal/version`. Binaries built by CI/Rescale had the wrong version string. Fixed to use `github.com/rescale/rescale-int/internal/version.Version`.
- **Files**: All scripts in `build/` and `scripts/`

**Fix 7: Fragile `Set-Location` in GitHub Actions build script**
- `Set-Location $env:REPO_NAME` referenced an unset env var. Made conditional.
- **File**: `build/build_dist.ps1`

---

## v4.6.1 - February 10, 2026

### Fix: PUR Jobs Fail with "The specified version is not available"

All PUR pipeline jobs failed at the "create" stage with API status 400: `"The specified version is not available."` when the user selected a software version by its display name (e.g., "CPU" for user_included). The Rescale API requires the `versionCode` (e.g., `"0"`), not the display name.

#### Root Cause

The TemplateBuilder dropdown showed `v.version` ("CPU") and stored that same display name as `analysisVersion`. It flowed unchanged through `BuildJobRequest` to the API, which rejected it. The CLI single-job path already resolved this via `resolveAnalysisVersion()`, but the PUR pipeline, GUI single job, and all import paths bypassed it.

#### Bug Fixes

**Fix 1: Frontend — TemplateBuilder stores versionCode instead of display name**
- Version dropdown now maintains a `versionMap` (display name → versionCode) and stores the versionCode internally while showing the friendly display name in the UI
- Default version on analysis change uses `versionCode` first
- Backward-compatible: existing templates with versionCodes still work via reverse lookup
- **File**: `frontend/src/components/widgets/TemplateBuilder.tsx`

**Fix 2: Backend — Pipeline resolves display names to versionCodes**
- New `resolveAnalysisVersions()` method on Pipeline fetches all analyses once (single API call), builds a lookup table, and resolves every job's version before tar/upload begins
- Catches all entry paths: GUI PUR, CLI PUR (CSV), legacy saved templates, JSON/SGE imports
- **File**: `internal/pur/pipeline/pipeline.go`

**Fix 3: Preflight validation warns about unrecognized versions**
- After version resolution, validates that each `(analysisCode, analysisVersion)` pair is recognized
- Logs prominent warnings before any tar/upload work begins, giving users clear diagnosis
- **File**: `internal/pur/pipeline/pipeline.go`

---

## v4.6.0 - February 8, 2026

### PUR Pipeline Bug Fixes

This release fixes seven interrelated bugs that rendered the PUR (Parallel Upload and Run) pipeline non-functional in the GUI. Jobs would scan and tar/upload, but never actually create or submit on Rescale. The GUI showed no progress (0/0/0), provided almost no diagnostic information, the cancel button was unresponsive, and the "Run Subpath" field didn't match user expectations. These fixes affect all platforms.

#### Bug Fixes

**Fix 1: `shouldSubmit()` Rejects the GUI Default Submit Mode**
- Root cause: `shouldSubmit()` only recognized `"yes"`, `"true"`, `"submit"`, or `""`. The GUI uses `"create_and_submit"` / `"create_only"` / `"draft"`, so jobs were never submitted.
- Fix: Created `NormalizeSubmitMode()` as a shared helper that maps all GUI and CLI mode strings to canonical `"submit"` or `"create_only"` values. Used in both `shouldSubmit()` and `ValidateJobSpec()` for early validation.
- **Files**: `internal/pur/pipeline/pipeline.go`, `internal/wailsapp/job_bindings.go`

**Fix 2: GUI Shows "0 of 0" — Dual State Manager + Race Condition**
- Root cause: Engine and Pipeline each created separate `state.NewManager()` instances. The Pipeline updated its copy; the GUI read from the Engine's — always empty. Also, `e.state` was set inside a goroutine, creating a race where polling started before state existed.
- Fix: Moved state initialization into `StartRun()` (before goroutine launch). Changed `NewPipeline()` to accept an optional `*state.Manager` so the Engine can share its instance. Pre-populated all jobs as "pending" in `StartBulkRun()` so the GUI sees them immediately.
- **Files**: `internal/core/engine.go`, `internal/pur/pipeline/pipeline.go`, `internal/wailsapp/job_bindings.go`, `internal/cli/pur.go`

**Fix 3: Terminal-State Accounting — Upstream Failures Leave Jobs as "Pending" Forever**
- Root cause: When tar/upload/create failed, `SubmitStatus` stayed "pending". `GetRunStats()` only checked `SubmitStatus`, so failed jobs counted as "pending" forever.
- Fix: Set `SubmitStatus = "failed"` when tar, upload, or create fails. Updated both `GetRunStats()` and `getJobStats()` to use consistent logic with a belt-and-suspenders fallback that checks `TarStatus`/`UploadStatus` for upstream failures.
- **Files**: `internal/pur/pipeline/pipeline.go`, `internal/core/engine.go`

**Fix 4: Cancel Pipeline — Workers Ignore Context Cancellation**
- Root cause: Workers used `for item := range channel` which blocks on receive, ignoring `ctx.Done()`. Sends to downstream channels could also deadlock when workers were exiting.
- Fix: Replaced `range` loops with `select` on `ctx.Done()` in all three workers (tar, upload, job) and the feeder goroutine. All downstream channel sends are now context-aware. Frontend handles "no run in progress" error gracefully during cancel.
- **Files**: `internal/pur/pipeline/pipeline.go`, `frontend/src/stores/jobStore.ts`

**Fix 5: GUI Needs Comprehensive Pipeline Feedback and Diagnostics**
- Root cause: The PUR GUI provided almost no diagnostic information during runs.
- Fix: Added real-time Wails event subscriptions (`interlink:state_change`, `interlink:log`, `interlink:complete`) for live job row updates. Re-added the "Create" column to the jobs table (pipeline has distinct create and submit stages). Added per-job error display with red row highlighting. Added pipeline stage summary bar (Tar/Upload/Create/Submit breakdowns). Added expandable pipeline log panel with timestamped entries. Added error summary on completion screen. Fixed `in_progress` status matching in StatsBar (pipeline emits `in_progress`, not `running`). Reduced polling to 3-second reconciliation since events are primary.
- **Files**: `frontend/src/stores/jobStore.ts`, `frontend/src/components/tabs/PURTab.tsx`, `frontend/src/stores/index.ts`

**Fix 6: Add "Tar Subpath" Field**
- Root cause: "Run Subpath" navigates into a subpath before scanning for Run_*. Users wanted to tar only a subdirectory within each matched Run_*.
- Fix: Added `TarSubpath` field to `JobSpec`, DTOs, scan options, and `tarWorker()`. Includes a path traversal guard that rejects `../` escape attempts. Clarified UI labels: "Run Subpath" renamed to "Scan Prefix" for clarity; new "Tar Subpath" field added.
- **Files**: `internal/models/job.go`, `internal/wailsapp/job_bindings.go`, `internal/core/engine.go`, `internal/pur/pipeline/pipeline.go`, `frontend/src/stores/jobStore.ts`, `frontend/src/components/tabs/PURTab.tsx`, `frontend/src/components/tabs/SetupTab.tsx`

**Fix 7: Tar File Naming — Readable with Collision-Safe Suffix**
- Root cause: `GenerateTarPath()` replaced all path separators with underscores, producing unreadable names like `Users_pklein_data_Run_1.tar.gz`.
- Fix: Uses last 1-2 path components plus a short FNV-32a hash for collision safety. Produces: `Testing_Run_6_a1b2c3d4.tar.gz`.
- **Files**: `internal/util/tar/tar.go`

---

## v4.5.9 - February 7, 2026

### Scanner, Core Count, Proxy Bypass, Encrypted File Cleanup, and Retry Path Fixes

This release fixes 5 bugs found during post-v4.5.8 testing, covering GUI job scanning, core count constraints, proxy bypass wiring, encrypted file cleanup reliability, and retry download path normalization.

#### Bug Fixes

**Bug 7: Job Scanner Shows "Found 0 Jobs" When Directory Contains Valid Runs**
- Root cause: `ScanDirectory` never passed `RootDir` to `ScanOptions`, so the scanner had no base directory to search
- Fix: Pass `PartDirs` and `StartIndex` from the scan request, return an actionable error message on zero matches instead of silently returning empty results
- **Files**: `internal/wailsapp/job_bindings.go`

**Bug 8: Core Count Forced to Node-Size Multiples Instead of Allowing Fractional Nodes**
- Root cause: HTML `<input>` had `min` and `step` set to `coresBaseUnit` (full node size), preventing fractional node values like 2 cores on a 4-core node
- Fix: Set `min=1`, `step=1`, and updated the hint text to show the valid range of fractional values
- **Files**: `frontend/src/components/widgets/TemplateBuilder.tsx`

**Bug 9: No-Proxy Bypass List Not Wired to HTTP Client or GUI**
- Root cause: `Config.NoProxy` field existed but was never read by the HTTP transport or exposed in the GUI
- Fix: Implemented `proxyFuncWithBypass()` using Go's `httpproxy` package, wired it into both NTLM and Basic proxy transports, added a No Proxy input field to the GUI Setup tab, and included `NoProxy` in `apiSettingsChanged()` and `TestConnection()` config copy
- **Files**: `internal/http/proxy.go`, `internal/wailsapp/config_bindings.go`, `frontend/src/components/tabs/SetupTab.tsx`

**Bug 10: `.encrypted` File Left Over After GUI Download Completes**
- Root cause: Deferred cleanup used a single `os.Remove` call with a warning-only fallback, which would fail silently if the file was still locked
- Fix: Retry cleanup with 3x exponential backoff, log cleanup status even without an `OutputWriter`, and added a safety-net cleanup in `download.go`
- **Files**: `internal/cloud/transfer/downloader.go`, `internal/cloud/download/download.go`

**Bug 11: Retry-Download Missing Directory Normalization for Destination Path**
- Root cause: The retry path passed `req.Dest` raw without the directory-to-file normalization that the primary path performs
- Fix: Normalize dest-is-directory in the retry path (same logic as primary path), add empty-filename fallback to both paths
- **Files**: `internal/services/transfer_service.go`

#### New Tests

- `internal/http/proxy_test.go`: Proxy bypass matching (wildcard, CIDR, exact domain, multi-pattern)
- `internal/wailsapp/job_bindings_test.go`: Scan directory validation
- `internal/cloud/transfer/downloader_test.go`: `.encrypted` cleanup (success, already-removed, safety-net)

#### Migration Notes

- No user action required
- Proxy bypass rules (`no_proxy` config key) are now fully functional — existing values that were previously ignored will take effect

---

## v4.5.8 - February 6, 2026

### Windows Installer, Config, and Daemon Reliability Fixes

This release fixes 7 bugs affecting Windows installer behavior, MSI signing, config persistence, daemon logging, file browser mount-point handling, path consistency, and UAC elevation gating.

#### Bug Fixes

**Bug 0: Installer Privilege Model — UAC-Elevated Install/Uninstall**
- Removed `ServiceFeature` from WiX installer (was silently failing on restricted VMs)
- Added UAC-elevated install/uninstall buttons in GUI Setup tab and system tray
- Service install/uninstall now uses `ShellExecute` with `runas` verb for reliable elevation
- **Files**: `installer/rescale-interlink.wxs`, `internal/wailsapp/daemon_bindings_windows.go`, `internal/elevation/elevation_windows.go`, `cmd/rescale-int-tray/tray_windows.go`

**Bug 1: MSI Signing — Post-Build Signing Step + Checksum Regeneration**
- Added MSI signing step in GitHub Actions workflow after `build_installer.ps1`
- Checksums are regenerated after signing so `.sha256` matches the signed MSI
- **Files**: `.github/workflows/release.yml`, `build/build_dist.ps1`

**Bug 2: Mount-Point Handling — Junction Resolution for Cloud VM File Browser**
- File browser now resolves Windows junction points (e.g., OneDrive, Box) before listing
- Falls back to original path if resolved path is inaccessible
- Auto-download validates download folder accessibility with junction-aware logic
- **Files**: `internal/wailsapp/file_bindings.go`, `internal/wailsapp/daemon_bindings_windows.go`, `internal/service/multi_daemon.go`

**Bug 3: Daemon Logging — `--log-file` Argument for Subprocess Launches**
- GUI and tray now pass `--log-file` to daemon subprocess for persistent logging
- Log file path uses centralized `config.LogDirectory*()` functions
- IPC handler uses centralized log path functions instead of hand-built paths
- **Files**: `internal/wailsapp/daemon_bindings_windows.go`, `cmd/rescale-int-tray/tray_windows.go`, `internal/cli/daemon.go`, `internal/service/ipc_handler.go`

**Bug 4: Config Persistence — Removed False/0/Empty Filter, Fixed Delimiter**
- CSV config writer no longer skips `"0"`, `"false"`, or empty string values
- All values are written unconditionally to prevent silent data loss
- Fixed semicolon vs comma delimiter handling in config serialization
- **Files**: `internal/config/csv_config.go`

**Bug 5: Path Consistency — Unified Windows Paths + Migration**
- PID file, state file, and log directory now all use `%LOCALAPPDATA%\Rescale\Interlink\`
- Centralized path functions (`StatePath()`, `RuntimePath()`, `LogDirectory*()`) prevent drift
- One-time migration moves state files from old Unix-style paths to Windows-native paths
- Cleans up old PID files from legacy paths
- **Files**: `internal/config/paths.go`, `internal/config/daemonconfig.go`, `internal/daemon/daemonize_windows.go`, `internal/daemon/state.go`

**Bug 6: UAC Gating — Removed IsInstalled() Pre-Checks**
- Removed `IsInstalled()` pre-checks that blocked elevation on restricted VMs
- Install/uninstall now always attempt elevation, letting Windows UAC handle access control
- HKLM registry markers track service installation state for GUI/tray status display
- **Files**: `internal/wailsapp/daemon_bindings_windows.go`, `cmd/rescale-int-tray/tray_windows.go`, `internal/service/windows_service.go`

#### Migration Notes

- No user action required — path migration is automatic on first run
- Existing `daemon.conf` files are preserved; config write behavior is now more reliable
- Windows Service installed via previous versions should be uninstalled and reinstalled using the new elevated buttons

---

## v4.5.7 - February 1, 2026

### Auto-Download Settings Auto-Save Fix

This release fixes a critical bug where settings other than the checkbox (lookback days, download folder, poll interval, conditional tag) were **not saved** when users modified them after enabling auto-download. The checkbox auto-save in v4.5.6 would capture default values before users could change them.

#### Root Cause

v4.5.6's checkbox-first workflow created a race condition:
1. User checks "Enable Auto-Download" → v4.5.6 saves config with **default** lookback (7 days)
2. User changes lookback to 364 days → only updates React state, **never saved**
3. User assumes all settings are saved, but daemon.conf still has lookback=7
4. Jobs older than 7 days are filtered out, never downloaded

#### Fixes

**Debounced Auto-Save for All Daemon Config Fields (NEW)**
- All daemon config fields now auto-save after 1 second of no changes (debounce)
- Lookback days, download folder, poll interval, and conditional tag all auto-save
- Shows "Saving..." indicator when auto-save is in progress
- Checkbox still saves immediately (cancels any pending debounce first)
- **File**: `frontend/src/components/tabs/SetupTab.tsx`

**Settings Fields Editable Before Checkbox (CHANGED)**
- Download folder, poll interval, lookback days, and tag inputs are now **always enabled**
- Users can configure all settings BEFORE checking "Enable Auto-Download"
- This allows settings-first workflow: configure → enable (saves all at once)
- Prevents the checkbox-first race condition that caused default values to be saved
- **File**: `frontend/src/components/tabs/SetupTab.tsx`

**"Save All Settings" Button Shows Saved State (IMPROVED)**
- Button turns green with checkmark when all daemon config changes are saved
- Shows "Saving..." with spinner when auto-save is in progress
- Shows "Save All Settings" only when there are unsaved changes
- **File**: `frontend/src/components/tabs/SetupTab.tsx`

**Automatic Rescan on Lookback Increase (NEW)**
- When lookback is significantly increased (more than doubled), triggers profile rescan
- This ensures newly-eligible older jobs are picked up immediately
- Only triggers when auto-download is enabled
- **File**: `frontend/src/components/tabs/SetupTab.tsx`

#### Behavior Changes

- **All settings auto-save**: No need to click "Save All Settings" for daemon config changes
- **1 second debounce**: Rapid changes only trigger one save (after typing stops)
- **Checkbox cancels pending saves**: Checking/unchecking immediately saves and cancels any pending debounce
- **Settings-first workflow enabled**: Users can now configure all settings before enabling auto-download
- **Lookback increase triggers rescan**: Extending lookback beyond 2x original value triggers immediate job scan

#### Migration Notes

- No configuration changes required
- Existing daemon.conf files are unaffected
- Users who previously had incorrect lookback values should verify their settings

---

## v4.5.6 - January 30, 2026

### Windows Auto-Download UX Fixes

This release fixes critical UX issues discovered during user testing of v4.5.5, where the "Enable Auto-Download" checkbox didn't save to disk immediately, the status display showed misleading information, and users had no clear guidance on how to complete setup.

#### Root Cause Analysis

The core issues were:
1. **Checkbox didn't auto-save**: The "Enable Auto-Download" checkbox only updated React state, not the config file. Users had to click "Save All Settings" manually.
2. **Status showed "Running" misleadingly**: The "My Downloads" section showed "Running" based on Windows Service status, even when the current user had no configuration.
3. **No feedback on unsaved changes**: Nothing indicated that checkbox changes needed explicit saving.
4. **Tray didn't guide users**: System tray showed daemon running but didn't indicate setup was needed.
5. **"current" user routing returned empty**: IPC `TriggerScan("current")` returned `no daemon found` when user had no `daemon.conf`.

#### Fixes

**Auto-Save Checkbox with Rollback (NEW)**
- "Enable Auto-Download" checkbox now saves configuration immediately when toggled
- Uses optimistic UI update with automatic rollback if save fails
- Triggers profile rescan via `TriggerScan("all")` so service picks up new users immediately
- Shows clear status messages: "Auto-download enabled. Scanning for your jobs now..."
- **File**: `frontend/src/components/tabs/SetupTab.tsx`

**Profile Rescan Binding (NEW)**
- Added `TriggerProfileRescan()` to daemon bindings (cross-platform)
- Reuses existing `TriggerScan("all")` IPC path - no new message type needed
- Called after saving daemon.conf so service picks up new users within seconds
- **Files**: `internal/wailsapp/daemon_bindings.go`, `internal/wailsapp/daemon_bindings_windows.go`

**User-Specific Status Fields (NEW)**
- Added `UserConfigured`, `UserState`, `UserRegistered` fields to `DaemonStatusDTO`
- `GetDaemonStatus()` now checks if `daemon.conf` exists and is enabled
- Returns user state: `not_configured`, `pending`, `running`, `paused`, `stopped`
- Tracks whether service has registered the current user
- **Files**: `internal/wailsapp/daemon_bindings.go`, `internal/wailsapp/daemon_bindings_windows.go`

**Redesigned "My Downloads" UI (CHANGED)**
- Separate display for Windows Service status (system-level) vs Your Auto-Download status (user-level)
- Clear state indicators: "Setup Required", "Activating...", "Active", "Paused"
- Contextual guidance messages based on state with step-by-step instructions
- Service details (uptime, jobs downloaded, etc.) only shown when user is active
- **File**: `frontend/src/components/tabs/SetupTab.tsx`

**Disabled Action Buttons When Not Configured (NEW)**
- Scan/Pause/Resume buttons disabled until user is registered with service
- Helper functions `canUserPerformActions()` and `getActionDisabledReason()`
- Shows reason why buttons are disabled: "Enable auto-download first"
- Prevents "no daemon found for identifier: current" errors
- **File**: `frontend/src/components/tabs/SetupTab.tsx`

**Tray "Setup Required" Indicator (NEW)**
- Tray tooltip shows "Your Auto-Download: Setup Required" when user hasn't configured
- New menu item "Setup Required - Click to Configure" when setup needed
- Pause/Resume/Scan controls disabled when user is not configured
- Status line changes from "Running" to "Setup Required" when appropriate
- Refreshes user configuration status every 5 seconds
- **File**: `cmd/rescale-int-tray/tray_windows.go`

#### Behavior Changes

- **Checkbox saves immediately**: No need to click "Save All Settings" after toggling "Enable Auto-Download"
- **Faster pickup by service**: Service rescans profiles immediately instead of waiting for 5-minute interval
- **Clearer status display**: "My Downloads: Running" no longer appears when user has no config
- **Tray guides setup**: First-time users see "Setup Required" in tray tooltip and menu

---

## v4.5.5 - January 30, 2026

### Windows Auto-Download Daemon Fixes

This release fixes critical bugs in Windows daemon startup and detection that caused "Access is denied" errors when the GUI/CLI/Tray tried to spawn subprocess daemons while the Windows Service was already running.

#### Root Cause

The core issue was that `service.IsInstalled()` requires admin privileges to query the Windows Service Control Manager (SCM). When run as a standard user, it returns `false` (access denied) even when the service IS installed and running. This caused all three entry points (GUI, CLI, Tray) to incorrectly spawn subprocess daemons, which then failed with "Access is denied" when trying to create the named pipe that the Windows Service already owned.

#### Fixes

**Unified Service Detection (NEW)**
- Created `ShouldBlockSubprocess()` function that performs multi-layer detection:
  1. First tries SCM query (may fail without admin)
  2. Falls back to IPC check if SCM access denied (detects ServiceMode flag)
  3. Falls back to PID file check for subprocess detection
  4. Falls back to pipe existence check as last resort
- Only blocks subprocess when service is RUNNING (not just installed) - allows subprocess mode when service is stopped
- Logs warning when service is installed-but-stopped to inform user of potential conflicts
- **Files**: `internal/service/detection_windows.go` (NEW), `internal/service/detection_other.go` (NEW stub for non-Windows)

**Robust Named Pipe Detection (NEW)**
- Created `IsPipeInUse()` function with proper Windows error code handling
- Uses `errors.As()` to unwrap go-winio's wrapped errors
- Returns `false` only for `ERROR_FILE_NOT_FOUND` (pipe doesn't exist)
- Returns `true` for `ERROR_PIPE_BUSY`, `ERROR_ACCESS_DENIED`, or any other error (conservative approach)
- **File**: `internal/ipc/pipe_windows.go` (NEW)

**Updated All Entry Points**
- GUI `StartDaemon()` now uses `ShouldBlockSubprocess()` instead of raw `IsInstalled()`
- CLI `daemon run` now uses `ShouldBlockSubprocess()` with clear error messages
- Tray `startService()` and `refreshStatus()` now use unified detection
- **Files**: `internal/wailsapp/daemon_bindings_windows.go`, `internal/cli/daemon.go`, `cmd/rescale-int-tray/tray_windows.go`

**Per-User Status Display**
- `GetDaemonStatus()` now returns CURRENT user's status by matching SID/username
- Previously returned first user's status regardless of who was logged in
- Added `getCurrentUserSID()` helper using Windows token API
- **File**: `internal/wailsapp/daemon_bindings_windows.go`

**IPC Routing for Current User**
- `TriggerDaemonScan()`, `PauseDaemon()`, `ResumeDaemon()` now use "current" user ID
- Previously used empty string which triggered operations for ALL users
- Server infers caller's SID from pipe connection when "current" is specified
- **File**: `internal/wailsapp/daemon_bindings_windows.go`

**Scan Timeout**
- Added 10-minute timeout to poll() to prevent indefinite hangs
- Logs timeout-specific error when scan exceeds limit
- **File**: `internal/daemon/daemon.go`

**Pre-Check Pipe Existence**
- IPC server now checks if pipe exists BEFORE trying to create it
- Provides clear error message: "pipe already exists (another daemon is running)"
- **File**: `internal/ipc/server.go`

#### Behavior Change

**Subprocess Allowed When Service Stopped**: Previously, subprocess spawn was blocked whenever the Windows Service was installed (even if stopped). Now it's only blocked when the service is RUNNING. A warning is logged when service is installed-but-stopped to inform users that the service may start later and cause conflicts.

---

## v4.5.4 - January 30, 2026

### Proxy Resilience for Large File Transfers

This release fixes mid-transfer failures when downloading large files through corporate proxies (particularly with Azure storage). The failures were caused by proxy connections closing during long-running data streams, combined with gaps in retry coverage.

#### Improvements

**Extended Retry Coverage to Body Reads**
- Previously, only HTTP requests were retried; if the response body read (io.ReadAll) failed, no retry occurred
- Now the full request+read+close cycle is wrapped in a single retry at the provider level
- Added `DownloadRangeOnce` (Azure) and `GetObjectRangeOnce` (S3) methods that skip internal retry, preventing double-retry when used within provider-level retry
- **Files**: `azure/client.go`, `s3/client.go`, `azure/download.go`, `s3/download.go`, `azure/streaming_concurrent.go`, `s3/streaming_concurrent.go`

**Per-Attempt Timeouts**
- Each retry attempt now uses `context.WithTimeout` with `PartOperationTimeout` (10 minutes)
- Prevents stalled proxy connections from hanging indefinitely

**Improved Error Classification**
- Added typed error checks: `context.Canceled` (fatal), `context.DeadlineExceeded` (retryable), `net.Error` timeout (retryable)
- Added 407 proxy authentication errors as fatal (prevents infinite retry on auth failures)
- Added network error patterns: "use of closed network connection", "server closed idle connection", "proxyconnect tcp", "stream error", "http2: server sent goaway"
- **File**: `internal/http/retry.go`

**NTLM Client Timeout Fix**
- Cleared the 300-second timeout on NTLM clients that was causing transfers to abort at 5 minutes
- Per-operation timeouts via context are the correct pattern
- **File**: `internal/http/client.go`

**HTTP/2 Disabled for Proxy Mode**
- HTTP/2 stream errors are common through corporate proxies
- HTTP/2 is now disabled when proxy mode is active (explicit proxy config or env vars)
- Power users can override with `FORCE_HTTP2=true` environment variable
- **File**: `internal/http/client.go`

**Progress Tracking on Retry**
- Streaming downloads with progress callbacks now correctly handle retry by rolling back progress on failure
- Uses negative progress delta to maintain accurate tracking while preserving smooth streaming updates

---

## v4.5.1 - January 28, 2026

### Security Hardening & FIPS Compliance Improvements

This release implements security hardening based on comprehensive security audit findings, with particular focus on log directory permissions, IPC authorization, and NTLM/FIPS compliance for FedRAMP environments.

#### Security Improvements

**Log Directory Permissions Hardened**

Log directories are now created with 0700 permissions (owner-only access) instead of 0755:
- Prevents other users on multi-user systems from reading log files
- Applied across all 10 locations that create log directories
- **Files**: `internal/config/paths.go`, `internal/wailsapp/filelogger_*.go`, `internal/daemon/startup_log_windows.go`, `cmd/rescale-int-tray/tray_windows.go`, `internal/wailsapp/daemon_bindings*.go`, `internal/service/ipc_handler.go`

**Windows IPC Authorization Changed to Fail-Closed**

The Windows daemon IPC authorization now fails closed if owner SID cannot be captured:
- Previously: If SID capture failed at startup, all modify operations were allowed (fail-open)
- Now: If SID capture fails, modify operations are denied with clear error message
- Prevents potential authorization bypass on multi-user Windows systems
- **File**: `internal/ipc/server.go`

**API Key Fragment Removed from Debug Logs**

Debug logs no longer include partial API key information:
- Previously logged "Testing API key XXXXXXXX..." (8 characters)
- Now logs "Testing API connection..." with no key information
- While 8 characters are not cryptographically useful, removing them follows security best practices
- **File**: `internal/wailsapp/config_bindings.go`

#### NTLM/FIPS Compliance Safeguards

**Auto-Disable NTLM for FedRAMP Platforms**

NTLM proxy mode uses MD4/MD5 algorithms which are not FIPS 140-3 approved. New safeguards prevent using NTLM with FedRAMP platforms:

- **Backend**: `IsFRMPlatform()` helper detects FedRAMP URLs (`rescale-gov.com`)
- **Backend**: `ValidateNTLMForFIPS()` returns warning when NTLM + FRM detected
- **Frontend**: NTLM option disabled and marked "(unavailable for FRM)" when FRM platform selected
- **Frontend**: Auto-switches from NTLM to `basic` when user selects FRM platform with NTLM configured
- **Startup**: Warning logged if NTLM proxy is configured in FIPS mode
- **Files**: `internal/config/csv_config.go`, `main.go`, `frontend/src/components/tabs/SetupTab.tsx`

#### New Documentation

**SECURITY.md Added**

Comprehensive security documentation covering:
- FIPS 140-3 compliance requirements and build instructions
- Proxy authentication modes and FIPS compatibility
- Log security best practices
- API key storage recommendations
- Windows IPC security model
- Encryption details

---

## v4.4.3 - January 25, 2026

### Daemon Startup UX + Service Mode Fixes

This release comprehensively fixes daemon startup reliability issues that have been affecting Windows service mode, tray app, and GUI daemon control.

#### P0 Critical Fixes

**Fix 1: Windows Service Entrypoint NOT Wired**

The Windows Service was configured to run `daemon run`, but the command never detected service context:
- **Problem**: SCM started `daemon run` but it never called `IsWindowsService()` or `RunAsMultiUserService()`, causing Error 1053
- **Solution**: Added service context detection at start of `daemon run` that delegates to `RunAsMultiUserService()` when running under SCM
- **File**: `internal/cli/daemon.go`

**Fix 2: Windows Per-User Config Path Mismatch**

Multi-user service was looking for config files in the wrong location:
- **Problem**: `multiuser_windows.go` used `.config/rescale/daemon.conf` but config is actually at `AppData\Roaming\Rescale\Interlink\daemon.conf`
- **Solution**: Updated 3 locations to use `config.DaemonConfigPathForUser()` and new `config.StateFilePathForUser()`
- **Files**: `internal/service/multiuser_windows.go`, `internal/config/daemonconfig.go`

**Fix 3: Windows Stale PID Blocks Daemon Startup**

Stale PID files prevented daemon startup:
- **Problem**: `IsDaemonRunning()` didn't validate if PID was alive; `os.FindProcess` always succeeds on Windows
- **Solution**: Use `windows.OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION)` to validate PID; clean up stale PID file if process doesn't exist
- **File**: `internal/daemon/daemonize_windows.go`

#### P1 UX Improvements

**Fix 4: Tray Immediate Error Feedback**

Errors weren't visible immediately:
- **Problem**: Errors stored in `lastError` but no immediate UI update; user only saw error on next 5-second refresh
- **Solution**: Call `updateUI()` immediately after setting error while holding lock; set `serviceRunning = false` to ensure UI reflects failed state
- **File**: `cmd/rescale-int-tray/tray_windows.go` (5 error paths updated)

**Fix 5: GUI Inline Guidance**

Users didn't understand why Start Service button was disabled:
- **Problem**: Tooltip existed but no visible inline message when auto-download was disabled
- **Solution**: Added visible amber message "Enable 'Auto-Download' above to start the service" and opacity styling on disabled button
- **File**: `frontend/src/components/tabs/SetupTab.tsx`

#### P2 Polish

**Fix 6: Unified Path Resolution**

Path resolution was inconsistent across entrypoints:
- **Problem**: CLI had robust `resolveAbsolutePath()` with ancestor fallback; GUI/Tray used naive `filepath.EvalSymlinks()`
- **Solution**: Created `internal/pathutil/resolve.go` with shared logic used by CLI, GUI, and Tray
- **Files**: New `internal/pathutil/resolve.go`, updated `internal/cli/daemon.go`, `internal/wailsapp/daemon_bindings_windows.go`, `cmd/rescale-int-tray/tray_windows.go`

**Fix 7: Relaxed Tray Parent Preflight**

Tray blocked legitimate new paths:
- **Problem**: Strict "parent exists" check blocked paths where user wanted to create new directories
- **Solution**: Replaced with `os.MkdirAll()` which creates full path - daemon already does this, now tray matches
- **File**: `cmd/rescale-int-tray/tray_windows.go`

**Fix 8: macOS/Linux Auto-Start Docs**

Auto-start examples had incorrect flags:
- **Problem**: Examples used `--background` which forks and exits, conflicting with launchd/systemd expectations
- **Solution**: Removed `--background` from launchd plist and systemd service file; added notes explaining why
- **File**: `README.md`

---

## v4.4.2 - January 17-19, 2026

### Security Hardening (January 19, 2026)

This release includes critical security hardening based on comprehensive security audits.

#### Security Fix 1: State File Permissions (Critical)

**Problem**: Resume state files were created with 0644 permissions, allowing any user on the system to read sensitive data including:
- AES-256 encryption keys
- Initialization vectors (IVs)
- Master keys for streaming encryption
- File metadata and transfer state

**Solution**: All state files are now created with 0600 permissions (owner-readable only):
- Upload resume state files (`*.upload.resume`)
- Upload lock files (`*.upload.lock`)
- Download resume state files (`*.download.resume`)
- Daemon state files (`daemon-state.json`)

**Files Modified**:
- `internal/cloud/state/upload.go` (lines 75, 226)
- `internal/cloud/state/download.go` (line 60)
- `internal/daemon/state.go` (line 97)

#### Security Fix 2: Windows IPC Authorization (High)

**Problem**: The Windows named pipe IPC allowed any authenticated user to control another user's daemon. User A could shut down User B's daemon, pause downloads, or trigger scans.

**Solution**: Added per-user SID verification:
- Daemon captures owner's SID at startup
- Each IPC connection extracts caller's SID via `GetNamedPipeClientProcessId`
- Modify operations (Pause, Resume, TriggerScan, Shutdown) require SID match
- Read-only operations (GetStatus, GetUserList, GetRecentLogs) remain open

**Files Modified**:
- `internal/ipc/server.go` - Added SID capture and verification

#### New Tests

Added comprehensive tests for security fixes:
- `internal/cloud/state/state_test.go` - File permission tests for upload/download state
- `internal/daemon/state_test.go` - File permission test for daemon state

### Checksum Verification Race Condition - Final Fix (January 17, 2026)

This release eliminates the remaining transient checksum verification failures by removing the "double verification" race condition.

#### Problem

Despite v4.4.1 adding checksum-during-write for CBC streaming, sporadic failures (1 in 265) still occurred:
- Error: `checksum mismatch... (after 3 attempts)`
- Manual retry always succeeds

#### Root Cause: Double Verification

The issue was "double verification":
1. CBC streaming computes hash during write → PASS ✓
2. Post-download `verifyChecksum()` re-reads file from disk → may get stale cache → FAIL ✗

Even with 3 retries (100ms delay each), filesystem cache may not be coherent.

#### Solution

Return computed hash from `Download()` instead of re-reading file:

1. **CBC streaming (v2)**: Already computed hash during write - now returns it
2. **HKDF streaming (v1)**: Reads file after all parts written (same file handle) to compute hash
3. **Legacy (v0)**: New `DecryptFileWithHash()` computes hash during decryption

The caller uses the returned hash for verification - **no file re-read needed**.

#### Files Modified

- `internal/cloud/transfer/downloader.go` - Return computed hash from Download()
- `internal/crypto/encryption.go` - Add DecryptFileWithHash() for legacy format
- `internal/cloud/download/download.go` - Use returned hash, skip re-verification
- Version updates in all relevant files

#### Testing

- Download 300+ files concurrently
- Verify ZERO checksum failures

---

## v4.4.1 - January 16, 2026

### Checksum Verification Improvements

This release added checksum-during-write for CBC streaming downloads and retry logic.

#### Changes

1. **Checksum-during-write** (CBC streaming): Calculate SHA-512 hash as bytes are written, eliminating post-download race condition
2. **Retry safety net**: Post-download verification retries up to 3 times with 100ms delay

#### Note

v4.4.2 supersedes this with a complete fix that eliminates the race condition entirely.

---

## v4.4.0 - January 16, 2026

### Windows Daemon Fixes & Download Reliability

This release combines Windows daemon fixes with critical download reliability improvements.

#### Fix A: Checksum Race Condition (9 files)

**Problem**: ~1 in 50-100 downloads failed checksum with empty file hash. Failed more at START of batch transfers.

**Root Cause**: `defer file.Close()` executes AFTER the function returns, but `verifyChecksum()` is called IMMEDIATELY after. On Windows, deferred Close() was still pending when verification tried to read the file.

**Fix**: Removed `defer file.Close()` and added explicit `Close()` AFTER `Sync()` but BEFORE returning.

**Files Modified**: 9 locations across downloader.go, s3/download.go, azure/download.go, download.go

#### Fix B: Real Windows IPCHandler Implementation

**Problem**: Windows daemon subprocess used a stub IPCHandler that returned nil for all methods:
- Activity tab showed no logs
- Pause/Resume didn't work
- TriggerScan didn't work

**Fix**: Fully implemented `internal/daemon/ipc_handler_windows.go`:
- SetLogBuffer/GetRecentLogs actually work
- GetStatus returns real data (uptime, active downloads)
- PauseUser/ResumeUser track state properly
- TriggerScan calls daemon.TriggerPoll()
- Shutdown calls shutdownFunc

#### Fix C: Retry Reuses Same Entry

**Problem**: Clicking retry created a NEW transfer entry instead of updating the failed one.

**Fix**: Modified `internal/transfer/queue.go:Retry()` to reset the existing task.

#### v4.3.9 Changes (Included)

1. CREATE_NO_WINDOW flag - Hides console window on subprocess launch
2. managedBy logic - Shows Stop/Pause/Resume buttons for subprocess mode
3. IPC Shutdown handler - Enables Stop button on Windows
4. GetRecentLogs handler - Fixes "unknown message type" error
5. False "Running" state fix - Trust IPC as source of truth
6. Frontend button logic - Only hide buttons for "Windows Service"

---

## v4.3.8 - January 16, 2026

### Windows Daemon Subprocess Launch Fix

This release fixes a critical bug where the Windows daemon subprocess never started when clicking "Start Service" in the GUI or tray.

#### Problem

Despite v4.3.7 enabling IPC on Windows, clicking "Start Service" did nothing:
- No daemon process was spawned
- No error messages were displayed
- Status remained "Not Running" with no explanation

Testing confirmed the daemon works perfectly when started manually from command line, but subprocess launch from GUI/tray was silently failing.

#### Root Causes

1. **Missing Windows process flags**: `exec.Command().Start()` was not using `SysProcAttr` with `CREATE_NEW_PROCESS_GROUP` for proper subprocess detachment
2. **Errors never displayed**: Tray stored errors in `lastError` but `updateUI()` never showed them to the user
3. **No diagnostic logging**: No way to trace where subprocess launch was failing

#### Fixes

1. **Added `SysProcAttr` configuration** for proper Windows subprocess detachment:
   ```go
   cmd.SysProcAttr = &syscall.SysProcAttr{
       CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
   }
   ```

2. **Made errors visible**: Tray's `updateUI()` now displays `lastError` when service is not running

3. **Added startup logging infrastructure**:
   - New `internal/daemon/startup_log_windows.go` - writes to `%LOCALAPPDATA%\Rescale\Interlink\logs\daemon-startup.log`
   - Logs each step of subprocess launch for diagnosis
   - Captures subprocess stderr to `daemon-stderr.log`
   - Log is cleared on successful daemon startup

4. **Early daemon logging**: The daemon CLI now writes startup checkpoints immediately, before IPC initialization

#### Files Modified

- `internal/daemon/startup_log_windows.go` - NEW: Startup log infrastructure
- `internal/daemon/startup_log.go` - NEW: Non-Windows stub
- `cmd/rescale-int-tray/tray_windows.go` - Display errors, add SysProcAttr, startup logging
- `internal/wailsapp/daemon_bindings_windows.go` - Add SysProcAttr, startup logging, stderr capture
- `internal/cli/daemon.go` - Early startup checkpoints
- Version updates in all relevant files

---

## v4.3.7 - January 14, 2026

### Critical Bug Fixes

This release fixes two critical issues discovered during v4.3.6 testing.

#### Fix #1: Sporadic Checksum Verification Failures (CRITICAL)

**Problem**: Random checksum mismatches during downloads (Windows & Linux), non-reproducible on retry.

**Root Cause**: Race condition - download functions used `defer outFile.Close()` without explicit `Sync()`. Checksum verification opened the file immediately after download returned, but OS buffers hadn't flushed to disk. Files were sometimes read as empty (SHA-512 of empty string in error logs).

**Fix**: Added `outFile.Sync()` before returning from all download functions:
- `internal/cloud/transfer/downloader.go` - CBC streaming and concurrent streaming downloads
- `internal/cloud/providers/s3/download.go` - All S3 download methods
- `internal/cloud/providers/azure/download.go` - All Azure download methods

#### Fix #2: Windows Daemon Communication Broken (CRITICAL)

**Problem**:
- GUI showed "Access is denied" when trying to start daemon
- Tray "Start Service" appeared to do nothing
- Neither GUI nor tray could communicate with daemon

**Root Causes**:
1. IPC was **explicitly disabled on Windows** in `daemon.go:298` (the daemon couldn't communicate with GUI/tray even when running)
2. GUI tried to use Windows Service Control Manager (SCM) which requires Administrator privileges
3. Tray stored errors but never displayed them to user

**Fix**:
- Enabled IPC on Windows by removing the `runtime.GOOS != "windows"` exclusion in `internal/cli/daemon.go`
- Refactored `internal/wailsapp/daemon_bindings_windows.go`:
  - `StartDaemon()` now launches daemon as subprocess (no admin required)
  - `StopDaemon()` sends shutdown via IPC (no admin required)
  - `GetDaemonStatus()` checks IPC first, with optional SCM query for display

**Result**: Daemon now works on Windows without Administrator privileges by using subprocess mode + named pipe IPC.

#### Files Modified

- `internal/cloud/transfer/downloader.go` - Add Sync() calls
- `internal/cloud/providers/s3/download.go` - Add Sync() calls
- `internal/cloud/providers/azure/download.go` - Add Sync() calls
- `internal/cli/daemon.go` - Enable IPC on Windows
- `internal/wailsapp/daemon_bindings_windows.go` - Use subprocess + IPC instead of SCM
- Version updates in main.go, internal/version/version.go, Makefile

---

## v4.3.6 - January 14, 2026

### Daemon Logging Improvements

This release significantly improves the auto-download daemon's logging behavior, reducing log noise and API call overhead.

#### Problem Solved

- Jobs with "Auto Download = not set" were flooding logs with ~300 useless SKIP entries per scan
- Scans were taking 10+ minutes due to excessive API calls (2+ per job at 1.6 req/sec rate limit)

#### New Behavior

- **Silent filtering**: Jobs with "Auto Download = not set" or "disabled" are now filtered silently - no log entry
- **Check field FIRST**: The "Auto Download" custom field is checked BEFORE the downloaded tag, saving API calls for ~95% of jobs
- **Improved summary**: Scan completion now shows `filtered` count separately from `skipped` count:
  ```
  === SCAN COMPLETE === Scanned 304, filtered 298, downloaded 1, skipped 2 (took 3m35s)
  ```
- **Cleaner logs**: Only real candidates (Enabled/Conditional jobs) appear in logs:
  - `DOWNLOAD: Job Name [id] - Auto Download is Enabled`
  - `SKIP: Job Name [id] - already has 'autoDownloaded:true' tag`
  - `SKIP: Job Name [id] - Auto Download is Conditional but missing tag 'xyz'`

#### API Call Optimization

- Before: 304 jobs × 2 API calls = 608 calls (~6.3 minutes at 1.6 req/sec)
- After: 304 jobs × 1 API call + ~10 additional tag checks = ~314 calls (~3.3 minutes)
- **~50% reduction in API calls**

#### Technical Changes

- Added `CheckEligibilityResult` struct with `ShouldLog` flag
- Refactored `CheckEligibility()` in `internal/daemon/monitor.go`
- Updated `poll()` in `internal/daemon/daemon.go` to track `filteredCount`

#### Files Modified

- `internal/daemon/monitor.go` - New CheckEligibilityResult, check field first
- `internal/daemon/daemon.go` - Silent filtering, filteredCount tracking
- `internal/daemon/monitor_test.go` - Updated tests for new return type
- Version updates in main.go, internal/version/version.go

---

## v4.2.1 - January 9, 2026

### Enhanced Eligibility Configuration

This release adds configurable eligibility settings for auto-download and workspace validation.

#### New Features

- **Configurable eligibility settings**: New config keys in `daemon.conf`
  - `eligibility.auto_download_value` - Required value for "Auto Download" field (default: "Enable")
  - `eligibility.downloaded_tag` - Tag added after download (default: "autoDownloaded:true")
  - The field NAME ("Auto Download") remains hardcoded and must be created in workspace

- **Workspace validation**: New `daemon config validate` command
  - Checks if required "Auto Download" custom field exists in workspace
  - Reports field type, section, and available values
  - Provides setup instructions if field is missing

- **API validation method**: New `ValidateAutoDownloadSetup()` API method
  - Used by GUI and CLI to validate workspace configuration
  - Returns detailed information about custom field setup
  - Also available as GUI binding `ValidateAutoDownloadSetup()`

#### Files Modified

- `internal/config/daemonconfig.go` - Add AutoDownloadValue, DownloadedTag to EligibilityConfig
- `internal/daemon/monitor.go` - Uses config values (structure unchanged)
- `internal/service/multi_daemon.go` - Pass new config values through
- `internal/api/client.go` - Add GetWorkspaceCustomFields(), ValidateAutoDownloadSetup()
- `internal/models/file.go` - Add CompanyInfo to UserProfile
- `internal/cli/daemon.go` - Add validate command, update config set/show
- `internal/wailsapp/daemon_bindings.go` - Update DTO, add ValidateAutoDownloadSetup()
- `internal/wailsapp/daemon_bindings_windows.go` - Same updates
- Documentation updates: CLI_GUIDE.md, etc.

---

## v4.2.0 - January 8, 2026

### Unified Daemon Configuration

This release introduces a unified configuration system for the auto-download daemon, replacing scattered settings with a single `daemon.conf` file.

#### New Features

- **Unified `daemon.conf` file**: Single INI config file for all daemon settings
  - Location: `~/.config/rescale/daemon.conf` (Unix) or `%APPDATA%\Rescale\Interlink\daemon.conf` (Windows)
  - Replaces scattered apiconfig settings
  - Organized sections: `[daemon]`, `[filters]`, `[eligibility]`, `[notifications]`

- **CLI config commands**: New `daemon config` subcommand group
  - `daemon config show` - Display current configuration
  - `daemon config path` - Show config file location
  - `daemon config edit` - Open in default editor ($EDITOR)
  - `daemon config set <key> <value>` - Set individual values
  - `daemon config init` - Interactive setup wizard

- **Config file + CLI flags**: Flexible configuration model
  - Daemon reads from config file by default
  - CLI flags override config file values
  - Allows testing without modifying config

- **Windows tray improvements**
  - "Configure..." menu opens GUI to daemon settings
  - "Start Service" reads from daemon.conf

#### v4.1.1 Fixes (included)

- **Tray icon fix**: Changed from PNG to ICO format for proper display on Windows
- **Start Service button**: Tray now has "Start Service" option when daemon is stopped
- **Version fix**: Tray now uses shared version package (no more hardcoded version)
- **Auto-start docs**: Added macOS launchd and Linux systemd configuration examples

### Files Modified

- `internal/config/daemonconfig.go` - NEW: DaemonConfig struct and I/O
- `internal/config/daemonconfig_test.go` - NEW: Unit tests
- `internal/cli/daemon.go` - Load from daemon.conf, add config subcommands
- `internal/wailsapp/daemon_bindings.go` - GetDaemonConfig, SaveDaemonConfig (Unix)
- `internal/wailsapp/daemon_bindings_windows.go` - GetDaemonConfig, SaveDaemonConfig (Windows)
- `cmd/rescale-int-tray/tray_windows.go` - Configure menu, startService uses daemon.conf
- `internal/service/multi_daemon.go` - Use DaemonConfig instead of APIConfig
- `internal/service/multiuser_windows.go` - ConfigPath uses daemon.conf
- `internal/service/multiuser_unix.go` - ConfigPath uses daemon.conf
- Version updates: `main.go`, `internal/version/version.go`, `Makefile`, `wails.json`
- Documentation: `README.md`, `CLI_GUIDE.md`, `RELEASE_NOTES.md`, `FEATURE_SUMMARY.md`

---

## v4.0.8 - January 6, 2026

### Windows Service/Tray Improvements

This release includes comprehensive fixes for the Windows Service and System Tray functionality.

#### Critical Fixes

- **Windows Config Save**: Fixed "path not found" error when saving config on fresh Windows install - now creates parent directory (`%APPDATA%\Rescale\Interlink\`) automatically
- **Unified API Key Resolution**: New `config.ResolveAPIKey()` function provides consistent API key handling across CLI, GUI, and auto-download service with priority: explicit flag → apiconfig → token file → environment variable
- **Tray User Resolution**: Fixed tray app pause/resume failing when service runs as SYSTEM - now resolves username on client side before IPC call

#### Windows-Specific

- **Standard Config Paths**: Changed Windows config location from `%USERPROFILE%\.config\rescale\` to standard `%APPDATA%\Rescale\Interlink\`
- **Multi-Resolution Icons**: Regenerated `build/windows/icon.ico` with 7 resolutions (16-256px) - was only 16px causing blank icon display
- **Tray Icon**: Upgraded `cmd/rescale-int-tray/assets/icon.png` to 128x128 for better high-DPI display
- **MSI Installer Icons**: Added explicit icon declarations for all shortcuts (Desktop, Start Menu GUI/CLI, Add/Remove Programs)
- **Version Update**: Installer version updated to 4.0.8.0

#### Service Improvements

- **Daemon Stats**: Added `GetLastPollTime()`, `GetDownloadedCount()`, `GetActiveDownloads()` methods
- **IPC Status**: Wired up SID, LastScanTime, JobsDownloaded, ActiveDownloads to IPC responses
- **Per-User Scan**: Added `TriggerUserScan()` for triggering individual user daemon scans
- **Activity Logging**: Auto-download config save now logs to GUI Activity tab

### Previous v4.0.8 Changes (January 3, 2026)

#### Critical Bug Fixes

- **Software Scan Timeout**: Fixed "context deadline exceeded" error when scanning software/hardware in job template configuration. Paginated API calls now use 5-minute timeout instead of 30 seconds to handle rate limiting.

#### Bug Fixes

- **Folder Transfer UX**: Added enumeration events showing real-time scan progress (files/folders/bytes found) in Transfers tab during folder upload/download
- **Windows ZIP Compatibility**: Fixed 7-Zip extraction errors by using 7-Zip (instead of PowerShell's Compress-Archive) to create portable distribution ZIPs
- **Missing Slots Validation**: Added validation for `Slots > 0` in job spec - previously would fail at runtime
- **Directory Existence Check**: Single job submission now verifies directory exists before starting
- **Status Value Inconsistency**: Fixed `getJobStats()` to recognize both "success" and "completed" status values
- **State Manager Iteration**: Fixed PUR state file save/load to handle non-consecutive job indices during resume

### Files Modified

- `internal/config/apikey.go` - NEW: Unified API key resolution
- `internal/config/csv_config.go` - Added `os.MkdirAll`, Windows %APPDATA% paths
- `internal/config/apiconfig.go` - Windows %APPDATA% paths
- `internal/service/multi_daemon.go` - Use unified API key, added stats methods
- `internal/daemon/daemon.go` - Added stats methods, activeDownloads counter
- `internal/service/ipc_handler.go` - Wired up all stats fields
- `cmd/rescale-int-tray/tray_windows.go` - Added `getCurrentUsername()`, updated IPC calls
- `internal/wailsapp/config_bindings.go` - Added Activity tab logging
- `installer/rescale-interlink.wxs` - Version 4.0.8.0, icon declarations
- `installer/rescale-interlink.ico` - NEW: Icon file for MSI shortcuts
- `build/windows/icon.ico` - Regenerated multi-resolution
- `cmd/rescale-int-tray/assets/icon.png` - Regenerated 128x128
- `internal/constants/app.go` - Added `PaginatedAPITimeout`
- `internal/wailsapp/job_bindings.go` - Updated timeouts, added validations
- `internal/events/events.go` - Added `EnumerationEvent` type
- `internal/wailsapp/event_bridge.go` - Forward enumeration events
- `internal/wailsapp/file_bindings.go` - Emit enumeration events during folder scan
- `internal/core/engine.go` - Fixed status value check
- `internal/pur/state/state.go` - Fixed map iteration
- `frontend/src/stores/transferStore.ts` - Handle enumeration events
- `frontend/src/components/tabs/TransfersTab.tsx` - Display scan progress
- `scripts/release-windows-wails-build.sh` - Use 7-Zip for packaging

---

## v4.0.7 - January 2, 2026

### Critical Bug Fixes

- **Infinite Polling Loop**: Fixed memory leak in SingleJobTab caused by polling loop not being properly cancelled
- **Job ID Race Condition**: Fixed race where job ID was set after state transition, causing missed updates

### High Priority Fixes

- Removed broken `explorer.exe` call from SYSTEM service context (Windows service)
- Documented IPC security descriptor implications
- Updated all documentation versions to v4.0.7

### Medium Priority Fixes

- Fixed hardcoded Windows paths in `ipc_handler.go`
- Added error checking to tray app `cmd.Start()` calls
- Changed WiX service install to `Return="ignore"` to not block installation

### Low Priority Fixes

- Clean up `fileInfoMap` cache on file removal
- Removed unused `CreateStatus` column from PUR table

---

## v4.0.6 - January 2, 2026

### Bug Fixes

- **Large File Size Precision**: Fixed precision loss for files >16TB by changing `float64` to `int64` for file sizes
- **API Error Propagation**: Errors from API calls now properly propagate to frontend with error DTOs
- **PUR Upload Progress**: Added progress display during PUR job uploads
- **UI Label Fix**: Changed "Scan Hardware" button label to "Scan Coretypes" for accuracy

---

## v4.0.5 - January 2, 2026

### Bug Fixes
- Fixed nil slice crash in GUI when no templates exist (job_bindings.go)
- Fixed nil slice crash for empty directory operations (file_bindings.go)
- Fixed nil slice crash in transfer task listing (transfer_bindings.go)
- Fixed silent error discarding in file cache operations

### Improvements
- Centralized timeout constants (PartOperationTimeout, ProgressUpdateInterval)
- Removed dead legacy localfs functions (ListDirectory, Walk, WalkFiles)
- Updated 9 files to use centralized constants

### Documentation
- Fixed chunk size documentation (16MB → 32MB)
- Version consistency across all documentation files

---

## v4.0.4 - January 2, 2026

### Bug Fixes & Code Quality

This release fixes critical nil slice bugs that could cause GUI crashes, improves error handling, and centralizes previously hardcoded constants.

#### Critical Bug Fixes

- **Nil slice crash fix (4 locations)**: Fixed Go functions returning `nil` slices to JavaScript frontend, which caused "null is not an object" crashes when accessing `.length`:
  - `job_bindings.go:ListSavedTemplates()` - crashed when no templates existed
  - `file_bindings.go:SelectDirectoryFiles()` - crashed on empty directories
  - `file_bindings.go:SelectDirectoryRecursive()` - crashed when no files found
  - `transfer_bindings.go:GetTransferTasks()` - crashed when engine not initialized

- **Silent error handling**: `file_bindings.go:UploadFolderWithProgress()` now logs cache warming failures instead of silently discarding errors

#### Version Consistency

- Fixed `tray_windows.go` version (was "4.0.0", now "4.0.4")
- Updated all documentation files to v4.0.4
- Fixed `main.go` version typo (was "v4.0.3")

#### Constants Centralization

- Added `PartOperationTimeout = 10 * time.Minute` to `constants/app.go`
- Added `ProgressUpdateInterval = 500 * time.Millisecond` to `constants/app.go`
- Replaced 9 hardcoded values across S3/Azure providers and transfer code

#### Code Cleanup

- Removed dead legacy functions from `localfs/browser.go`:
  - `ListDirectory()` → replaced by `ListDirectoryEx()`
  - `Walk()` → replaced by `WalkCollect()`
  - `WalkFiles()` → replaced by `WalkCollect()`

---

## v4.0.3 - January 1, 2026

### Code Cleanup & Robustness Improvements

This release focuses on code quality, documentation accuracy, and robustness improvements for the local file browser.

#### Dead Code Removal
- Removed unused `internal/notify/` package
- Removed unused `internal/state/file_list_state.go`

#### Race Condition Fix
- Fixed race condition in `transfer/queue.go:UpdateProgress()` where task fields (Progress, Speed, lastUpdateTime) were written without proper synchronization
- Lock is now held for entire update sequence with event publishing outside the lock

#### Local File Browser Robustness (v4.0.3)

Four new robustness features for the local file browser:

1. **Timeout Protection**: 30-second timeout prevents UI freeze on hung NFS/SMB mounts
2. **Hidden File Filtering**: Server-side filtering using `localfs.IsHiddenName()` - reduces data transfer
3. **Cancellation Support**: Previous directory operation is automatically cancelled when new navigation starts
4. **Parallel Symlink Resolution**: 8-worker pool for `os.Stat()` calls on symlinks

**New API:**
- `ListLocalDirectoryEx(path, includeHidden)` - new method with hidden file control
- `CancelLocalDirectoryRead()` - explicit cancellation for frontend
- `FolderContentsDTO` now includes `isSlowPath` and `warning` fields

**New Constants:**
- `DirectoryReadTimeout = 30s`
- `SlowPathWarningThreshold = 5s`
- `SymlinkWorkerCount = 8`

#### Documentation Fixes
- Fixed incorrect chunk size comment in Azure download (64MB → 32MB)
- Updated ARCHITECTURE.md ChunkSize documentation (16MB → 32MB)
- Updated all version strings to v4.0.3

---

## v4.0.2 - December 30, 2025

### Server-Side Pagination for File Browser

Added proper server-side pagination to the File Browser remote panel, fixing performance issues with large folders.

#### Changes
- New `ListRemoteFolderPage()` method with cursor-based pagination
- Frontend "Load More" button for incremental loading
- Configurable page size (default: API default, typically 100)
- Fixed My Jobs folder listing

---

## v4.0.1 - December 28, 2025

### Bug Fixes and Polish

- Fixed version display (was showing "dev" instead of "v4.0.1")
- Workspace name and ID displayed in GUI header after connecting
- Auto-switch to Transfers tab after starting upload/download

---

## v4.0.0 - December 27, 2025

### Complete GUI Rewrite: Fyne to Wails Migration

This major release completely rewrites the graphical user interface using [Wails](https://wails.io/) with a React/TypeScript frontend, replacing the previous Fyne-based GUI.

#### Why Wails?

- **Modern UI**: React enables a polished, responsive interface with Tailwind CSS
- **Better Performance**: Virtual scrolling for large file lists (TanStack Table)
- **Smaller Binary**: 20MB macOS arm64 (down from 29MB with Fyne)
- **Web Technologies**: Easier to style, test, and maintain
- **Cross-Platform**: WebView2 (Windows), WebKitGTK (Linux), WKWebView (macOS)

#### GUI Architecture

**Frontend (React/TypeScript):**
- `frontend/src/App.tsx` - Main application with tab navigation
- `frontend/src/components/tabs/` - Six tab implementations:
  - SetupTab - Configuration and API key management
  - SingleJobTab - Single job configuration and submission
  - PURTab - Parallel Upload Run batch pipeline
  - FileBrowserTab - Two-pane file browser (local/remote)
  - TransfersTab - Active transfer queue with progress
  - ActivityTab - Real-time logs with filtering
- `frontend/src/stores/` - Zustand state management
- `frontend/wailsjs/` - Auto-generated Go bindings

**Backend (Go Bindings):**
- `internal/wailsapp/app.go` - Main Wails application
- `internal/wailsapp/config_bindings.go` - Configuration methods
- `internal/wailsapp/transfer_bindings.go` - Upload/download methods
- `internal/wailsapp/file_bindings.go` - File browser methods
- `internal/wailsapp/job_bindings.go` - Job submission methods
- `internal/wailsapp/event_bridge.go` - EventBus to Wails events

#### Features Preserved

All functionality from the Fyne GUI has been ported:
- Configuration management with test connection
- Job template builder with searchable software/hardware
- CSV/JSON/SGE job file load/save
- Two-pane file browser with My Library/My Jobs/Legacy modes
- Multi-file selection with upload/download
- Transfer queue with progress, cancel, retry
- Real-time activity logging with filtering
- Directory scanning for PUR batch jobs

#### CLI Unchanged

All command-line functionality remains exactly the same:
- `rescale-int config`, `files`, `folders`, `jobs`, `pur` commands
- Progress bars, shell completion, all flags

#### Breaking Changes

- **Build Process**: Now requires Node.js 18+ for frontend build
- **Dependencies**: Fyne dependencies removed, Wails dependencies added
- **Binary Name**: Still `rescale-int`, but includes WebView runtime

#### Build Instructions

```bash
# Install Wails CLI
go install github.com/wailsapp/wails/v2/cmd/wails@latest

# Install frontend dependencies
cd frontend && npm install && cd ..

# Build with FIPS 140-3 compliance
GOFIPS140=latest wails build -platform darwin/arm64
```

#### Migration Notes

- Old Fyne code archived in `_archive_fyne_gui/` for reference
- Configuration files and API tokens unchanged - no user action needed
- All CLI scripts and workflows continue to work

#### E2E Testing Results

All CLI and core functionality tested on both S3 and Azure backends:
- File uploads/downloads (small, medium, large, multi-part)
- Folder uploads with concurrent transfers
- Job output downloads
- Streaming encryption verified
- Rate limiters verified
- GUI launch and basic operation verified

---

### v4.0.0-dev Bug Fixes (December 27, 2025)

Following the Wails migration, these bugs were discovered and fixed:

#### Critical Bug Fixes

1. **Download "is a directory" Error**
   - **Problem**: Downloads failed with "is a directory" when a remote folder contained both a file and subdirectory with the same name
   - **Root Cause**: After creating a subdirectory `layer3_dir3/`, attempting to download file `layer3_dir3` failed because the path existed as a directory
   - **Fix**: Added directory conflict detection before download; files are automatically renamed with `.file` suffix when conflicting with existing directories
   - **Files Modified**: `internal/cli/folder_download_helper.go:290-302`, `internal/cli/download_helper.go:195-203, 589-597`

2. **Download Path Missing Filename (GUI)**
   - **Problem**: GUI file browser passed only the directory path to downloads, not the full file path
   - **Fix**: `FileBrowserTab.tsx` now constructs full path with filename; `transfer_service.go` adds safety check
   - **Files Modified**: `frontend/src/components/tabs/FileBrowserTab.tsx:253-263`, `internal/services/transfer_service.go:350-359`

#### GUI Improvements

3. **Checkboxes Added to File Browser**
   - **Problem**: File selection only indicated by subtle background color change
   - **Fix**: Added explicit checkbox column to FileList component for clear selection indication
   - **File Modified**: `frontend/src/components/widgets/FileList.tsx:195-196, 245-262`

4. **Version Display Fixed**
   - **Problem**: Version showed "dev" instead of "v4.0.0-dev"
   - **Fix**: Changed default Version in cli/root.go from "dev" to "v4.0.0-dev"
   - **File Modified**: `internal/cli/root.go:44`

5. **Workspace Info in Header**
   - **Feature**: Display connected workspace name and ID in header after successful API connection
   - **Files Modified**: `internal/models/file.go:86-98`, `internal/wailsapp/config_bindings.go:140-178`, `frontend/src/App.tsx`, `frontend/src/stores/configStore.ts`, `frontend/src/types/events.ts`

6. **Auto-switch to Transfers Tab**
   - **Feature**: After starting upload or download, automatically switch to Transfers tab to show progress
   - **Implementation**: Added `TabNavigationContext` in `App.tsx`, consumed in `FileBrowserTab.tsx`
   - **Files Modified**: `frontend/src/App.tsx:26-31, 52-58`, `frontend/src/components/tabs/FileBrowserTab.tsx:12, 86-87, 210-211, 277-278`

#### Dead Code Removal

7. **PartTimer Class Removed**
   - Removed unused `PartTimer` struct and all its methods from `internal/cloud/timing.go`
   - Removed corresponding tests from `timing_test.go`
   - Verified not called anywhere in codebase outside its own definition

8. **DefaultWalkOptions Function Removed**
   - Removed unused `DefaultWalkOptions()` from `internal/localfs/options.go`
   - Removed corresponding test from `localfs_test.go`
   - Verified only called in test file

---

## v3.6.0 - December 23, 2025

### Architectural Foundation - Local Filesystem Abstraction

This release addresses P0 architectural debt by consolidating duplicated local filesystem operations into a unified `internal/localfs/` package.

#### New Package: `internal/localfs/`

Created a unified local filesystem abstraction that eliminates code duplication across CLI, GUI, and core packages:

**Files Created:**
- `internal/localfs/hidden.go` - `IsHidden()`, `IsHiddenName()` functions
- `internal/localfs/options.go` - `ListOptions`, `WalkOptions` structs
- `internal/localfs/browser.go` - `ListDirectory()`, `Walk()`, `WalkFiles()` functions
- `internal/localfs/localfs_test.go` - Comprehensive unit tests

**Functions:**
```go
func IsHidden(path string) bool         // Check if path is hidden (starts with .)
func IsHiddenName(name string) bool     // Check if filename is hidden
func ListDirectory(path string, opts ListOptions) ([]FileEntry, error)
func Walk(root string, opts WalkOptions, fn WalkFunc) error
func WalkFiles(root string, opts WalkOptions, fn WalkFunc) error
```

#### Hidden File Detection Consolidation

Migrated 9 duplicate `strings.HasPrefix(name, ".")` checks to use the unified `localfs.IsHidden()`:

| File | Change |
|------|--------|
| `internal/util/multipart/multipart.go` | Uses `localfs.IsHidden()` |
| `internal/cli/folder_upload_helper.go` | Uses `localfs.IsHidden()` |
| `internal/core/engine.go` | Uses `localfs.IsHidden()` (4 locations) |
| `internal/gui/scan_preview.go` | Uses `localfs.IsHiddenName()` |
| `internal/gui/local_browser.go` | Uses `localfs.IsHiddenName()` |

#### GUI Page Size Consistency

- Removed `SetPageSize(200)` override in `remote_browser.go`
- Remote browser now uses default page size (25) for consistency with local browser

#### Windows Download Timing Instrumentation

Added timing instrumentation to diagnose end-of-download slowness on Windows:

- Enable with `RESCALE_TIMING=1` environment variable
- Outputs:
  - `[TIMING] Download transfer complete: Xms`
  - `[TIMING] Checksum verification: Xms`

**Root Cause Hypothesis:** The `verifyChecksum()` function reads the entire file after download to compute SHA-512. For large files, this causes the "slow at end" behavior users reported.

#### Files Modified

- `internal/cloud/download/download.go` - Added timing instrumentation
- `internal/gui/remote_browser.go` - Removed page size override
- `internal/gui/local_browser.go` - Uses localfs package
- `internal/gui/scan_preview.go` - Uses localfs package
- `internal/core/engine.go` - Uses localfs package
- `internal/cli/folder_upload_helper.go` - Uses localfs package
- `internal/util/multipart/multipart.go` - Uses localfs package

---

## v3.5.0 - December 22, 2025

### File Browser Robustness - Definitive Fix for Leftover Entries Bug

This release comprehensively addresses the "leftover folder entries" bug in the GUI file browser, where old entries from the previous directory could remain visible alongside new content when navigating.

#### Root Cause Analysis

Multiple failure modes in Fyne's `widget.List` contributed to the bug:
1. **Recycled row objects not fully overwritten** - Fyne pools/reuses list item objects; partial updates left stale data
2. **canvas.Text mutations without Refresh()** - Mutating text fields doesn't trigger repaint automatically
3. **Selection state persistence** - Selection highlight could persist across dataset swaps
4. **Race conditions in UpdateItem** - Async scroll/refresh ordering caused stale indices to render

#### Solution: Multi-Layered Defense

**Typed Row Template** (`fileRowTemplate` struct)
- Eliminates brittle `row.Objects[...]` indexing with type-safe field access
- Stored via `sync.Map` for safe concurrent access
- Compile-time safety instead of runtime panics

**Total Overwrite + Refresh Pattern**
- `updateListItem()` now sets EVERY visible field in ALL code branches
- Explicit `.Refresh()` calls on every `canvas.Text` and `widget.RichText` after mutation
- `blankListItem()` properly clears recycled rows AND refreshes all mutated objects

**Generation Gating** (`viewGeneration` counter)
- Incremented on ANY change that invalidates in-flight row updates
- `updateListItem()` captures generation before accessing items, validates after unlock
- Stale callbacks detect mismatch and blank the row instead of showing wrong data

**UnselectAll on Navigation**
- `SetItemsAndScrollToTop()` calls `list.UnselectAll()` before data swap
- Prevents selection highlight from persisting between folders

**Double Refresh Pattern**
- First refresh updates `Length` and triggers `UpdateItem` for visible rows
- Second refresh scheduled "next tick" handles scroll/length edge cases in Fyne

**Safety Guard in Open Handler**
- `onItemTapped()` validates item ID exists in index before taking action
- Prevents stale visual from triggering wrong folder open

#### Other Changes

- **Local Delete Button Removed** - Simplified UI; local file deletion managed by OS file manager
- **Debug Mode Enhancement** - Set `RESCALE_GUI_DEBUG=1` for detailed file browser state logging

#### Files Modified

- `internal/gui/file_list_widget.go` - Core robustness fixes
- `internal/gui/file_browser_tab.go` - Removed local delete functionality

---

## v3.4.10 - December 21, 2025

### File Browser Deep Fix

This release fixes remaining file browser issues discovered during testing of v3.4.9. User testing revealed:
- **Stale content mixing** - navigating to new folder showed contents of previous folder
- **Duplicate folder entries** appearing in listings
- **Missing folders** - wrong folders displayed
- **Browse button lockup** - folder icon button caused "Not Responding" on network drives

#### Root Cause

**Primary Bug**: `SetItemsAndScrollToTop()` never cleared `w.filteredItems`. When user navigated with a filter active, old filtered items persisted and `getDisplayItemsLocked()` returned them instead of the new directory's contents.

#### Fixes

**FileListWidget (internal/gui/file_list_widget.go)**

- Clear `w.filteredItems = nil` at start of `SetItemsAndScrollToTop()`
- Recompute filter for new items if `filterQuery != ""` (preserves user's filter)
- Added deduplication in `AppendItems()` - checks `itemIndexByID` before appending to prevent duplicate entries

**LocalBrowser (internal/gui/local_browser.go)**

- Removed `browseBtn` (folder open button) - caused Windows lockups with network drives due to native folder picker blocking UI during enumeration
- Removed `showFolderDialog()` function
- Removed unused `dialog` import

**RemoteBrowser (internal/gui/remote_browser.go)**

- Session binding already in place (generation check inside `fyne.Do()` before `AppendItems`)

#### Verification

All fixes are O(n) or better, with no performance impact on local filesystem browsing:
- Filter recompute: O(n) but only when filter is active
- Existing sort: O(n log n) - dominates navigation time
- Deduplication: O(1) per item via `itemIndexByID` map lookup

---

## v3.4.9 - December 21, 2025

### Critical File Browser Race Condition Fix

This release fixes critical bugs discovered in v3.4.8 that could cause:
- **Path/content mismatch** - displayed path doesn't match displayed contents (data safety risk)
- **Duplicate folder entries** appearing after rapid navigation
- **Incorrect sorting** when page size is increased

#### Root Cause

The bugs were caused by a race condition in the file browser's navigation handling:

1. **fyne.Do() Timing Window**: Generation check happened BEFORE `fyne.Do()`, but `fyne.Do()` is async. Between the check and callback execution, another navigation could start, allowing stale data through.

2. **Generation Drift**: `loadDirectory()` read `navGeneration` after being spawned instead of receiving it as a parameter, causing slow-starting goroutines to pick up wrong generation values.

#### Fix Summary

**LocalBrowser (internal/gui/local_browser.go)**

- Created `applyDirectoryLoadResult()` function that checks generation INSIDE `fyne.Do()` callback - atomic with UI update
- Changed `loadDirectory()` to accept `loadGen` parameter instead of reading it internally
- Updated all callers (`navigateTo`, `goBack`, `refresh`, `checkAndLoadPending`) to pass generation explicitly
- Added `isLoading` atomic flag for state tracking
- Clear selection on navigation start to prevent stale selections

**RemoteBrowser (internal/gui/remote_browser.go)**

- Moved generation check inside `fyne.Do()` callback in `loadMoreItems()`
- Added selection clearing on all navigation functions (`navigateToFolder`, `navigateToBreadcrumb`, `goUp`, `onRootChanged`)

#### New Features

**Timing Visibility**

- Added `--timing` flag and `RESCALE_TIMING=1` environment variable
- Outputs timing metrics to stderr (independent of logger level)
- Format: `[TIMING] path: total=Xms readdir=Xms symlink=Xms items=N`

**Enhanced Mesa Diagnostics**

- `--mesa-doctor` now shows LOADED MODULES before and after DLL preload
- Identifies which opengl32.dll is actually loaded (System32 vs Mesa)
- Warns if Windows' built-in OpenGL is active instead of Mesa

---

## v3.4.8 - December 19, 2025

### Deep Performance Optimization + Windows Mesa Variants

This release delivers comprehensive GUI performance optimizations and introduces two Windows build variants for different deployment scenarios.

#### Performance Optimizations

**FileListWidget (internal/gui/file_list_widget.go)**

- Removed 4 redundant `Refresh()` calls in `updateListItem()` - parent list handles refresh
- Precomputed lowercase names before sorting - reduces allocations from 20,000 to 2,000 for 1,000 items
- Added `itemIndexByID` map for O(1) selection lookups (was O(n) linear search)
- Added `lowerName` field to `FileItem` for O(1) filtering
- Combined two separate `fyne.Do()` calls in `Refresh()` method

**FileBrowserTab (internal/gui/file_browser_tab.go)**

- Removed redundant `bar.Refresh()` in progress interpolation - `SetValue()` already triggers refresh
- Batched lock acquisition in `initProgressUIWithFiles()` and `initProgressUIForDownloads()` - single lock instead of per-file

**JobsTab (internal/gui/jobs_tab.go, jobs_integration.go)**

- Added `jobIndexByName` map for O(1) job updates during pipeline execution (was O(n) linear search)
- Updates to `UpdateProgress()` and `UpdateJobState()` now use map lookup

**ActivityTab (internal/gui/activity_tab.go)**

- Added `formattedCache` and `lowerCache` fields to `LogEntry` struct
- Formatted text and lowercase computed once when log is added, not during every filter

#### Windows Build Variants

Two Windows binaries are now available to accommodate different deployment environments:

**Standard Build** (`rescale-int-windows-amd64.zip`)
- Smaller binary size (~25MB smaller)
- Requires hardware GPU with OpenGL support
- Use this for workstations with dedicated graphics

**Mesa Build** (`rescale-int-windows-amd64-mesa.zip`)
- Includes embedded Mesa3D software renderer
- Works on VMs, RDP sessions, and headless servers
- Use this for environments without GPU access

**Build Tags:**
```bash
# Standard build (default)
make build-windows-amd64

# Mesa build
make build-windows-amd64-mesa

# Both variants
make build-windows-all
```

**Files Added:**
- `internal/mesa/embed_mesa_windows.go` - Mesa DLL embedding (build tag: mesa)
- `internal/mesa/embed_nomesa_windows.go` - Empty stub for standard build

**Files Modified:**
- `internal/mesa/mesa_windows.go` - Refactored to use build-tag-selected DLL map
- `Makefile` - Added Windows build variants and packaging

#### Building on v3.4.7

This release builds on the v3.4.7 network filesystem fixes:
- Using `entry.Info()` (cached metadata from `os.ReadDir()`) instead of `os.Stat()` (extra network call)
- Eliminates catastrophic slowdowns on network-mounted filesystems

---

## v3.4.5 - December 18, 2025

### Accelerated Scroll Implementation

This release replaces the experimental FastScroll widget with a proper custom scroll implementation that actually receives scroll events.

#### New Features

**AcceleratedScroll Widget**

The previous `FastScroll` wrapper didn't work because Fyne routes scroll events directly to the deepest `Scrollable` widget, bypassing our wrapper. The new `AcceleratedScroll` widget is built from scratch:

- **Direct Event Handling**: Extends `widget.BaseWidget` and implements `fyne.Scrollable` directly, so scroll events are received
- **3x Scroll Speed**: Default multiplier of 3.0 makes scrolling feel more natural (Fyne's default ~12px is too slow)
- **Draggable Scroll Bars**: Includes proper scroll bars that can be dragged to navigate
- **Theme-Aware**: Uses Fyne's theme colors for scroll bar rendering

**Files Added:** `internal/gui/accelerated_scroll.go` (~380 lines)
**Files Removed:** `internal/gui/fast_scroll.go` (non-functional)

**Integration:**

All 10 scroll containers in the GUI now use AcceleratedScroll:
- Activity Tab: Log view, export dialog
- Jobs Tab: Jobs table
- File Browser Tab: File progress list
- Setup Tab: Settings scroll area
- Single Job Tab: File selection dialog
- Template Builder: Form content
- Scan Preview: Table scroll
- Remote Browser: Breadcrumb bar
- SearchableSelect: Dropdown list

---

## v3.4.4 - December 17, 2025

### GUI Usability Improvements

This release focuses on GUI usability improvements including better software/hardware selection workflow, RHEL 9 compatibility, and UI layout enhancements.

#### New Features

**1. Improved Software Configuration Workflow**

- **Analysis Name Display**: Software dropdown now shows "Analysis Name (code)" format instead of just codes for better readability
- **Version Dropdown**: After selecting software, a dropdown is populated with available versions from the API (no more manual version entry required)
- **Core Count Dropdown**: Hardware dropdown now shows valid core counts per hardware type, fetched from the API's `cores` field
- **Better Labels**: Changed "Analysis Code" label to "Analysis" for clarity

**Files Modified:** `internal/gui/template_builder.go`, `internal/models/job.go`

**2. Version Display in GUI**

- **Window Title**: Main window now displays version and FIPS status (e.g., "Rescale Interlink v3.4.4 [FIPS 140-3]")

**Files Modified:** `internal/gui/gui.go`

**3. RHEL 9 / Wayland Compatibility Fix**

Fixed missing minimize/maximize buttons on RHEL 9 and other Wayland-based Linux distributions:

- **Auto X11 Detection**: GUI now automatically detects Wayland and forces X11 backend for consistent window decorations
- **Opt-in Wayland**: Users can set `RESCALE_USE_WAYLAND=1` to use native Wayland support if desired
- **Fallback DISPLAY**: Ensures DISPLAY is set to `:0` if only WAYLAND_DISPLAY was configured

**Files Modified:** `internal/gui/gui.go`

**4. Input File Selection UI Improvement**

Restructured the file selection dialog to make it more obvious that users can select multiple files:

- **Fixed Header**: Instructions and buttons are always visible at top
- **Scrollable File List**: Selected files appear in a dedicated scroll area with clear "Selected files:" label
- **Dynamic Count Bug Fix**: Fixed async callback bug where file count didn't update when adding/removing files

**Files Modified:** `internal/gui/single_job_tab.go`

#### Technical Improvements

**FastScroll Widget (Experimental - Superseded in v3.4.5)**

Added a new `FastScroll` widget to address slow mouse wheel scrolling in Fyne (known issue #775).

**Note:** This widget didn't work as intended because Fyne routes scroll events to the deepest Scrollable widget. See v3.4.5 for the working `AcceleratedScroll` replacement.

**SearchableSelect Enhancement**

- Made `OnChanged` callback public for external assignment

**Files Modified:** `internal/gui/searchable_select.go`

---

## v3.4.3 - December 16, 2025

### Configuration Fixes + GUI Stability

This release fixes critical configuration bugs and GUI stability issues, particularly for users with proxy configurations.

#### Critical Fixes

**1. Engine Mutex Contention Fix (GUI Freeze)**

Fixed a critical issue where the GUI would freeze for extended periods (15+ seconds) when proxy was configured:

- **Root Cause**: `engine.UpdateConfig()` held the mutex during `api.NewClient()`, which includes slow proxy warmup
- **Impact**: Any UI code calling `GetConfig()` or `API()` would block waiting for the lock
- **Fix**: API client is now created BEFORE acquiring the lock, reducing lock hold time to milliseconds

**Files Modified:** `internal/core/engine.go`

**2. Configuration Path Fixes**

Fixed bugs preventing the `config init` workflow from working properly:

- **Token Filename Mismatch**: `config init` saved to `rescale_token` but auto-load expected `token`
- **Config Path Mismatch**: `loadConfig()` used local `config.csv` instead of `~/.config/rescale/config.csv`
- **Config Directory Change**: Moved from `~/.config/rescale-int/` to `~/.config/rescale/` for consistency
- **Migration Support**: Old config location is auto-detected with migration guidance

**Files Modified:**
- `internal/cli/config_commands.go`
- `internal/cli/pur.go`
- `internal/config/csv_config.go`

**3. GUI Thread Safety Fixes**

- `testConnection()` now uses `applyConfigAsync()` instead of blocking `applyConfig()`
- `handleScan()` now runs `os.Stat()` asynchronously (prevents freeze on network drives)

**Files Modified:**
- `internal/gui/setup_tab.go`
- `internal/gui/jobs_workflow_ui.go`

#### Proxy Improvements

- Basic auth proxy now respects `ProxyWarmup` flag (previously always ran warmup regardless)
- Reduced proxy warmup timeout from 30s to 15s

**Files Modified:** `internal/http/proxy.go`

#### Documentation

- Added Linux system requirements: GLIBC 2.27+ (RHEL/CentOS 8+, Ubuntu 18.04+)
- Clarified that CentOS/RHEL 7 is NOT supported (end-of-life, GLIBC too old)
- Updated CLI_GUIDE.md with system requirements section

---

## v3.4.2 - December 16, 2025

### Dynamic Thread Reallocation + Code Cleanup

This release adds dynamic thread scaling for improved transfer throughput and comprehensive code cleanup.

#### New Features

**1. Dynamic Thread Reallocation**

Transfers can now dynamically acquire additional threads mid-flight when they become available:

- **TryAcquire()** - Resource manager method for mid-transfer thread acquisition
- **GetMaxForFileSize()** - Recommends optimal thread count based on file size
- **TryAcquireMore()** - Transfer manager wrapper for seamless thread scaling
- **Background Scaler** - Goroutines check every 500ms for available threads

This improves throughput for large files when other transfers complete and free up threads.

**Files Modified:**
- `internal/resources/manager.go` - New TryAcquire and GetMaxForFileSize methods
- `internal/transfer/manager.go` - New TryAcquireMore method
- `internal/cloud/transfer/downloader.go` - Background scaler goroutine
- `internal/cloud/upload/upload.go` - Background scaler goroutine

#### Bug Fixes

**2. GUI Thread Safety (setup_tab.go)**

Fixed Fyne thread safety error in `applyConfig()`:
- Wrapped `statusLabel.SetText()` in `fyne.Do()` block
- Prevents "Error in Fyne call thread" error on Linux

**Files Modified:** `internal/gui/setup_tab.go`

#### Code Cleanup

**Dead Code Removed:**
- `internal/crypto/streaming.go` - Removed `CalculateTotalEncryptedSize()` (never called)
- `internal/cloud/transfer/download_helpers.go` - Removed ~450 lines of unused download infrastructure
- `internal/gui/layout_helpers.go` - Removed unused button helper functions

**Stale Comments Removed:**
- `internal/cli/download_helper.go` - Removed "NOTE: buildJobFileOutputPaths was removed" comment
- `internal/gui/template_builder.go` - Removed "NOTE: handleLoadFromSGE and populateFormFromJob were removed" comment

**Version Comments Cleaned (65+ instances):**

Per Go community standards, removed `// vX.Y.Z:` prefix annotations from comments across ~19 files. Git history tracks when changes were made; comments should explain WHY, not WHEN.

Files affected:
- `internal/gui/*.go` - Fyne thread safety comments
- `internal/cli/*.go` - Bug fix comments
- `internal/cloud/**/*.go` - CBC format comments
- `internal/core/engine.go` - Goroutine lifecycle comments
- `internal/http/proxy.go` - Proxy warmup comments

**Directory Consolidation:**
- Merged `internal/utils/` into `internal/util/` for naming consistency
- Updated all imports referencing the old path

#### Documentation

- Fixed ARCHITECTURE.md: "dual token bucket" → "three-scope token bucket" rate limiting
- Updated all version references and dates to December 16, 2025
- Synchronized version across README, TODO_AND_PROJECT_STATUS, and RELEASE_NOTES

#### Testing

- All unit tests pass
- E2E tested with both S3 and Azure backends
- Linux build tested on Rescale platform

---

## v3.4.0 - December 12, 2025

### Background Service Mode + GUI Stability + Upload Pipelining

This release introduces a background service mode (daemon) for automatically downloading completed jobs, critical GUI thread safety and stability fixes, race condition fixes, and upload pipelining for 30-50% improved upload speeds.

#### New Features

**1. Background Service Mode (`daemon` command)**

New daemon subsystem that polls for completed jobs and automatically downloads their output files:

```bash
# Start daemon (foreground mode, Ctrl+C to stop)
rescale-int daemon run --download-dir ./results

# With job name filtering
rescale-int daemon run --download-dir ./results --name-prefix "MyProject"
rescale-int daemon run --download-dir ./results --name-contains "simulation"
rescale-int daemon run --download-dir ./results --exclude "Debug" --exclude "Test"

# Configure poll interval (default: 5 minutes)
rescale-int daemon run --poll-interval 2m

# Run once and exit (useful for cron jobs)
rescale-int daemon run --once --download-dir ./results

# Check daemon state
rescale-int daemon status

# List downloaded/failed jobs
rescale-int daemon list
rescale-int daemon list --failed

# Retry failed downloads
rescale-int daemon retry --all
rescale-int daemon retry --job-id XxYyZz
```

Key features:
- Persistent state tracking (downloaded jobs, failed downloads)
- State file: `~/.config/rescale-int/daemon-state.json`
- Job name filtering (prefix, contains, exclude)
- Output directories include job ID suffix to prevent collisions
- Graceful shutdown on Ctrl+C
- Integration with existing download infrastructure (checksums enabled)

**Files Created:**
- `internal/daemon/state.go` - State persistence
- `internal/daemon/monitor.go` - Job monitoring and filtering
- `internal/daemon/daemon.go` - Main daemon orchestration
- `internal/cli/daemon.go` - CLI commands
- `internal/daemon/state_test.go` - Unit tests
- `internal/daemon/monitor_test.go` - Unit tests

**Files Modified:** `internal/cli/root.go`

#### Bug Fixes

**2. GUI Config Save Blocking/Lockup**

Fixed critical bug where saving configuration would lock up the GUI for up to 30 seconds:
- Root cause: `engine.UpdateConfig()` called `api.NewClient()` which performed proxy warmup synchronously
- Solution: Created `applyConfigAsync()` with progress dialog showing "Initializing API client..."
- Config apply now runs in background goroutine with proper panic recovery

**Files Modified:** `internal/gui/setup_tab.go`

**3. GUI Tab Switching Stability**

Fixed GUI freezes when switching tabs:
- Auto-apply config on tab switch now runs in background goroutine
- Added panic recovery to prevent crashes
- Added `fyne.Do()` wrappers for thread safety in remote browser

**Files Modified:** `internal/gui/gui.go`, `internal/gui/remote_browser.go`

**4. Transfer Goroutine Panic Recovery**

Added panic recovery to upload and download goroutines:
- Prevents GUI crash on unexpected errors during transfers
- Shows error dialog instead of crashing
- Resets transfer state properly on failure

**Files Modified:** `internal/gui/file_browser_tab.go`

**5. My Jobs Upload Button**

Upload button now correctly disabled when viewing "My Jobs":
- Added `IsJobsView()` method to RemoteBrowser
- Button shows "Upload (N/A in Jobs)" when files are selected in Jobs view
- Root change callback triggers button state update

**Files Modified:** `internal/gui/file_browser_tab.go`, `internal/gui/remote_browser.go`

**6. Proxy Launch Failure**

Fixed GUI failing to launch when proxy is configured without saved password:
- Previously, proxy warmup would fail immediately if password was missing
- Now skips warmup when credentials are incomplete
- GUI can prompt for password before attempting warmup

**Files Modified:** `internal/http/proxy.go`

#### Documentation

**7. Encryption and Transfer Architecture**

Added comprehensive documentation of encryption formats and transfer architecture:
- Documents v0 (legacy), v1 (HKDF), v2 (CBC streaming) formats
- Explains CBC chaining constraint (sequential encryption/decryption)
- Clarifies upload speed is cryptographically constrained (~20-30 MB/s)
- Lists future optimization options (deferred)

**Files Created:** `docs/ENCRYPTION_AND_TRANSFER_ARCHITECTURE.md`

**8. Format Detection Diagnostics**

Added diagnostic output for download format detection:
- `--verbose` now shows which encryption format is detected
- Helps debug temp file issues (v0 legacy creates temp files, v2 CBC does not)

**Files Modified:** `internal/cloud/transfer/downloader.go`

#### GUI Thread Safety & Stability (Critical)

**9. Activity Tab Thread Safety**

Fixed crash on Linux/Wayland when clearing logs:
- `clearLogs()` called `SetText()` without `fyne.Do()` wrapper
- Now properly wraps widget updates for thread safety

**Files Modified:** `internal/gui/activity_tab.go`

**10. Jobs Tab Thread Safety**

Fixed table refresh crashes from background threads:
- `table.Refresh()` in `UpdateProgress()` and `throttledRefresh()` now wrapped in `fyne.Do()`
- Prevents crashes when job monitoring updates trigger table refreshes

**Files Modified:** `internal/gui/jobs_tab.go`

**11. Event Monitor Panic Recovery**

Added panic recovery to GUI event monitoring goroutines:
- `monitorProgress`, `monitorLogs`, `monitorStateChanges` now have `defer recover()`
- Prevents GUI freeze if any event handler panics
- Added context-based shutdown for `monitorGoroutines()`

**Files Modified:** `internal/gui/gui.go`

**11a. Background Goroutine Panic Recovery (Audit Follow-up)**

Added panic recovery to additional background goroutines identified in comprehensive audit:
- `fetchCoreTypes()` - background API fetch goroutine
- pprof debug server goroutine (debug mode only)

**Files Modified:** `internal/gui/jobs_tab.go`, `internal/gui/gui.go`

#### Race Condition Fixes

**12. Progress Interpolation Race Condition**

Fixed race in progress bar interpolation ticker:
- Added `interpolateWg` WaitGroup to ensure goroutine exits before restart
- Prevents multiple interpolation goroutines running simultaneously

**Files Modified:** `internal/gui/file_browser_tab.go`

**13. Job Monitoring Race Condition**

Fixed race in `StopJobMonitoring()`:
- Added `monitorWg` WaitGroup to wait for goroutine exit before creating new channel
- Prevents accessing closed channel in monitoring goroutine

**Files Modified:** `internal/core/engine.go`

#### Performance Optimization

**14. Upload Pipelining (30-50% Speed Improvement)**

Implemented encryption/upload pipelining for streaming uploads:
- Previously: Sequential encrypt-then-upload (20-30 MB/s)
- Now: Encryption runs ahead of uploads in separate goroutine
- 3-part channel buffer (~48 MB) hides network latency
- CBC sequential encryption constraint still respected
- Expected improvement: 35-50 MB/s (30-50% faster)

Architecture:
```
Before:  [Encrypt1][Upload1][Encrypt2][Upload2][Encrypt3][Upload3]
After:   [Encrypt1][Encrypt2][Encrypt3]...
                  [Upload1][Upload2][Upload3]...
```

New interface methods added to `StreamingConcurrentUploader`:
- `EncryptStreamingPart()` - encrypts plaintext, returns ciphertext (sequential, CBC)
- `UploadCiphertext()` - uploads already-encrypted data (can pipeline)

**Files Modified:**
- `internal/cloud/transfer/uploader.go` - Added interface methods
- `internal/cloud/providers/s3/streaming_concurrent.go` - Implemented pipelining
- `internal/cloud/providers/azure/streaming_concurrent.go` - Implemented pipelining
- `internal/cloud/upload/upload.go` - Pipelined `uploadStreaming()` function

#### Notes

- FIPS 140-3 compliance: Mandatory for all production builds
- All unit tests pass
- E2E tested with real Rescale API (S3 backend)

---

## v3.2.4 - December 10, 2025

### Bug Fixes & Code Cleanup

This release includes a bug fix, dead code removal, and documentation improvements.

#### Bug Fix

**1. Throughput Monitor Index Bounds Fix**

Fixed potential panic in `ShouldScaleDown` function when exactly 5 throughput samples were present:
- Function checked `len(samples) < 5` but accessed `samples[len(samples)-6:]`, requiring 6 samples
- Changed bounds check to require 6 samples minimum

**Files Modified:** `internal/resources/manager.go`

#### Code Cleanup

**2. Dead Code Removal**

Removed unused code identified during code review:
- Removed unused `globalManagerOnce sync.Once` from credential manager
- Removed unused `mu sync.Mutex` from transfer Manager struct

**Files Modified:** `internal/cloud/credentials/manager.go`, `internal/transfer/manager.go`

**3. Stale Comment Cleanup**

Removed outdated "BUG FIX #3 (v1.0.8):" prefixes from tar utility comments:
- Updated comments in `CreateTarGz`, `CreateTarGzWithOptions`, `GenerateTarPath` functions
- Kept explanatory text, removed historical version references

**Files Modified:** `internal/util/tar/tar.go`

#### Documentation Updates

**4. FIPS 140-3 Build Requirements**

Added explicit FIPS 140-3 build requirements section to CONTRIBUTING.md:
- Documents mandatory GOFIPS140=latest build flag
- Explains RESCALE_ALLOW_NON_FIPS development escape hatch
- Links to Go FIPS 140-3 documentation

**Files Modified:** `CONTRIBUTING.md`

**5. Documentation Version Sync**

Synchronized all documentation files to version 3.2.4:
- README.md, CLI_GUIDE.md, ARCHITECTURE.md, FEATURE_SUMMARY.md
- CONTRIBUTING.md, GITHUB_READY.md, TODO_AND_PROJECT_STATUS.md
- TESTING.md, DOCUMENTATION_SUMMARY.md

#### Notes

- FIPS 140-3 compliance: Mandatory for all production builds
- All E2E tests passed (S3 and Azure backends)

---

## v3.2.0 - November 30, 2025

### GUI Improvements & Bug Fixes

This release focuses on GUI quality improvements, adding JSON job template support and fixing several usability issues in the Single Job Tab and Activity Tab.

#### New Features

**1. JSON Job Template Support**

Added complete JSON format support for job templates in the Single Job Tab:
- **Load from JSON** button loads job configuration from JSON files
- **Save as JSON** button exports current job configuration to JSON
- Complements existing CSV and SGE format support
- Uses existing `config.LoadJobsJSON()` and `config.SaveJobJSON()` backend functions

**Files Modified:** `internal/gui/single_job_tab.go`

#### Bug Fixes

**2. SearchableSelect Duplicate Text Display**

Fixed issue where SearchableSelect widget showed dropdown list when value was set programmatically:
- Added `settingSelection` flag to prevent list display during `SetSelected()` calls
- Dropdown now only appears on user input, not programmatic selection

**Files Modified:** `internal/gui/searchable_select.go`

**3. Fyne Thread Safety Errors**

Fixed "Error in Fyne call thread" warnings in Activity Tab:
- Wrapped all UI widget updates in `fyne.Do()` blocks
- Fixed `refreshDisplay()`, `UpdateOverallProgress()`, and `AddLog()` methods
- Removed redundant `updateStats()` function (merged into `refreshDisplay()`)

**Files Modified:** `internal/gui/activity_tab.go`

**4. Hardware Scan Button Availability**

Improved Hardware scan workflow in Template Builder:
- Button now enables when ANY valid software code is entered (not just after clicking "Scan Available Software")
- Auto-fetches software info from API if user typed a code directly
- Added `showCompatibleHardware()` helper for cleaner code organization

**Files Modified:** `internal/gui/template_builder.go`

#### UI Improvements

**5. Configure New Job Dialog Sizing**

Improved dialog layout to prevent text cutoff:
- Dialog size increased from 850×750 to 900×800
- Scroll content min size increased from 750×550 to 800×600
- Input Files list container increased from 700×120 to 750×150
- Input Files list now uses `fyne.TextWrapWord` for long file/folder paths

**Files Modified:** `internal/gui/template_builder.go`

#### Notes

- Customer-specific command defaults are NOT in source code - they persist in user's `~/.pur-gui/workflow_memory.json` file (delete to reset defaults)
- Source code defaults remain: `Command: "# Enter your command here"`

---

## v3.1.0 - November 29, 2025

### Unified Backend Architecture (Internal Refactor)

This release completes a major internal refactoring of the cloud storage layer without any breaking changes to the CLI or API. The result is cleaner code, better maintainability, and identical functionality.

#### Architecture Changes

**1. Provider Factory Pattern**

All uploads and downloads now use a unified entry point with provider factory:
```go
// Single entry point for uploads
provider := providers.NewFactory().NewTransferFromStorageInfo(storageInfo)
// Routes to S3 or Azure automatically based on storage type
```

**2. New Package Structure**
```
internal/cloud/
├── interfaces.go              # CloudTransfer, UploadParams, DownloadParams
├── state/                     # Shared resume state management
│   ├── upload.go             # UploadResumeState
│   └── download.go           # DownloadResumeState
├── transfer/                  # Orchestration layer (8 files)
│   ├── uploader.go           # Generic upload orchestration
│   ├── downloader.go         # Generic download orchestration
│   └── ...
├── providers/                 # Provider implementations
│   ├── factory.go            # Provider factory
│   ├── s3/                   # S3 provider (5 files)
│   └── azure/                # Azure provider (5 files)
├── upload/
│   └── upload.go             # THE ONLY entry point
└── download/
    └── download.go           # THE ONLY entry point
```

**3. Symmetric Provider Interfaces**

Both S3 and Azure providers now implement identical interfaces:
- `cloud.CloudTransfer`
- `transfer.StreamingConcurrentUploader`
- `transfer.StreamingConcurrentDownloader`
- `transfer.LegacyDownloader`
- `transfer.PreEncryptUploader`

#### Code Quality Improvements

- **-6,375 lines** removed from old upload/download implementations
- **+7,629 lines** of proper architecture (not duplication)
- All `*_concurrent.go` files removed from upload/ and download/ directories
- Provider independence: no imports from old packages
- 28 usages of `RetryWithBackoff` for robust error handling
- Centralized constants via `constants.ChunkSize`

#### Verification

Full E2E test matrix passed:

| Test | S3 | Azure |
|------|-----|-------|
| Streaming upload | PASS | PASS |
| Pre-encrypt upload | PASS | PASS |
| Streaming download | PASS | PASS |
| Legacy (v0) download | PASS | PASS |
| Content integrity | PASS | PASS |

#### No Breaking Changes

- All CLI commands work identically
- All flags work identically
- File format compatibility maintained (both formatVersion 0 and 1)
- Resume state files compatible

---

## v3.0.1 - November 28, 2025

### Streaming Encryption (Major Release)

This major release introduces on-the-fly streaming encryption, eliminating the 2x disk footprint for large file uploads and enabling FIPS 140-3 compliance with Go's native crypto module.

#### Breaking Changes

- **New encryption format (formatVersion=1)**: Files uploaded with v3.0.1 use per-part streaming encryption. The downloader automatically detects the format.
- **FIPS 140-3 REQUIRED**: Non-FIPS builds now **refuse to run** (exit code 2) unless `RESCALE_ALLOW_NON_FIPS=true` is set. This ensures FedRAMP compliance.

#### Streaming Encryption

**1. Per-Part AES-256-CBC Encryption**

Uploads now encrypt each part on-the-fly instead of pre-encrypting the entire file:
- **No temp file**: Eliminates 2x disk usage for large files (50-100GB)
- **Faster uploads**: Overlaps encryption with upload
- **FIPS-compliant**: Uses `crypto/hkdf` (Go 1.24 standard library, FIPS validated)
- **Per-part key derivation**: HKDF-SHA256 derives unique key/IV per part

**Cloud Metadata (formatVersion=1):**
```json
{
  "formatVersion": "1",
  "fileId": "<base64-encoded-32-bytes>",
  "partSize": "<actual-part-size-in-bytes>"
}
```

**Note**: For single-part uploads, `partSize` equals the actual file size. For multipart uploads (≥100MB), `partSize` is 16777216 (16MB).

**2. Automatic Format Detection**

Downloads automatically detect encryption format from cloud metadata:
- `formatVersion=1` (or `"1"`): Streaming per-part decryption
- `formatVersion=0` or absent: Legacy full-file decryption

**3. --pre-encrypt Flag**

For backward compatibility with older clients:
```bash
# Default: streaming encryption (recommended)
rescale-int upload large_file.tar.gz

# Legacy: pre-encrypt entire file (compatible with Python client)
rescale-int files upload large_file.tar.gz --pre-encrypt
```

#### FIPS 140-3 Compliance (Mandatory)

**4. Mandatory FIPS Mode**

FIPS 140-3 compliance is now **enforced at runtime**:
- Non-FIPS binaries exit with code 2 and clear error message
- Development opt-out: `RESCALE_ALLOW_NON_FIPS=true` (shows CRITICAL warnings)
- All crypto uses Go 1.24 standard library FIPS module

**5. Migrated to crypto/hkdf**

Replaced `golang.org/x/crypto/hkdf` with standard library `crypto/hkdf`:
- FIPS 140-3 validated implementation
- No external crypto dependencies for key derivation

#### Bug Fixes (v3.0.1)

**6. Fixed Single-Part Streaming Upload Bug**

Fixed a critical bug in S3/Azure single-part uploads (files < 100MB) where:
- **Problem**: `partSize` metadata was incorrectly stored as 16MB (default chunk size) instead of actual file size
- **Impact**: Downloads of files 16-99MB failed with "invalid padding" errors (e.g., 50MB files)
- **Root Cause**: Download code calculated `numParts = ceil(encryptedSize / partSize)` = 4 parts for a 50MB file, but the file was encrypted as a single block
- **Fix**: Single-part uploads now store `partSize = actual file size` in metadata, ensuring download correctly calculates `numParts = 1`

**7. Fixed S3 Threshold Comparison**

- Changed S3 multipart threshold from `> 100MB` to `>= 100MB` for consistency with Azure
- Files exactly at 100MB threshold now use multipart upload on both backends

#### Files Modified

**New Files:**
- `internal/crypto/keyderive.go`: HKDF key/IV derivation
- `internal/crypto/streaming.go`: StreamingEncryptor/Decryptor

**Modified Files:**
- `internal/cloud/upload/s3.go`: Streaming upload functions, single-part partSize fix (line ~1099), threshold fix (line ~1207)
- `internal/cloud/upload/azure.go`: Streaming upload functions, single-part partSize fix (line ~826)
- `internal/cloud/download/s3.go`: Streaming download functions
- `internal/cloud/download/azure.go`: Streaming download + format detection
- `internal/cloud/download/download.go`: Format detection routing
- `internal/cli/files.go`: --pre-encrypt flag
- `internal/cli/shortcuts.go`: --pre-encrypt flag
- `cmd/rescale-int/main.go`: Mandatory FIPS enforcement

#### Testing

**Comprehensive E2E Tests (All Passed):**

| Test | S3 | Azure |
|------|-----|-------|
| 10MB upload/download | ✓ | ✓ |
| 50MB upload/download | ✓ | ✓ |
| 100MB upload/download | ✓ | ✓ |
| 101MB upload/download | ✓ | ✓ |
| Folder upload/download (12 files, 4 subdirs) | ✓ | ✓ |
| Pre-encrypt mode (5MB) | ✓ | ✓ |
| Platform-uploaded file download | ✓ | N/A |

**Rescale-Compatible-Identical Verification:**
- ✓ Files uploaded via Rescale platform website can be downloaded with Interlink
- ✓ Files uploaded via Interlink have identical API metadata structure to platform uploads
- ✓ Round-trip (upload→download→verify) produces identical content

**Manual Testing Required:**
- GUI file browser upload/download
- PUR pipeline with streaming encryption
- Jobs tab file download from completed jobs

---

## v2.7.0 - November 26, 2025

### FIPS 140-3 Compliance for FedRAMP Moderate

This release adds FIPS 140-3 compliance using Go 1.24's native FIPS cryptographic module, along with important security fixes.

#### FIPS 140-3 Compliance

**1. Native Go FIPS Module Integration**

All builds now use `GOFIPS140=latest` to enable FIPS 140-3 compliant cryptography:
- AES-256-CBC encryption uses FIPS-validated algorithms
- SHA-512 hashing uses FIPS-validated implementation
- TLS 1.2+ with FIPS-approved cipher suites
- Random number generation uses FIPS-approved DRBG

**Files Modified:**
- `Makefile`: Added `GOFIPS := GOFIPS140=latest` to all build targets
- `cmd/rescale-int/main.go`: Added FIPS verification at startup
- `internal/cli/root.go`: Added FIPS status to version output

**Verification:**
```bash
./rescale-int --version
# Output: rescale-int version v2.7.0 (2025-11-26) [FIPS 140-3]
```

**2. Runtime FIPS Verification**

Added `crypto/fips140.Enabled()` check at startup:
- Logs warning if FIPS mode is not active
- Version output shows `[FIPS 140-3]` or `[FIPS: disabled]`

#### Security Improvements

**3. API Key Display Security Fix**

Fixed potential information leakage in `config show` command:
- **Before**: Displayed partial API key (first 4 + last 4 characters)
- **After**: Displays only `<set (40 chars)>` with no key content
- Addresses CodeQL security alert for clear-text logging

**File Modified:** `internal/cli/config_commands.go:265-271`

**4. API Key Precedence with Warnings**

Added clear warnings when multiple API key sources are detected:
- Priority (highest to lowest):
  1. `--api-key` flag
  2. `RESCALE_API_KEY` environment variable
  3. `--token-file` flag
  4. Default token file (`~/.config/rescale-int/token`)
- Warning shows all detected sources and which one is being used

**File Modified:** `internal/config/csv_config.go:267-360`

**5. Dependency Security Update**

Updated `golang.org/x/crypto` from v0.44.0 to v0.45.0:
- Fixes SSH unbounded memory consumption vulnerability
- Fixes SSH agent malformed message panic (out of bounds read)
- Note: Rescale Interlink does not use SSH, but this is a transitive dependency

#### GUI UX Improvements

**6. Standardized Configuration Directory**

Changed standard configuration directory from `~/.config/rescale/` to `~/.config/rescale-int/`:
- Config file: `~/.config/rescale-int/config.csv`
- Token file: `~/.config/rescale-int/token`
- All documentation updated to reflect new paths

**Files Modified:**
- `internal/config/csv_config.go`: Added `ConfigDir`, `GetConfigDir()`, `EnsureConfigDir()`
- All documentation files

**7. No More 401 Errors on GUI Startup**

GUI now checks if API key is configured before making API calls:
- File Browser shows "API key not configured. Set up your API key in the Setup tab."
- No more error popups when opening GUI without configured credentials

**File Modified:** `internal/gui/remote_browser.go:154-165`

**8. Auto-Load Configuration on GUI Launch**

GUI automatically loads configuration from default location if it exists:
- Checks `~/.config/rescale-int/config.csv` on startup
- Also auto-loads API key from `~/.config/rescale-int/token`
- GUI "just works" after first-time setup

**File Modified:** `internal/gui/gui.go:75-122`

**9. Save to Default Location Button**

Added "Save to Default" button in Setup tab:
- One-click save to `~/.config/rescale-int/config.csv`
- Also saves API key to `~/.config/rescale-int/token` with secure permissions (0600)
- Creates config directory if it doesn't exist
- Shows confirmation that config and API key will auto-load next time

**Files Modified:**
- `internal/gui/setup_tab.go:251, 537-589`
- `internal/config/csv_config.go:475-495` (added `WriteTokenFile` function)

**10. Auto-Apply Configuration on Tab Change**

Configuration is automatically applied when navigating away from Setup tab:
- No need to manually click "Apply Changes" before switching tabs
- Errors are logged but don't interrupt navigation
- User can still manually apply and see success dialog

**Files Modified:**
- `internal/gui/gui.go:196-212`
- `internal/gui/setup_tab.go:569-572`

#### License Update

**11. Standard MIT License**

Updated LICENSE file to standard MIT license text (added "sublicense" permission).

#### Testing

All changes verified with:
- Unit tests passing (`go test ./...`)
- E2E tests with S3-backed Rescale account
- E2E tests with Azure blob-backed Rescale account
- Upload + encrypt + download + decrypt round-trip verified

---

## v2.6.0 - November 26, 2025

### GUI Enhancements: Delete, StatusBar, Filter, Pagination, Transfer Rates

This release adds significant GUI improvements including file deletion, unified status display, search filtering, pagination, and transfer rate display.

#### Code Cleanup

**1. Removed 16 Dead Code Files from cmd/rescale-int/**

Investigation revealed that all files in `cmd/rescale-int/` except `main.go` were dead code - compiled but never called. These duplicate files have been removed:
- template_defaults.go, template_defaults_test.go
- jobs_validation.go
- api_cache.go, api_cache_test.go
- jobs_workflow.go, jobs_workflow_ui.go, jobs_workflow_test.go
- jobs_integration.go
- jobs_tab.go, jobs_tab_test.go
- template_builder.go
- scan_preview.go
- plan_dialog_test.go
- setup_tab.go
- activity_tab.go

All GUI functionality runs from `internal/gui/` - no code duplication.

#### New GUI Features

**2. Delete Functionality in File Browser**

Added delete buttons for both local and remote files:
- Delete button in left pane header for local files/folders
- Delete button in right pane header for remote Rescale files/folders
- Confirmation dialog before deletion
- Status feedback after deletion
- Uses `os.Remove`/`os.RemoveAll` for local, `apiClient.DeleteFile`/`DeleteFolder` for remote

**3. Unified StatusBar Component**

New `StatusBar` widget (`internal/gui/status_bar.go`) provides consistent status display:
- Level-based icons (info, success, warning, error, progress)
- Activity spinner for operations in progress
- Thread-safe updates
- Integrated into File Browser tab

**4. Dialog Helpers**

New dialog helper functions (`internal/gui/dialogs.go`):
- `ShowErrorWithDetails()` - Error with expandable technical details
- `ShowOperationResult()` - Summary after batch operations
- `GetUserFriendlyError()` - Maps technical errors to user-friendly messages
- `ShowUserFriendlyError()` - Automatic friendly error display

**5. Search/Filter in File Browser**

Added filter entry to `FileListWidget`:
- Type to filter files/folders by name
- Case-insensitive matching
- Real-time filtering as you type
- Filter state maintained during selection operations

**6. Pagination for File Lists**

Added pagination support to handle large directories/jobs:
- Default: 40 items per page
- Configurable range: 20-200 items per page
- Compact UI format: `< 1/1 >` with page size entry
- Page resets when navigating folders, filtering, or changing page size

**7. Transfer Rate Display**

Added real-time transfer rate calculation and display:
- In-progress format: `⟳ filename.txt (10.5 MB) 45% @ 2.5 MB/s`
- Completed format: `✓ filename.txt (10.5 MB) @ 3.2 MB/s`
- Rate display waits 0.5s before showing to avoid initial jitter
- New `FormatTransferRate()` helper function

**8. Visual Refinements**

Added visual polish to File Browser:
- Button spacing/padding around navigation and action buttons
- Vertical spacing above/below header buttons (Delete/Upload/Download)
- Filename truncation with ellipsis (prevents text overlapping size column)
- White background for file list panes (instead of grey)

#### Bug Fixes

**9. Fixed "Coretype" Typo**

Changed "Coretype: is required" to "Core Type: is required" in validation messages.

---

## v2.5.0 - November 23, 2025

### GUI Visual Improvements + Cross-Platform Consistency

This release includes significant visual improvements to the GUI, addressing cross-platform consistency issues and enhancing the overall look and feel.

#### GUI Changes

**1. Cross-Platform Consistency (Forced Light Mode)**

The GUI now forces a consistent light theme appearance across all platforms (macOS, Linux, Windows). Previously, the theme would partially adapt to OS dark/light mode preference, causing inconsistent appearance - particularly noticeable on Linux where background colors appeared darker.

**Technical Details:**
- Comprehensive light color palette defined in `internal/gui/theme.go`
- All key colors explicitly specified (backgrounds, foregrounds, inputs, etc.)
- Theme ignores OS dark/light variant preference for consistent cross-platform appearance
- Fallback colors use `theme.VariantLight` instead of OS preference

**2. Improved Spacing and Typography**

The UI now feels less cramped with moderately increased spacing:
- Padding increased from 4px to 6px (~50% increase)
- Inner padding increased from 4px to 6px
- Line spacing increased for better readability
- Separator thickness increased from 1px to 2px for better visibility
- Input border radius increased for a more modern look

Typography hierarchy improved:
- Heading text: 18px → 20px
- Regular text: 13px → 14px
- Sub-heading text: 16px (new)
- Caption text: 12px

**3. Tab Bar Icons**

Main tabs now include icons for better visual identification:
- Setup tab: Settings icon (gear)
- Jobs tab: Document icon
- Activity tab: Info icon

**4. Layout Helper Functions**

New layout helper functions added (`internal/gui/layout_helpers.go`) for consistent spacing throughout the UI:
- `VerticalSpacer(height)` - Fixed-height vertical spacing
- `HorizontalSpacer(width)` - Fixed-width horizontal spacing
- `SectionHeader(text)` - Styled section headers
- `SectionDivider()` - Visual separators with spacing
- `FormSection(title, items...)` - Titled form groups
- `ButtonGroup(buttons...)` - Horizontally arranged buttons with spacing

#### GUI Feature Enhancements (Phase 2)

**5. Header with Logos**

Added header bar with embedded logos:
- Rescale logo on left, Interlink logo on right
- Logos embedded in binary using Go's `embed` package
- New files: `internal/gui/resources.go`, `internal/gui/assets/logo_left.png`, `internal/gui/assets/logo_right.png`

**6. Tab and Button Text Changes**

- "Jobs" tab renamed to "Parallel Upload and Run (PUR) for Multiple Jobs"
- "Load Existing Complete Jobs CSV" → "Load Existing Complete Jobs & Directories CSV"
- "Create New Template" → "Create New Template Jobs & Directories CSV"
- "Core Type" → "Coretype" for consistency

**7. Software/Hardware Scan Buttons**

Template builder now includes API-powered scan functionality:
- "Scan Software" button fetches available analysis codes from Rescale API
- "Scan Hardware" button (enabled after software selection) filters coretypes by software compatibility
- Dropdowns auto-populate with scanned options
- New method `GetAnalyses()` added to engine for API access

**8. Default Value Changes**

- Cores Per Slot: 4 → 1 (minimum viable configuration)
- Walltime Hours: 48.0 → 1.0 (start small, adjust up)
- Window height: 850px → 700px (more compact default)

**9. Back Navigation**

Added step-by-step back navigation in PUR workflow:
- "← Back" button appears after initial selection
- Navigation clears relevant state when going back
- Cannot go back from Initial, Executing, Completed, or Error states

**10. File Browser Tab**

New placeholder tab added between PUR and Activity tabs:
- Prepares for future file browser functionality
- Shows "coming soon" message

**11. Tab Icons Updated**

- Jobs/PUR tab: Changed from DocumentIcon to ComputerIcon
- File Browser tab: Uses DocumentIcon

---

### CLI Usability Improvements + Conflict Handling

This release focuses on improving CLI usability by adding short flags to all commands, changing the `hardware list` default behavior to show only active hardware types, and implementing comprehensive conflict handling for file/folder uploads and downloads.

#### New Features

**1. Short Flags for All CLI Commands**

All CLI commands now support single-letter short flags, aligned with `rescale-cli` conventions where applicable. This enables faster command typing and a more familiar experience for users of other CLI tools.

**Short Flag Mappings:**

| Short | Long | Commands | Origin |
|-------|------|----------|--------|
| `-j` | `--job-id` / `--id` | jobs get/stop/tail/download/delete | rescale-cli |
| `-s` | `--search` | hardware/software/files list, jobs download | rescale-cli |
| `-o` | `--output` / `--outdir` | files/jobs download | rescale-cli |
| `-d` | `--folder-id` / `--outdir` | files upload, jobs download | rescale-cli |
| `-f` | `--force` / `--job-file` | config init, jobs submit | rescale-cli |
| `-E` | `--end-to-end` | jobs submit | rescale-cli |
| `-n` | `--limit` | jobs/files list | new |
| `-m` | `--max-concurrent` | files upload/download, jobs submit/download | new |
| `-y` | `--confirm` | jobs stop/delete, files delete | new |
| `-w` | `--overwrite` | files/jobs download | new |
| `-r` | `--resume` | files/jobs download | new |
| `-S` | `--skip` | files/jobs download | new |
| `-x` | `--exclude` | files list, jobs download | new |
| `-i` | `--interval` / `--fileid` | jobs tail, files delete | new |
| `-J` | `--json` | hardware/software list | new |
| `-V` | `--versions` | software list | new |
| `-a` | `--all` | hardware list | new |

**Usage Examples:**
```bash
# Before (verbose)
rescale-int hardware list --search emerald --json
rescale-int jobs download --id WfbQa --outdir ./results --overwrite
rescale-int files upload model.tar.gz --folder-id abc123

# After (concise)
rescale-int hardware list -s emerald -J
rescale-int jobs download -j WfbQa -d ./results -w
rescale-int files upload model.tar.gz -d abc123
```

**2. Hardware List Default Behavior Change**

The `hardware list` command now shows only **active** hardware types by default, which is what most users want. Use `-a/--all` to include inactive/deprecated types.

**Before:**
```bash
rescale-int hardware list          # Showed all hardware (active + inactive)
rescale-int hardware list --active # Showed only active hardware
```

**After:**
```bash
rescale-int hardware list          # Shows only active hardware (default)
rescale-int hardware list --all    # Shows all hardware (active + inactive)
rescale-int hardware list -a       # Short flag for --all
```

**Technical Change:** The `GetCoreTypes()` API function parameter was changed from `activeOnly bool` to `includeInactive bool` with inverted logic. The API call now uses `?isActive=true` by default.

**3. Comprehensive Conflict Handling**

Added complete conflict handling for file/folder uploads and downloads with interactive prompts and CLI flags.

**File Upload Duplicate Detection:**
- `--check-duplicates` - Check for existing files before uploading (prompts for each duplicate)
- `--no-check-duplicates` - Skip duplicate checking (fast mode, may create duplicates)
- `--skip-duplicates` - Check and automatically skip files that already exist
- `--allow-duplicates` - Check but upload anyway (explicitly allows duplicates)
- `--dry-run` - Preview what would be uploaded without actually uploading

**Folder Upload Conflict Handling:**
- `-S, --skip-folder-conflicts` - Skip folders that already exist on Rescale
- `-m, --merge-folder-conflicts` - Merge into existing folders (skip existing files)

**Folder Download Conflict Handling:**
- `-S, --skip` - Skip existing files/folders without prompting
- `-w, --overwrite` - Overwrite existing files without prompting
- `-m, --merge` - Merge into existing folders, skip existing files
- `--dry-run` - Preview what would be downloaded without actually downloading

**Interactive Mode:**
- When no conflict flags are provided in interactive mode (TTY), prompts user for handling mode
- Non-interactive mode (pipes/scripts): uploads default to no-check with warning; downloads require explicit flag

**Usage Examples:**
```bash
# File upload with duplicate skip
rescale-int files upload *.dat --skip-duplicates

# Folder upload with merge
rescale-int folders upload-dir ./project --merge-folder-conflicts

# Folder download with merge (skip existing files)
rescale-int folders download-dir abc123 --merge -o ./data

# Dry-run to preview
rescale-int folders download-dir abc123 --dry-run --merge -o ./data
```

**4. Byte-Offset Download Resume (COMPLETED)**

The `--resume` flag now supports full byte-offset resume for interrupted downloads. Previously, `--resume` would restart downloads from the beginning. Now it continues from the exact byte position using HTTP Range requests.

**How It Works**:
1. Download tracks progress via `.download.resume` JSON sidecar file
2. On interruption (Ctrl+C, network error), partial encrypted file and state are preserved
3. On re-run with `--resume`, CLI detects valid state and shows: `↻ Resuming download for X from Y%`
4. Downloader uses HTTP Range requests to continue from byte offset
5. Once encrypted file is complete, decryption runs automatically

**Example**:
```bash
# Start download - interrupt with Ctrl+C at 40%
rescale-int files download -r abc123 -o ./data

# Resume - continues from 40%, not from 0%
rescale-int files download abc123 -r -o ./data
# Output: ↻ Resuming download for file.bin from 40.0% (2097152/5242896 bytes)...
```

**Note**: Decryption must start from the beginning due to AES-CBC mode constraints. This is automatic and transparent to the user.

#### Files Modified

**CLI Commands (8 files):**
- `internal/cli/hardware.go` - Changed `--active` to `--all`, added `-s`, `-J`, `-a`
- `internal/cli/software.go` - Added `-s`, `-J`, `-V`
- `internal/cli/jobs.go` - Added `-j`, `-n`, `-y`, `-f`, `-E`, `-m`, `-i`, `-d`, `-o`, `-w`, `-S`, `-r`, `-s`, `-x`
- `internal/cli/files.go` - Added `-d`, `-m`, `-o`, `-w`, `-S`, `-r`, `-n`, `-s`, `-x`, `-i`, `-y`, duplicate detection flags
- `internal/cli/config_commands.go` - Added `-f`
- `internal/cli/shortcuts.go` - Added `-d`, `-m`, `-o`
- `internal/cli/download_helper.go` - Implemented byte-offset resume (preserve partial files, check resume state)

**Conflict Handling (5 files):**
- `internal/cli/prompt.go` - Added upload/download conflict prompts and mode selection
- `internal/cli/upload_helper.go` - Added duplicate detection logic with API check
- `internal/cli/folder_download_helper.go` - Added merge mode, dry-run, and conflict prompts
- `internal/cli/folders.go` - Added `--skip-folder-conflicts`, `--merge-folder-conflicts`, `--merge`, `--dry-run`
- `internal/cli/files.go` - Added `--check-duplicates`, `--skip-duplicates`, `--allow-duplicates`, `--dry-run`

**API and Core (6 files):**
- `internal/api/client.go` - Changed `GetCoreTypes(ctx, activeOnly)` to `GetCoreTypes(ctx, includeInactive)`; Added `readResponseBody()` helper to properly handle io.ReadAll errors in error messages
- `cmd/rescale-int/api_cache.go` - Updated interface and call
- `internal/gui/api_cache.go` - Updated interface and call
- `internal/pur/validation/validation.go` - Updated call
- `internal/core/engine.go` - Updated call

**Documentation (3 files):**
- `CLI_GUIDE.md` - Updated with short flags for all commands
- `FEATURE_SUMMARY.md` - Clarified download resume status
- `internal docs` - Removed machine-specific paths, made token file references portable

**Bug Fixes (11 files):**
- `internal/pur/state/state.go` - Fixed race condition in UpdateState()
- `internal/pur/pipeline/pipeline.go` - Fixed ignored error in TAR worker
- `cmd/rescale-int/jobs_workflow_ui.go` - Removed debug logging
- `internal/validation/paths.go` - Fixed overly strict filename validation that rejected valid filenames like "foo..bar.txt"
- `internal/validation/paths_test.go` - Updated test to expect valid filenames containing ".."
- `internal/progress/progress.go` - Added mutex protection to GUIProgress and ProgressReader for race detector compliance
- `internal/core/engine.go` - Fixed race condition in StartJobMonitoring by capturing channels before goroutine
- `internal/cloud/upload/s3_concurrent.go` - Added context cancellation check + fixed error channel consumption bug
- `internal/cloud/upload/azure_concurrent.go` - Same fixes as above for Azure backend
- `internal/cloud/download/s3_concurrent.go` - Fixed error channel consumption bug for concurrent downloads
- `internal/cloud/download/azure_concurrent.go` - Same fix as above for Azure backend

**Security Improvements (2 files):**
- `internal/config/csv_config.go` - Token file permission validation warns if permissions are too open (not 0600)
- `internal/util/buffers/pool.go` - Buffer pool now clears sensitive data before returning buffers to pool

#### Backwards Compatibility

- **Short flags are additive**: All existing long flags continue to work unchanged
- **Hardware list behavior change**: Scripts relying on `hardware list` showing inactive types should add `--all`
- **No API changes**: All REST API calls remain compatible

#### Bug Fixes

**1. Race Condition in State Manager**

Fixed a race condition in `UpdateState()` where the mutex was released before calling `Save()`, creating a window where another goroutine could modify state between unlock and save.

- **Before**: `UpdateState()` unlocked mutex, then called `Save()` which acquired RLock separately
- **After**: Created internal `saveUnlocked()` method; `UpdateState()` now calls it while holding the lock
- **Impact**: Prevents potential state inconsistency under high concurrency during pipeline execution
- **File**: `internal/pur/state/state.go`

**2. Ignored Error in TAR Worker**

Fixed ignored error return from `ValidateTarExists()` in the pipeline TAR worker. The error was silently discarded with `exists, _ := tar.ValidateTarExists(...)`.

- **Before**: File system errors (permissions, etc.) were silently ignored
- **After**: Errors are logged as warnings and the tar is recreated as a safe fallback
- **File**: `internal/pur/pipeline/pipeline.go:294`

**3. Debug Logging Cleanup**

Removed stray `fmt.Println("DEBUG: ...")` statement that was outputting to stdout during scan operations.

- **File**: `cmd/rescale-int/jobs_workflow_ui.go:559`

**4. Goroutine Leak Prevention in Concurrent Uploads**

Fixed potential goroutine leak in concurrent upload implementations where the file reader goroutine didn't check for context cancellation.

- **Before**: File reader goroutine only checked `errorChan`, could miss `ctx.Done()` signals
- **After**: Added `ctx.Done()` check alongside `errorChan` in the select statement
- **Impact**: Prevents goroutine leaks when uploads are cancelled via Ctrl+C
- **Files**: `internal/cloud/upload/s3_concurrent.go`, `internal/cloud/upload/azure_concurrent.go`

**5. Race Condition in ProgressReader**

Fixed race condition in `ProgressReader` where the `current` field was accessed without synchronization.

- **Before**: `current` field was incremented without mutex protection
- **After**: Added `sync.Mutex` to protect concurrent access to `current`
- **File**: `internal/progress/progress.go`

**6. Error Channel Consumption Bug in Concurrent Transfers**

Fixed critical bug where errors could be silently lost during concurrent upload/download operations. Workers and the job producer were consuming errors from the error channel when checking for errors, causing the main function to think operations succeeded when they actually failed.

- **Before**: Workers checked for errors with `case <-errorChan:` which consumed the error; if any code path consumed the error before the final check, the function would return success
- **After**: Changed to context-based cancellation signaling with `sync.Once`-protected error capture; workers check `opCtx.Done()` instead of reading from errorChan
- **Impact**: Prevents silent failure of concurrent uploads/downloads where errors could be lost, ensuring proper error propagation to the caller
- **Files**: `internal/cloud/upload/s3_concurrent.go`, `internal/cloud/upload/azure_concurrent.go`, `internal/cloud/download/s3_concurrent.go`, `internal/cloud/download/azure_concurrent.go`

#### Security Improvements

**1. Token File Permission Validation**

Added security warning when token files have overly permissive permissions (readable by group or others).

- **Behavior**: Warns on stderr if token file permissions are not 0600 or stricter
- **Message**: `Warning: Token file <path> has insecure permissions <mode>. Consider using 'chmod 600 <path>'`
- **File**: `internal/config/csv_config.go`

**2. Buffer Pool Security Hardening**

Buffer pools now clear sensitive data before returning buffers to the pool, preventing potential data leakage between operations.

- **Before**: Buffers were returned to pool without clearing (comment noted this was skipped for performance)
- **After**: Uses Go's `clear()` builtin to zero buffer contents before pooling
- **Impact**: Prevents sensitive data (encryption keys, file contents) from persisting in memory
- **File**: `internal/util/buffers/pool.go`

#### Documentation Fixes

**1. Constants File Path Correction**

Fixed incorrect file path references in documentation that pointed to non-existent `internal/pur/constants/constants.go` instead of actual location `internal/constants/app.go`.

- **Files**: `RELEASE_NOTES.md`, `FEATURE_SUMMARY.md`

**2. Documentation Summary Update**

Updated `DOCUMENTATION_SUMMARY.md` to include internal docs and removed reference to non-existent `bin/README.md`.

**3. Download Resume Status Clarification**

Updated `FEATURE_SUMMARY.md` to clarify that download resume is incomplete - state tracking works but actual byte-offset resume is not yet implemented. Added reference to `TODO_AND_PROJECT_STATUS.md` for details.

**4. CLI_GUIDE.md Command Example Fixes**

Fixed incorrect command examples in the Quick Reference Examples section:
- Fixed `folders upload-dir` example: Changed from non-existent `--folder-id`, `--dir`, `--recursive` flags to correct positional directory argument with `--parent-id` flag
- Fixed `jobs tail` example: Removed non-existent `--follow` flag

**5. Cross-Document Reference Fixes**

- `LESSONS_LEARNED.md`: Updated reference to `DOWNLOAD_PARITY_FINAL_SUMMARY.md` to include correct `old-reference/` path
- `README.md`: Updated architecture diagram version from v2.0 to v2.5.0
- `README.md`: Fixed malformed empty parentheses in Known Limitations section
- `TODO_AND_PROJECT_STATUS.md`: Updated conclusion section from outdated v2.0.5 references to v2.5.0
- `TODO_AND_PROJECT_STATUS.md`: Removed stale TODO item referencing deleted `cliprogress.go` file
- `TESTING.md`: Added version context to historical testing sections for clarity

**6. go.mod Cleanup**

Removed unused `replace` directive for `github.com/rescale-labs/pur` module which was not imported anywhere in the codebase.

#### Testing

Verified with:
- ✅ All unit tests passing (`go test ./cmd/... ./internal/...`)
- ✅ E2E tests with S3 backend (files list/upload/download/delete, jobs list, hardware list, folders list)
- ✅ E2E tests with Azure backend (files list/upload/download/delete)
- ✅ Short flags work correctly in all tested scenarios
- ✅ Help output shows short flags properly
- ✅ File upload/download roundtrip verified with content integrity check
- ✅ go.mod change verified with successful build

---

## v2.4.9 - November 22, 2025

### Security Improvements + Bug Fixes

This release focuses on security hardening and bug fixes. Credentials are no longer stored in config files, and several resource leaks have been fixed.

#### Security Enhancements

**1. Credential Persistence Removal**

API keys and proxy passwords are no longer stored in configuration files. This reduces the risk of credential exposure through:
- Accidental commits to version control
- Overly permissive file permissions
- Filesystem compromise

**Changes:**
- `LoadConfigCSV()` silently ignores `api_key` and `proxy_password` fields (backward compatible)
- `SaveConfigCSV()` never writes credentials to files
- Existing config files with credentials continue to work (ignored for security)

**Migration:** Use one of these methods for API keys:
```bash
# Option 1: Environment variable
export RESCALE_API_KEY="your-api-key"

# Option 2: Token file (recommended for scripts)
echo "your-api-key" > ~/.config/rescale-int/token
chmod 600 ~/.config/rescale-int/token
rescale-int --token-file ~/.config/rescale-int/token <command>

# Option 3: Command-line flag (not recommended)
rescale-int --api-key "your-api-key" <command>
```

**2. Token File Support**

New `--token-file` flag allows reading API key from a dedicated file:
- Keeps credentials separate from configuration
- Supports restricted file permissions (600)
- Ideal for CI/CD and automation scripts

**Priority order:** `--api-key` > `--token-file` > `RESCALE_API_KEY` env var > defaults

**3. Secure Proxy Password Prompting**

Proxy passwords are now prompted at runtime instead of being stored:
- Password input is not echoed to terminal
- Password stored in memory only during session
- Works for both `basic` and `ntlm` proxy modes

#### Bug Fixes

**1. Pipeline Resource Leak (CRITICAL)**

Fixed `defer` statement inside for-loop in `uploadWorker()`:
- **Problem**: `defer transferHandle.Complete()` inside loop accumulated defers, never releasing resources until function exit
- **Impact**: Thread pool exhaustion after processing many files, severe performance degradation
- **Fix**: Explicit `Complete()` calls after each file operation

**2. S3 Context Leak (MODERATE)**

Fixed context leak in `uploadMultipart()`:
- **Problem**: `defer cancel()` inside for-loop accumulated cancel functions
- **Impact**: Memory increase during large file uploads (e.g., 1,600 contexts for 100GB file)
- **Fix**: Explicit `cancel()` call after each part upload

**3. PKCS7 Padding Verification (LOW)**

Enhanced `pkcs7Unpad()` to verify all padding bytes:
- **Problem**: Only checked padding length, not all byte values
- **Fix**: Now verifies every padding byte has the correct value
- **Also fixed**: Added check for `padding == 0` (invalid in PKCS7)

#### Files Modified

| File | Changes |
|------|---------|
| `internal/pur/pipeline/pipeline.go` | Fixed defer-in-loop resource leak |
| `internal/cloud/upload/s3.go` | Fixed defer-in-loop context leak |
| `internal/crypto/encryption.go` | Added PKCS7 padding byte verification |
| `internal/config/csv_config.go` | Removed credential persistence, added token file support |
| `internal/cli/root.go` | Added `--token-file` global flag |
| `internal/cli/pur.go` | Added proxy password prompting |
| `internal/cli/prompt.go` | Added secure password prompting functions |
| `internal/http/proxy.go` | Added NeedsProxyPassword helper |
| `internal/gui/setup_tab.go` | Added security notice on config save |
| `internal/config/csv_config_test.go` | Updated tests for new behavior |
| `internal/cli/config_commands_test.go` | Updated tests for new behavior |
| `internal/crypto/encryption_test.go` | Added padding verification tests |

#### Backwards Compatibility

- **Old config files**: Silently ignored for credentials (no errors)
- **Existing `--api-key` flag**: Still works, highest priority
- **Existing `RESCALE_API_KEY` env var**: Still works
- **No changes** to API calls, upload/download functionality, or data formats

#### Testing

Verified with:
- ✅ All unit tests passing
- ✅ E2E tests with S3 backend (token file authentication)
- ✅ E2E tests with Azure backend (token file authentication)
- ✅ Environment variable authentication
- ✅ File upload/download roundtrip with encryption

---

## v2.4.8 - November 20, 2025

### Massive Download Performance Improvement

This release achieves a **99% reduction in API overhead** for job downloads by eliminating unnecessary GetFileInfo API calls. Downloads are now limited by S3/Azure transfer speed, not API rate limits.

#### Performance Breakthrough

**The Problem (v2.4.7):**
- Downloading 289 files from a job required 289 GetFileInfo API calls
- At 1.6 req/sec rate limit: ~180 seconds wasted on API calls
- Total API overhead: ~188 seconds per job

**The Solution (v2.4.8):**
- Use metadata already returned by v2 ListJobFiles endpoint
- Zero GetFileInfo calls needed
- Total API overhead: <1 second per job
- **Improvement: ~3 minutes saved per 289-file job**

#### Technical Changes

**Enhanced JobFile Model** (`internal/models/job.go`):
- Added `Path`, `PathParts`, `Storage`, `FileChecksums` fields to capture complete metadata from v2 endpoint
- Created `ToCloudFile()` conversion method for clean abstraction
- Source: `internal/models/job.go`

**New Download Function** (`internal/cloud/download/download.go`):
- Added `DownloadFileWithMetadata()` that accepts CloudFile directly (no API call)
- Refactored existing functions to use new helper
- Source: `internal/cloud/download/download.go`

**Updated Job Download Flow** (`internal/cli/download_helper.go`):
- Modified to use `ToCloudFile()` conversion instead of GetFileInfo API call
- Updated documentation with v2.4.8 performance characteristics
- Source: `internal/cli/download_helper.go`

#### Files Modified

- `internal/models/job.go` - Enhanced JobFile model
- `internal/cloud/download/download.go` - New metadata-based download function
- `internal/cli/download_helper.go` - Updated job download orchestration
- `cmd/rescale-int/main.go` - Version 2.4.8
- `internal/cli/root.go` - Version 2.4.8

#### Performance Metrics

| Version | API Overhead (289 files) | Improvement |
|---------|-------------------------|-------------|
| v2.4.6  | ~188 seconds            | baseline    |
| v2.4.7  | ~181 seconds            | 4%          |
| v2.4.8  | <1 second               | **99%**     |

#### Testing

Verified with real job downloads:
- ✅ Build successful
- ✅ Version check: 2.4.8
- ✅ Integration test: Downloaded 5 files from job wemvxd
- ✅ No rate limit waits
- ✅ Checksum validation passed
- ✅ All unit tests passing

---

## v2.4.7 - November 20, 2025

### v2 API Support for Job Operations

This release adds support for Rescale's v2 API endpoints for job operations, achieving a **12.5x faster rate limit** for job file listings.

#### Key Changes

**Faster Job File Listing:**
- Switched `ListJobFiles` from v3 to v2 endpoint
- v2 uses `jobs-usage` scope: 90000/hour = 25 req/sec (hard limit)
- Target rate: 20 req/sec (80% of limit for safety)
- **12.5x faster** than v3 user scope (1.6 req/sec)

**Smart API Routing** (`internal/api/client.go`):
- Added logic to select appropriate rate limiter based on endpoint:
  - v3 endpoints → user scope (1.6 req/sec)
  - v2 job submission → job-submission scope (0.139 req/sec)
  - v2 job query → jobs-usage scope (20 req/sec)
- Source: `internal/api/client.go`

**New Rate Limiter** (`internal/ratelimit/`):
- Added jobs-usage scope constants
- Created `NewJobsUsageRateLimiter()` with 300-token burst capacity
- Burst allows ~15 seconds of rapid operations at startup
- Source: `internal/ratelimit/constants.go`, `internal/ratelimit/limiter.go`

#### Technical Details

**Rate Limiting Configuration:**
```go
JobsUsageLimitPerHour = 90000       // 25 req/sec hard limit
JobsUsageTargetPercent = 80         // Use 80% for safety
JobsUsageRatePerSec = 20.0          // Target rate
JobsUsageBurstCapacity = 300        // ~15 seconds burst
```

**API Client Changes:**
```go
// Select rate limiter based on endpoint type
limiter := c.userScopeLimiter  // default

if strings.Contains(path, "/api/v2/jobs/") {
    if strings.Contains(path, "/submit/") {
        limiter = c.jobSubmitLimiter     // 0.139 req/sec
    } else {
        limiter = c.jobsUsageLimiter     // 20 req/sec
    }
}
```

#### Files Modified

- `internal/ratelimit/constants.go` - Added jobs-usage constants
- `internal/ratelimit/limiter.go` - Added NewJobsUsageRateLimiter()
- `internal/api/client.go` - Smart routing, v2 ListJobFiles endpoint
- `cmd/rescale-int/main.go` - Version 2.4.7
- `internal/cli/root.go` - Version 2.4.7

#### Performance Impact

- Job file listing: <1 second (was ~8 seconds in v2.4.6)
- Still made 289 GetFileInfo calls (fixed in v2.4.8)
- API overhead reduced from ~188s to ~181s for 289-file job

---

## v2.4.6 - November 20, 2025

### Rate Limiting and Upload Improvements

This release corrects rate limiting configuration for better safety margins and adds dual-mode upload with conflict detection.

#### Key Changes

**Rate Limiting Corrections:**
- **User scope**: Changed to 80% of 2 req/sec = **1.6 req/sec** (was using 100%)
- **Job submission**: Changed to 50% of 0.278 req/sec = **0.139 req/sec** (was using 100%)
- 20% safety margin prevents throttle lockouts during burst operations
- More conservative approach based on real-world testing
- Source: `internal/ratelimit/constants.go`

**Dual-Mode Upload:**
- **Fast Mode (default)**: Upload first, handle conflicts on error (1 API call/file)
- **Safe Mode** (`--check-conflicts`): Check existence before upload (1-2 API calls/file)
- Gives users choice between speed and preemptive conflict detection
- Source: `internal/cli/files.go`

**Upload Concurrency Configuration:**
- Fixed `--max-concurrent` flag for file uploads
- Correctly configures worker pool size (1-10 workers)
- Default: 5 concurrent uploads
- Source: `internal/cli/files.go`

#### Technical Details

**Rate Limiter Constants:**
```go
// User scope (was 2.0 req/sec, now 1.6)
UserScopeTargetPercent = 80
UserScopeRatePerSec = 1.6

// Job submission (was 0.278 req/sec, now 0.139)
JobSubmitTargetPercent = 50
JobSubmitRatePerSec = 0.139
```

**Upload Modes:**
```bash
# Fast mode (default) - 1 API call per file
rescale-int files upload *.dat

# Safe mode - check before upload
rescale-int files upload *.dat --check-conflicts
```

#### Files Modified

- `internal/ratelimit/constants.go` - Corrected rate limit percentages
- `internal/cli/files.go` - Added conflict detection modes
- `cmd/rescale-int/main.go` - Version 2.4.6
- `internal/cli/root.go` - Version 2.4.6

#### Rationale

The 20% safety margin in rate limiting prevents edge cases where:
1. Multiple processes might be using the same API key
2. Burst operations could temporarily exceed limits
3. Network timing variations could cause rate limit violations

---

## v2.4.5 - November 19, 2025

### Cross-Storage Download & Signal Handling Fixes

This release fixes two bugs: a critical issue preventing Azure users from downloading job outputs, and a spurious cancellation message appearing after successful operations.

#### Bug Fixes

**1. Fixed job output downloads for cross-storage scenarios** (🔧 Critical):
- Azure users can now download job outputs stored in platform S3 storage
- S3 users can download files stored in Azure (if applicable)
- API client now requests file-specific storage credentials instead of assuming all files use the user's default storage type
- Credentials are correctly refreshed during long downloads for the appropriate storage backend

**Root Cause**: Job output files are typically stored in Rescale's platform storage (S3), regardless of the user's account storage type. Previous versions always requested credentials for the user's default storage, causing Azure users to receive Azure credentials and attempt to download from Azure blob storage, where the files don't exist (404 errors).

**The Fix**:
1. Modified credentials API requests to include file-specific storage metadata from `CloudFile.Storage`
2. API returns credentials for the file's actual storage type (e.g., S3 credentials for job outputs, even on Azure accounts)
3. Updated AWS credential provider to use file-specific credentials during auto-refresh
4. Fixed container/bucket name resolution from `pathParts.container` field

#### Files Modified

**Cross-storage download fix:**
- `internal/models/file.go` - Added CredentialsRequest models with camelCase JSON tags
- `internal/api/client.go` - Modified GetStorageCredentials to accept optional fileInfo
- `internal/cloud/download/download.go` - Added getStorageInfo() helper, updated download functions
- `internal/cloud/credentials/aws_provider.go` - Added fileInfo parameter to credential provider
- `internal/cloud/download/s3.go` - Pass fileInfo to credential provider, removed manual refresh
- `internal/cloud/upload/s3.go` - Updated to pass nil for default storage credentials

**Signal handling fix:**
- `internal/cli/root.go` - Added nil check in signal handler

**Tab-completion documentation:**
- `internal/cli/root.go` - Enhanced completion command with detailed help text
- `README.md` - Added "Optional: Enable Tab Completion" section to Quick Start
- `internal/cli/shortcuts.go` - Removed "run" shortcut (use `pur run` instead)
- `internal/cli/shortcuts_test.go` - Updated test expectations

**2. Fixed spurious cancellation message** (🐛 Minor):
- Removed "Received signal <nil>, cancelling operations..." message appearing after successful downloads
- This message was printed when the program exited normally due to channel cleanup
- Signal handler now checks for nil signals before printing cancellation message

#### Improvements

**Enhanced tab-completion documentation** (✨ UX):
- Completely rewrote `completion` command help text with clear explanations
- Added step-by-step setup instructions for bash, zsh, fish, and PowerShell
- Included "Quick Start" examples for macOS and Linux
- Added tab-completion setup section to README with collapsible instructions
- Makes it much easier for users to enable this productivity feature

#### Testing

Verified with Azure account (API key ending in ...4555) downloading job WVieAd:
- ✅ Single file download successful (file ywiybh)
- ✅ Batch download successful (10 files with nested directories)
- ✅ All files receive S3 credentials correctly
- ✅ No 404 errors or credential mismatches
- ✅ No spurious cancellation messages

---

## v2.4.3 - November 18, 2025

### Security & Quality Improvements Release

This release significantly improves security, reliability, and user experience through comprehensive testing, input validation, and quality enhancements. All improvements maintain full backward compatibility.

#### Security Enhancements

**Path Traversal Protection**:
- Added comprehensive input validation for all file download operations
- Validates API-provided filenames to prevent directory escape attacks
- Three-layer validation strategy: strict filename validation, path sanitization, and directory containment checks
- Protects against malicious filenames like `../../etc/passwd` or files with path separators
- New validation module: `internal/validation/paths.go` with 54 comprehensive tests

**Strict Checksum Verification** (⚠️ BREAKING CHANGE):
- Checksum verification now fails by default (was warning-only in v2.4.2)
- Prevents silent data corruption from corrupted downloads
- New `--skip-checksum` flag available if override needed (not recommended)
- Clear error messages guide users to the flag if necessary
- Applies to: `files download`, `folders download-dir`, `jobs download`

#### New Features

**Graceful Cancellation Support**:
- Ctrl+C now properly cancels long-running operations
- Context cancellation propagates through all concurrent workers
- Clean shutdown with resume state preservation
- User-friendly cancellation messages with cleanup status
- Affected operations: uploads, downloads, concurrent transfers

**Enhanced Command Flags**:
- Added `--skip-checksum` flag to all download commands for flexibility in edge cases

#### Test Coverage Improvements

**Comprehensive Test Suites Added** (1,745 lines of new test code):
- Encryption module: 12 tests covering key generation, IV generation, PKCS7 padding, round-trip encryption
- Upload module: 6 tests for resume state management and atomic saves
- Download module: 8 tests including critical PKCS7 padding range check (v2.3.0 bug verification)
- Validation module: 54 tests covering path traversal attacks and edge cases

**Coverage Statistics**:
- Encryption: 0% → ~90% coverage ✅
- Upload/Download resume: 0% → 100% coverage ✅
- Validation: New module with ~95% coverage ✅

#### Code Quality Improvements

**Logging Standardization**:
- Unified all logging to zerolog for consistent structured output
- Converted ~54 log statements in GUI code from raw `fmt.Printf`/`log.Printf` to zerolog
- Professional log levels (DEBUG/INFO/WARN/ERROR) with timestamps and context
- Debug logging controlled via `RESCALE_DEBUG` environment variable

**Error Handling Fixes**:
- Fixed `log.Fatal()` calls in library code (proper error propagation)
- Fixed failing CLI tests (shortcuts checking non-existent flags)
- Better error messages with actionable guidance

#### User Experience Improvements

**Before → After Examples**:

**Ctrl+C Cancellation**:
```bash
# Before: Ctrl+C does nothing, user must kill terminal
# After:
^C
🛑 Received signal interrupt, cancelling operations...
   Please wait for cleanup to complete.
✓ Upload cancelled, resume state saved
```

**Checksum Verification**:
```bash
# Before: Warning only, download succeeds despite corruption
Warning: Checksum verification failed for file.dat: hash mismatch
✓ Downloaded file.dat

# After: Strict by default, prevents corruption
Error: checksum verification failed for file.dat: expected abc123, got def456

To download despite checksum mismatch, use --skip-checksum flag (not recommended)
```

**Path Security**:
```bash
# Before: Silent acceptance of malicious paths
# After: Immediate rejection
Error: invalid filename from API for file ABC123: filename cannot contain '..': ../../etc/passwd
```

#### Files Modified

**New Files Created** (5 files, 1,745 lines):
- `internal/crypto/encryption_test.go` (424 lines, 12 tests)
- `internal/cloud/upload/upload_test.go` (201 lines, 6 tests)
- `internal/cloud/download/download_test.go` (344 lines, 8 tests)
- `internal/validation/paths.go` (152 lines, validation functions)
- `internal/validation/paths_test.go` (624 lines, 54 tests)

**Files Modified** (25+ files):
- Core modules: `internal/cloud/download/download.go` (checksum strictness)
- CLI commands: `internal/cli/{files,folders,jobs}.go` (added `--skip-checksum` flag)
- Download helpers: `internal/cli/{download_helper,folder_download_helper}.go` (validation integration)
- Context propagation: 8 CLI command files (replaced `context.Background()` with `GetContext()`)
- Concurrent workers: 4 concurrent upload/download modules (added cancellation support)
- Signal handling: `internal/cli/root.go` (global context with signal handler)
- GUI logging: 5 GUI files (standardized to zerolog)
- Tests: `internal/cli/shortcuts_test.go` (fixed failing flag checks)
- Error handling: `internal/gui/gui.go` (removed `log.Fatal()` calls)

#### Breaking Changes

⚠️ **Checksum Verification Behavior**:
- **Before (v2.4.2)**: Checksum mismatches produced warnings but downloads succeeded
- **After (v2.4.3)**: Checksum mismatches fail downloads by default
- **Workaround**: Use `--skip-checksum` flag to restore old behavior
- **Rationale**: Prevents silent data corruption, ensures data integrity
- **Impact**: Users downloading files with checksum mismatches must explicitly opt-in to skip verification

#### Upgrade Notes

- **No other breaking changes**: All modifications are backward compatible except checksum behavior
- **Default behavior**: More secure (strict checksums, path validation)
- **New features**: Opt-in (Ctrl+C cancellation works automatically, `--skip-checksum` available if needed)
- **Performance**: No performance impact, all optimizations maintained

#### Testing

**Verification Status**:
- ✅ All 20+ test suites passing
- ✅ Build succeeds on all platforms
- ✅ Zero regressions detected
- ✅ 40 new tests added (1,593 lines of test code)
- ✅ Tested with S3 backend (API key: 91cb2a...)
- ✅ Tested with Azure backend (API key: 8f6cb2...)

**Version Information**:
```bash
$ ./rescale-int --version
rescale-int version 2.4.3
Build date: 2025-11-18
```

---

## v2.4.2 - November 18, 2025

### Proxy Support for S3/Azure Storage

This release adds full proxy support for direct S3 and Azure Blob Storage operations, achieving feature parity with the Python PUR implementation. All file transfers (uploads and downloads) now respect proxy configuration.

#### What's New

**Proxy Integration for Storage Operations**:
- S3 uploads and downloads now go through configured proxy
- Azure Blob uploads and downloads now go through configured proxy
- Matches Python PUR behavior where ALL traffic (API + storage) uses proxy
- Critical for enterprise environments with strict network policies

**Implementation Details**:
- Modified `internal/pur/httpclient/client.go` to use `proxy.ConfigureHTTPClient()` as base
- Added `GetConfig()` method to API client for config access
- Updated all 4 storage modules: S3/Azure upload/download
- Maintains all performance optimizations (connection pooling, HTTP/2, etc.)

**Proxy Modes Supported** (for storage operations):
- **no-proxy**: Direct connection (default)
- **system**: Use system environment proxy settings
- **basic**: Basic authentication (username/password)
- **ntlm**: NTLM authentication (corporate proxies)

**Benefits**:
- **Network Policy Compliance**: All traffic routes through corporate proxy
- **Security Monitoring**: Security teams can monitor/audit all file transfers
- **Firewall Compatibility**: Works in environments blocking direct S3/Azure access
- **Enterprise Ready**: Matches enterprise network security requirements

#### Files Modified

> **Note (v4.5.9):** The paths below reflect the codebase at the time of v3.4.0. These modules
> were later refactored into the unified provider architecture under `internal/cloud/transfer/`
> and `internal/cloud/providers/`. See v4.0.0 release notes for the unified architecture details.

- `internal/api/client.go` - Added GetConfig() method
- `internal/pur/httpclient/client.go` - Proxy-aware HTTP client creation (now `internal/http/proxy.go`)
- `internal/cloud/upload/s3.go` - Proxy support for S3 uploads (now `internal/cloud/providers/s3/`)
- `internal/cloud/upload/azure.go` - Proxy support for Azure uploads (now `internal/cloud/providers/azure/`)
- `internal/cloud/download/s3.go` - Proxy support for S3 downloads (now `internal/cloud/providers/s3/`)
- `internal/cloud/download/azure.go` - Proxy support for Azure downloads (now `internal/cloud/providers/azure/`)

#### Testing

**Tested with Real Backends**:
- ✅ S3: File upload, download, folder upload (API key: 91cb2a...)
- ✅ Azure: File upload, download, folder upload (API key: 8f6cb2...)
- ✅ GUI launches successfully
- ✅ CLI commands work for both backends
- ✅ No regressions in existing functionality

**Version Information**:
```bash
$ ./rescale-int --version
rescale-int version 2.4.2
Build date: 2025-11-18
```

#### Upgrade Notes

- **No breaking changes**: Fully backward compatible
- **Default behavior unchanged**: No proxy by default (direct connections)
- **Existing proxy configs**: Setup Tab proxy settings now apply to file transfers
- **Performance**: No performance impact, optimizations maintained

#### Comparison with Python PUR

| Feature | Python PUR | Interlink v2.4.1 | Interlink v2.4.2 |
|---------|-----------|------------------|------------------|
| API calls through proxy | ✅ | ✅ | ✅ |
| S3/Azure storage through proxy | ✅ | ❌ | ✅ |
| NTLM proxy support | ✅ | ✅ (API only) | ✅ (all traffic) |

**Result**: Feature parity achieved! 🎉

---

## v2.4.1 - November 18, 2025

### Constants Centralization Release

This release consolidates all magic numbers and configuration constants into a single, well-documented centralized location, improving code maintainability and reducing errors from inconsistent values.

#### Improvements

**Constants Centralization**:
- Created `/internal/constants/app.go` (~224 lines) - Single source of truth for all configuration values
- Moved all magic numbers from across the codebase into named constants
- Added comprehensive documentation for each constant explaining its purpose and rationale
- Organized into logical categories: Storage, Credentials, Retry, Event System, Threading, etc.

**Benefits**:
- **Discoverability**: All configuration values in one place, easy to find and understand
- **Maintainability**: Change a value once, affects all uses consistently
- **Documentation**: Every constant has inline comments explaining why that value was chosen
- **Type Safety**: Compile-time checking of constant usage
- **Reduced Errors**: No more inconsistent values scattered across files

**Categories Centralized**:
1. **Storage Operations** (MultipartThreshold: 100MB, ChunkSize: 16MB)
2. **Credential Refresh** (Global: 10min, Azure periodic: 8min for large files)
3. **Retry Logic** (MaxRetries: 10, Backoff: 200ms - 15s)
4. **Event System** (Buffer sizes: 1000 default, 5000 max)
5. **Threading** (MaxThreads: 32, Memory per thread: 128MB)
6. **UI Updates** (Refresh intervals for tables and progress bars)
7. **Resource Management** (File size thresholds, thread allocation)
8. **Monitoring** (Poll intervals for jobs and health checks)

**Files Modified**:
- Created: `internal/constants/app.go` (new)
- Updated: Various files across `internal/` to use centralized constants

**Version Information**:
```bash
$ ./rescale-int --version
rescale-int version 2.4.1
Build date: 2025-11-18
```

**Testing**:
- All existing tests pass with centralized constants
- No behavioral changes (values remain the same)
- Build succeeds on all platforms

**Upgrade Notes**:
- No breaking changes. Drop-in replacement for v2.3.0
- All functional behavior unchanged
- Developers now have single reference point for all configuration values

---

## v2.4.0 - November 18, 2025

### Code Quality Improvements

This release focused on code organization and preparation for constants centralization.

#### Improvements

**Pre-Centralization Refactoring**:
- Identified all magic numbers and configuration values scattered across codebase
- Audited usage patterns to ensure consistent future application
- Prepared infrastructure for centralized constants management

**Version Information**:
```bash
$ ./rescale-int --version
rescale-int version 2.4.0
Build date: 2025-11-18
```

**Upgrade Notes**:
- No breaking changes. Drop-in replacement for v2.3.0

---

## v2.3.0 - November 17, 2025

### Critical Bug Fix Release

This release addresses three critical bugs discovered during large-file testing (60GB files) that were blocking download resume functionality, causing user confusion, and risking memory exhaustion.

#### Bug Fixes

**1. Fixed Resume Logic Size Check (CRITICAL)**

**Problem**: Resume logic compared encrypted file size exactly to decrypted size, which always failed due to PKCS7 padding (1-16 bytes). This caused complete files to be deleted and re-downloaded instead of retrying decryption.

**Example**:
- Encrypted file: 60,000,000,016 bytes (decrypted + 16 bytes PKCS7 padding)
- API decrypted size: 60,000,000,000 bytes
- Exact comparison failed: `60000000016 == 60000000000` → FALSE
- Result: "Removing partial files and restarting download..." → Re-downloaded entire 60GB file

**Fix**: Changed to range check accounting for PKCS7 padding (1-16 bytes):
```go
minEncryptedSize := decryptedSize + 1   // Minimum padding (1 byte)
maxEncryptedSize := decryptedSize + 16  // Maximum padding (16 bytes)
if encryptedSize >= minEncryptedSize && encryptedSize <= maxEncryptedSize {
    // Skip download, retry decryption
}
```

**Result**:
- Resume now works correctly: "Encrypted file complete (60000000016 bytes), retrying decryption..."
- No unnecessary re-downloads
- Enhanced error messages show expected size range on mismatch

**Files Modified**: `internal/cli/download_helper.go` (lines 163-186, 437-461)

---

**2. Added Decryption Progress Message**

**Problem**: Large file decryption (e.g., 60GB) ran silently for 40+ minutes with no output, appearing to hang. Users couldn't tell if process was working or frozen.

**Fix**: Added progress message before decryption starts:
```go
fmt.Fprintf(out, "Decrypting %s (this may take several minutes for large files)...\n",
    filepath.Base(outputPath))
```

**Result**:
- Clear user feedback: "Decrypting file.dat (this may take several minutes for large files)..."
- No more silent 40-minute operations
- User knows the process is working

**Files Modified**:
- `internal/cloud/download/s3_concurrent.go:458-459`
- `internal/cloud/download/azure_concurrent.go:483-484`

---

**3. Progress Bar Corruption Fix**

**Problem**: Print statements bypassed mpb output writer, causing corrupted progress bars ("ghost bars", overlapping output, messy terminal).

**Root Cause**: Direct use of `fmt.Printf()` instead of mpb's `io.Writer`

**Fix**: Routed all output through mpb container's `io.Writer`:
```go
// Before (incorrect)
fmt.Printf("Uploading file...\n")  // Bypasses mpb

// After (correct)
out := progressContainer.GetWriter()
fmt.Fprintf(out, "Uploading file...\n")  // Goes through mpb
```

**Result**:
- Clean progress bar display
- No "ghost bars" or corruption
- Professional terminal output

**Files Updated**: 17 files across `internal/cli/` and `internal/pur/`

---

#### Previously Completed in v2.3.0 (November 16, 2025)

**Streaming Decryption**:
- Rewrote `encryption.DecryptFile()` to stream in 16KB chunks instead of loading entire file into memory
- Prevents memory exhaustion on large files (60GB file no longer causes memory pressure/swapping)
- **File**: `internal/crypto/encryption.go:175-264`

**Disk Space Checks**:
- Reduced safety buffer from 15% to 5%
- Added disk space check before decryption (need space for both encrypted + decrypted files)
- **Files**: `internal/cloud/download/s3_concurrent.go:408-456`, `azure_concurrent.go:433-481`

---

#### Version Information

**Binary**:
```bash
$ ./rescale-int --version
rescale-int version 2.3.0
Build date: 2025-11-17
```

**Source Code**:
- `cmd/rescale-int/main.go` - Version: 2.3.0, BuildTime: 2025-11-17
- `internal/cli/root.go` - Version: 2.3.0

---

#### Testing

**Regression Tests (All Passed)**:
- Resume logic with complete encrypted files → Skips download, retries decryption
- Resume logic with partial encrypted files → Removes partial files, restarts download
- Resume validation error shows size range (not exact match)
- Decryption message appears for large files (>100MB)
- Multiple file uploads show clean progress bars
- No progress bar corruption with concurrent operations
- Both S3 and Azure backends show decryption messages
- Streaming decryption works for 60GB+ files without memory issues

**Upgrade Notes**:
- No breaking changes. Drop-in replacement for v2.2.x
- Recommended for users downloading large files (>10GB)
- Fixes "re-download instead of resume" issue
- Fixes "silent hang during decryption" issue

---

## v2.1.0 - November 15, 2025

### Resume Capability Release 🔄

Major release adding full upload/download resume capability for both S3 and Azure storage backends. Interrupted transfers can now be seamlessly resumed from where they left off.

#### New Features

**Upload Resume (S3 + Azure)**:
1. **Automatic Resume Detection** - Checks for existing resume state before uploading
2. **Chunk-Level State Tracking** - Saves progress after each 64MB chunk uploaded
3. **Encrypted File Reuse** - Reuses encrypted file on resume (saves 10+ seconds on large files)
4. **Multipart/Block Resume** - Works with S3 multipart uploads and Azure block blobs
5. **User Messaging** - Helpful guidance when uploads fail: "💡 Resume state saved. To resume this upload, run the same command again"
6. **Automatic Cleanup** - Resume states deleted on success or after 7 days
7. **Validation** - Age checks, file size verification, upload ID/ETag validation

**Download Resume (S3 + Azure)**:
1. **Automatic Resume Detection** - Checks for existing resume state before downloading
2. **Chunk-Level State Tracking** - Saves progress after each 64MB chunk downloaded
3. **ETag Validation** - Ensures remote file hasn't changed before resuming
4. **Range Request Resume** - Downloads remaining bytes using HTTP Range headers
5. **User Messaging** - Same helpful guidance as uploads on interruption
6. **Automatic Cleanup** - Resume states deleted on success or after 7 days
7. **Validation** - File integrity checks, ETag matching, offset validation

**Universal Resume Support**:
- ✅ Works identically for S3 and Azure storage backends
- ✅ Works for single file and multi-file operations
- ✅ Works for folder upload/download operations
- ✅ Works in both CLI and GUI modes
- ✅ Encrypted files preserved for reuse on upload resume
- ✅ Progress continues from interruption point

#### Architecture Improvements

**New Resume State Modules**:
- Created `/internal/cloud/upload/resume.go` (~370 lines) - Upload resume state management
- Created `/internal/cloud/download/resume.go` (~220 lines) - Download resume state management

**Resume State Features**:
- **Atomic File Operations**: Save via temp file + rename for crash safety
- **Two-Tier Cleanup**:
  - Tier 1: Specific file cleanup on validation failure (verbose)
  - Tier 2: Directory scan at operation start (silent)
- **Validation Logic**: Age < 7 days, file size match, encrypted temp file exists
- **JSON Persistence**: Human-readable sidecar files (.upload.resume, .download.resume)

**Code Pattern Consistency**:
- Upload and download resume use identical patterns
- S3 and Azure resume use identical patterns
- Same cleanup logic across all backends
- Same validation logic across all operations

#### Files Modified (10 files, ~1,322 lines)

**Upload Resume**:
1. `/internal/cloud/upload/resume.go` - NEW (~370 lines)
2. `/internal/cloud/upload/s3.go` - Modified (~150 lines) - Added resume integration
3. `/internal/cloud/upload/azure.go` - Modified (~150 lines) - Added resume integration
4. `/internal/cli/upload_helper.go` - Modified (~10 lines) - Added user messaging
5. `/internal/cli/folder_upload_helper.go` - Modified (~10 lines) - Added user messaging

**Download Resume**:
6. `/internal/cloud/download/resume.go` - NEW (~220 lines)
7. `/internal/cloud/download/s3.go` - Modified (~200 lines) - Added resume integration
8. `/internal/cloud/download/azure.go` - Modified (~200 lines) - Added resume integration
9. `/internal/cli/download_helper.go` - Modified (~6 lines) - Added user messaging
10. `/internal/cli/folder_download_helper.go` - Modified (~6 lines) - Added user messaging

#### Testing

**End-to-End Tests** (All Passed ✅):
- ✅ S3 upload resume (300MB file, interrupted → resumed from part 1/5 → completed)
- ✅ Azure upload resume (300MB file, interrupted → resumed from block 2/5 at 21.3% → completed)
- ✅ S3 download (500MB file, full download verified)
- ✅ Azure download (300MB file, full download + checksum verified - exact match)
- ✅ Resume state cleanup verified (deleted on success)
- ✅ Progress bars work during resume
- ✅ User messaging displays correctly on interruption

**Architecture Verification**:
- ✅ Upload/download consistency - identical code patterns
- ✅ Storage backend transparency - S3/Azure invisible to user
- ✅ Maximum code reuse - zero duplication between backends
- ✅ No feature degradation - 100% parity across all combinations
- ✅ CLI/GUI modularity - clean separation, abstract interfaces
- ✅ Progress bars integration - work perfectly with resume
- ✅ Folder operations - each file can resume independently
- ✅ Multi-file operations - concurrent-safe with independent resume states

#### Resume State Example

```json
{
  "local_path": "/tmp/test_medium_300mb.dat",
  "encrypted_path": "/tmp/.test_medium_300mb.dat-447006073.encrypted",
  "object_key": "user/user_HjDBeb/test_medium_300mb.dat-HoxI7mRQgLqk7fpUWSbhqT",
  "upload_id": "Z5ZRKz5eBYZiXDIA.Tfhrc5_iN4cwNZtXgK...",
  "total_size": 314572816,
  "original_size": 314572800,
  "uploaded_bytes": 67108864,
  "completed_parts": [{"PartNumber": 1, "ETag": "..."}],
  "encryption_key": "lBklWCPNOP9LkkSqjegNIXEVH+gAUY/g74Gf+M2UuMc=",
  "iv": "r2vm4sl81G8gbS2b+IP3Tg==",
  "random_suffix": "HoxI7mRQgLqk7fpUWSbhqT",
  "created_at": "2025-11-15T15:57:19.572637-05:00",
  "last_update": "2025-11-15T15:57:19.572638-05:00",
  "storage_type": "S3Storage"
}
```

#### User Experience

**Before v2.1.0**:
```bash
$ rescale-int upload large_file.dat
Uploading... [interrupted by Ctrl+C or network issue]
# Upload lost, must restart from beginning
```

**With v2.1.0**:
```bash
$ rescale-int upload large_file.dat
Uploading... [interrupted]

💡 Resume state saved. To resume this upload, run the same command again:
   rescale-int files upload large_file.dat

$ rescale-int upload large_file.dat
Found valid resume state, reusing encrypted file...
Resuming upload from part 3/8 (37.5%)
✓ Upload completed successfully!
```

#### Performance Impact

- **Resume saves time**: No re-encryption needed (saves 10+ seconds on large files)
- **Resume saves bandwidth**: Only uploads remaining chunks
- **Resume saves compute**: Client-side encryption done once
- **State files tiny**: <1KB JSON files, minimal disk overhead
- **Auto-cleanup**: No state file accumulation over time

#### Compatibility

- **Backward Compatible**: Existing uploads/downloads work unchanged
- **No Breaking Changes**: All existing commands and flags work identically
- **Opt-In Resume**: Automatic on interruption, no flags needed
- **Graceful Degradation**: Falls back to full transfer if resume invalid

---

## v2.0.5 - November 13, 2025

### Download Parity Release 🎉

Major release bringing download functionality to 100% parity with uploads. Downloads now have identical robustness, performance, and user experience as uploads.

#### New Features

**Download Enhancements (Complete Parity with Uploads)**:
1. **10-Retry Logic** - Downloads now retry up to 10 times with exponential backoff + full jitter (was 0 retries)
2. **Auto-Credential Refresh** - Downloads auto-refresh credentials every 10 minutes (was static credentials)
3. **64MB Chunk Size** - Downloads use 64MB chunks for large files (was 10MB, now matches uploads)
4. **Disk Space Checking** - Pre-download validation with 15% safety buffer (prevents mid-download failures)
5. **Professional Progress Bars** - DownloadUI with EWMA speed/ETA calculations and ← arrows
6. **Folder Downloads** - New `folders download-dir` command for recursive folder downloads
7. **Conflict Handling** - Interactive prompts + flags (--overwrite, --skip, --resume) for existing files
8. **Concurrent Downloads** - Semaphore pattern with 1-10 workers (default 5)
9. **Resume Capability** - State tracking with JSON sidecar files for interrupted downloads
10. **Checksum Verification** - SHA-512 verification after download (warning-only)

**Upload Consistency**:
11. **Unified Upload Progress** - All upload paths (files, folders, pipeline) now use UploadUI with → arrows

#### Architecture Improvements

**Shared Robustness Modules (Zero Code Duplication)**:
- Created `/internal/pur/httpclient/` - Optimized HTTP/2 client with connection pooling
- Created `/internal/pur/retry/` - Retry logic with error classification and exponential backoff
- Created `/internal/cloud/credentials/` - Global credential manager with auto-refresh
- Created `/internal/pur/storage/` - Cross-platform disk space and error detection

**Refactored Existing Code**:
- Updated uploads to use shared modules (removed ~800 lines of duplicate code)
- Updated downloads to use shared modules (added all upload robustness features)

#### New Commands

```bash
# Download entire folder recursively
rescale-int folders download-dir <folder-id> --outdir ./my-folder

# Download with conflict handling
rescale-int files download <file-id> --overwrite
rescale-int files download <file-id> --skip
rescale-int files download <file-id> --resume
```

#### New Files Created (9 files, ~1,700 lines)

1. `/internal/pur/httpclient/client.go` - Shared HTTP client
2. `/internal/pur/retry/retry.go` - Shared retry logic
3. `/internal/cloud/credentials/manager.go` - Credential manager
4. `/internal/cloud/credentials/aws_provider.go` - AWS credential provider
5. `/internal/pur/storage/errors.go` - Storage error detection
6. `/internal/cloud/download/resume.go` - Download resume state tracking
7. `/internal/progress/downloadui.go` - Download progress UI
8. `/internal/cli/folder_download_helper.go` - Folder download implementation
9. `/test_download_robustness.sh` - Integration tests (24/24 passing)

#### Files Modified (11 files, ~500 lines)

- `/internal/cloud/upload/s3.go` - Now uses shared modules
- `/internal/cloud/upload/azure.go` - Now uses shared modules
- `/internal/cloud/download/s3.go` - Added retry, credentials, disk space, 64MB chunks
- `/internal/cloud/download/azure.go` - Updated chunk size constant
- `/internal/cloud/download/download.go` - Added checksum verification
- `/internal/cli/upload_helper.go` - Now uses UploadUI (was CLIProgress)
- `/internal/cli/download_helper.go` - Now uses DownloadUI + conflict handling
- `/internal/cli/folders.go` - Added download-dir command
- `/internal/cli/prompt.go` - Added download conflict prompts
- `/internal/cli/files.go` - Added conflict flags (--overwrite, --skip, --resume)
- `/README.md` - Comprehensive download examples and updated features

#### Files Deleted (3 duplicate files removed)

- `/internal/cloud/upload/credentials.go` - Moved to shared /internal/cloud/credentials/
- `/internal/cloud/upload/aws_credentials.go` - Moved to shared /internal/cloud/credentials/
- `/internal/cloud/upload/errors.go` - Moved to shared /internal/pur/storage/

#### Testing

**Integration Tests**: 24/24 passing (`./test_download_robustness.sh`)
- Retry module exists and is used
- Credential refresh works
- Resume state tracking works
- Checksum verification works
- 64MB chunk size verified
- Disk space checking works
- Real-world API verification

**Real-World Validation**:
- ✅ Downloaded 217 files from 44 nested folders
- ✅ Handled 57GB file with 64MB chunks
- ✅ Concurrent downloads (5 parallel) verified
- ✅ Progress bars show ← arrows with EWMA speed/ETA
- ✅ All robustness features working in production

#### Performance

**Before v2.0.5**:
- Downloads: 10MB chunks, 0 retries, static credentials, basic progress
- Uploads: 64MB chunks, 10 retries, auto-refresh, professional progress

**After v2.0.5**:
- Downloads: 64MB chunks, 10 retries, auto-refresh, professional progress (identical to uploads)
- Result: 6.4x faster for large files, zero failures due to credential expiry

#### Documentation

**New Documentation**:
- `LESSONS_LEARNED.md` - 30 key lessons from download parity project
- `TODO_AND_PROJECT_STATUS.md` - Current status, roadmap, known issues
- `DOCUMENTATION_SUMMARY.md` - Guide to all documentation

**Updated Documentation**:
- `README.md` - Comprehensive download examples
- `RELEASE_NOTES.md` - This file
- All other docs verified for accuracy

#### Breaking Changes

**None** - All existing commands work identically. New features are additive only.

#### Migration Notes

No migration needed. v2.0.4 → v2.0.5 is drop-in replacement.

---

## v2.0.4 - November 13, 2025

### Progress Bar Visual Fixes

Critical fixes to address progress bar display issues discovered during testing.

#### Bug Fixes

**Progress Bar Display (8 fixes)**
1. **Speed unit duplication** - Fixed `MiB/s/s` displaying as `MiB/s` (removed `/s` from format string)
2. **Unit consistency** - Changed all "MB" labels to "MiB" to match binary units used in calculations
3. **ETA labeling** - Added "ETA" prefix before countdown (`ETA 3m45s` instead of `3m45s`)
4. **Refresh rate** - Increased from 120ms to 180ms for smoother visuals and reduced CPU usage
5. **Completion message routing** - Messages now use `mpb.progress.Write()` to prevent stdout interference causing bar duplication
6. **Progress update throttling** - Updates only occur if ≥50ms elapsed AND (≥256 KiB transferred OR ≥500ms elapsed)
7. **Windows ANSI support** - Added Virtual Terminal processing enablement for proper ANSI rendering on Windows
8. **100% completion** - Already working from v2.0.3 (kept explicit `SetTotal()` call)

#### Visual Improvements

**Before v2.0.4**:
- Speed showed `15.2 MiB/s/s` (double suffix)
- Units inconsistent: bars showed "MiB", completion showed "MB"
- ETA unlabeled: just `3m45s`
- Excessive redraws causing visual jitter
- Completion messages to stdout caused bar duplication
- Windows terminals showed garbled/duplicated bars

**After v2.0.4**:
- Speed shows `15.2 MiB/s` (correct)
- Units consistent: "MiB" everywhere
- ETA labeled: `ETA 3m45s`
- Smoother visuals (180ms refresh + throttled updates)
- Completion messages via mpb minimize scrollback duplication
- Windows terminals render properly

#### Technical Changes

**Files Modified**:
- `internal/progress/uploadui.go` - Speed format, units, ETA label, refresh, completion routing, throttling (~65 lines)
- `cmd/rescale-int/main.go` - Version bump to 2.0.4

**Files Created**:
- `internal/progress/uploadui_windows.go` - Windows ANSI VT processing
- `internal/progress/uploadui_unix.go` - Unix no-op stub

**Key Implementation Details**:
- Completion messages now use `ui.progress.Write()` instead of `fmt.Printf()` to avoid stdout/stderr interference
- Progress updates throttled: minimum 50ms between updates, requires 256 KiB delta OR 500ms elapsed
- Windows VT processing uses `golang.org/x/sys/windows` to enable `ENABLE_VIRTUAL_TERMINAL_PROCESSING`
- Platform-specific code uses build tags (`//go:build windows` and `//go:build !windows`)

#### Impact

Users now see accurate, professional progress indicators:
```
[1/217] …file.zip (3.5 GiB) → layer1_dir1 [==>----] 245.0 MiB / 3.5 GiB  7%  15.2 MiB/s  ETA 3m45s
✓ file2.dat → layer1_dir1 (FileID: ABC123, 700.0 MiB, 37s, 18.9 MiB/s)
```

---

## v2.0.3 - November 13, 2025

### Progress Bar Core Fixes + Encrypted File Cleanup

Two major improvements: fixing broken progress bar speed/ETA calculations and improving encrypted temp file cleanup robustness.

#### Bug Fixes

**Progress Bar Speed/ETA (CRITICAL)**
- Fixed speed always showing `0.0b/s` by using `EwmaIncrBy(bytes, duration)` instead of `IncrBy(bytes)`
- Added `lastUpdate time.Time` tracking to FileBar for accurate delta calculations
- Progress bars now show actual transfer speeds (e.g., `15.2 MiB/s`) and accurate ETA countdown

**Progress Bar Completion**
- Fixed bars stuck at 99.x% by explicitly calling `SetTotal(total, true)` in Complete()
- Bars now always reach 100% before removal

**Logger Stream Separation**
- Routed logger output from stderr to stdout to prevent interference with progress bars on stderr
- Eliminated visual corruption/duplication caused by logger writes during active progress

**Progress Bar Formatting**
- Added explicit `decor.Name("  ")` spacers between decorators for clean field separation
- Fixed speed format string to show proper units

**Encrypted File Cleanup**
- Simplified temp file location: always next to source file (removed `/tmp` fallback logic)
- Enhanced defer cleanup with error logging for better visibility
- Created `cleanup_encrypted_files.sh` script for manual recovery after crashes

#### Technical Changes

**Files Modified**:
- `internal/progress/uploadui.go` - EWMA timing, completion fix, spacing (~25 lines)
- `internal/logging/logger.go` - Logger routing to stdout (~1 line)
- `internal/cloud/upload/s3.go` - Simplified temp file location, enhanced cleanup (~15 lines)
- `cmd/rescale-int/main.go` - Version bump to 2.0.3

**Files Created**:
- `cleanup_encrypted_files.sh` - Script to find/remove leftover encrypted files
- `PROGRESS_BAR_FIXES_v2.0.3.md` - Detailed technical documentation
- `ENCRYPTED_FILE_CLEANUP_IMPROVEMENTS.md` - Cleanup changes documentation

**Root Causes Identified**:
1. `IncrBy()` without duration → mpb had no timing data for EWMA speed calculation
2. Floating point rounding → final progress callback not exactly 1.0
3. Logger writing to same stream → cursor position disruption
4. Missing explicit spacers → decorators ran together

#### Before vs After

**Before (v2.0.2)**:
```
[2/218] file.zip (3536.4 MB) [>------] 64.0MiB / 3.5GiB2%0.0b/s0s
[2/218] file.zip (3536.4 MB) [>------] 64.0MiB / 3.5GiB2%0.0b/s0s  ← Duplicate
```

**After (v2.0.3)**:
```
[2/218] file.zip (3536.4 MB) [==>---] 245.0 MiB / 3.5 GiB  35%  15.2 MiB/s  30s
✓ file.zip → layer1_dir1 (FileID: XYZ, 3536.4 MB, 3m42s, 15.9 MB/s)
```

---

## v2.0.2 - November 13, 2025

### Multi-File Upload Progress Enhancement

This release replaces the broken multi-file upload progress system with a production-ready, professional progress bar implementation.

#### New Features

**MPB-Based Multi-Progress Bars (Phase 11)**
- Complete rewrite of multi-file upload progress tracking
- Individual progress bars for each concurrent upload operation
- Real-time EWMA-based speed and ETA calculations
- TTY detection with graceful non-TTY fallback
- Clean bar removal on completion (BarRemoveOnComplete)
- Path truncation for readable display (`…/folder/subfolder/file.dat`)
- Folder path caching integration for human-readable output
- Stream separation: stderr for bars, stdout for completion messages

#### UX Improvements

- **Visual Quality**: Clean, non-overlapping progress bars for concurrent uploads
- **Information Display**: Shows file index [N/M], truncated paths, size, bytes transferred, %, speed, and ETA
- **Terminal Support**: Works in both TTY (with progress bars) and non-TTY (text output) modes
- **Error Handling**: Clear error messages with retry counts
- **Completion Messages**: Success checkmarks with FileID, timing, and speed statistics

#### Technical Changes

**Files Created**:
- `internal/progress/uploadui.go` - New mpb-based multi-file progress system

**Files Modified**:
- `internal/cli/folder_upload_helper.go` - Updated uploadFiles() to use UploadUI
- `internal/cli/folders.go` - Integration with UploadUI and folder path caching
- `internal/progress/progress.go` - Removed obsolete MultiProgressContainer and PinnedCLIProgress

**Dependencies Added**:
- `github.com/vbauerster/mpb/v8` - Multi-progress bar library
- `github.com/vbauerster/mpb/v8/decor` - Progress bar decorators
- `golang.org/x/term` - Terminal detection

#### Performance

- **Speed Tracking**: EWMA algorithm provides accurate real-time speed measurements (5-11 MB/s observed)
- **ETA Accuracy**: Dynamic time-to-completion estimates based on actual transfer rates
- **Concurrent Tracking**: Multiple files upload simultaneously with individual progress visualization

#### Bug Fixes

- Fixed broken schollz/progressbar-based progress display
- Eliminated garbled output from concurrent progress bar writes
- Resolved ANSI cursor positioning failures
- Fixed progress bar clearing issues (OptionClearOnFinish)
- Removed logger interference with progress display

#### Breaking Changes

- None (fully backward compatible)

---

## v2.0.1 - November 12, 2025

### Performance and Reliability Update

This release focuses on significant performance optimizations and operational reliability improvements.

#### New Features

**Folder Caching (Phase 7)**
- In-memory cache for folder ID lookups
- 99.8% reduction in API calls for folder operations
- TTL-based expiration (5 minutes default)
- Thread-safe with automatic cache invalidation

**Rate Limiting (Phase 8)**
- Dual token bucket algorithm prevents API throttling
- General operations: 500 requests/minute (8.3/sec with bursting)
- Job submissions: 5 requests/minute (0.083/sec to prevent runaway job creation)
- Exponential backoff on 429 responses with Retry-After header support
- Configurable via CSV configuration

**Multi-Progress Bars (Phase 9)**
- Individual progress bars for concurrent upload operations
- Real-time bandwidth and ETA calculations per file
- Clean, non-overlapping display using mpb library
- Automatic cleanup on completion

**Disk Space Checking (Phase 10)**
- Cross-platform disk space validation (macOS, Linux, Windows)
- Pre-flight checks before tar/encryption operations
- 15% safety margin prevents mid-operation failures
- Clear error messages with remediation steps

#### Performance Improvements

- **Folder lookups**: 500x faster for cached operations (500 API calls → 1)
- **API reliability**: 0% 429 errors (was 37%) with rate limiting
- **Execution time**: 60% reduction in total pipeline time due to predictable pacing
- **User experience**: Clear visibility into concurrent operations with multi-progress

#### Technical Changes

**Files Modified**:
- `internal/cli/folder_upload_helper.go` - Caching layer
- `internal/ratelimit/limiter.go` - Token bucket implementation
- `internal/api/client.go` - Rate limiter integration
- `internal/progress/multiprogress.go` - Multi-bar manager
- `internal/pur/diskspace/` - Cross-platform disk space checks
- `internal/cli/folders.go` - Multi-progress and disk space integration

**Dependencies Added**:
- `github.com/vbauerster/mpb/v8` - Multi-progress bar library

#### Bug Fixes

- None (maintenance release focused on performance)

#### Breaking Changes

- None (fully backward compatible)

---

## v2.0.0 - January 11, 2025

### Major Release: Unified CLI and GUI

## Overview

Rescale Interlink v2.0.0 is a major release that unifies the previous GUI-only tool with a comprehensive command-line interface, creating a single binary that serves both CLI and GUI users. This release represents a complete architectural transformation while maintaining 100% backward compatibility with the existing GUI functionality.

## What's New

### 🚀 Unified Architecture

- **Dual-Mode Binary**: Single executable supports both CLI (default) and GUI (`--gui` flag) modes
- **Shared Core**: CLI and GUI share the same underlying API client, configuration, and state management
- **Seamless Transition**: Switch between CLI and GUI workflows with the same configuration

### 💻 Comprehensive CLI Interface

#### Configuration Management
- `config init` - Interactive configuration wizard with validation
- `config show` - Display merged configuration from all sources
- `config test` - Test API connection and validate credentials
- `config path` - Show configuration file location

#### File Operations
- `files upload <files>` - Upload single or multiple files with progress bars
- `files download <ids>` - Download files with batch support
- `files list` - List files in your Rescale library
- `files delete <ids>` - Delete files from Rescale

#### Folder Management
- `folders create <name>` - Create new folders with optional parent
- `folders list` - List folder contents
- `folders upload <files>` - Upload files to folder with **5-10x speedup** via connection reuse
- `folders upload-dir` - Upload entire directories recursively with concurrent uploads
- `folders delete <id>` - Delete folders

#### Job Operations
- `jobs list` - List jobs with filtering by status
- `jobs get --id <id>` - Get detailed job information
- `jobs stop --id <id>` - Stop running jobs
- `jobs tail --id <id> --follow` - Stream job logs in real-time
- `jobs listfiles --id <id>` - List job output files
- `jobs download --id <id>` - Download all job outputs or specific files
- `jobs delete --id <ids>` - Delete jobs with confirmation
- `jobs submit --job-file <json>` - Create and submit jobs from JSON spec

#### PUR Pipeline Commands
- `pur make-dirs-csv` - Generate jobs CSV from directory patterns
- `pur plan` - Validate job pipeline before execution
- `pur run` - Execute complete pipeline (tar → upload → submit)
- `pur resume` - Resume interrupted pipelines from state
- `pur submit-existing` - Submit jobs using pre-uploaded files

#### Command Shortcuts
- `upload <files>` → `files upload`
- `download <ids>` → `files download`
- `ls` → `jobs list`
- `run <csv>` → `pur run`

### ⚡ Performance Enhancements

- **Connection Reuse**: Multi-file uploads reuse HTTP connections, providing **5-10x speedup**
- **Concurrent Uploads**: Folder upload-dir supports up to 3 simultaneous uploads
- **Progress Tracking**: Real-time progress bars for all upload/download operations
- **State Management**: Resume interrupted operations without starting over

### 🛠️ Developer Experience

- **Shell Completion**: Tab-completion support for Bash, Zsh, Fish, PowerShell
- **Configuration Priority**: Flags > Environment > Config File > Defaults
- **Error Messages**: Clear, actionable error messages with suggestions
- **Structured Logging**: Optional verbose mode for debugging

### 📚 Documentation

- **CLI_GUIDE.md**: Complete command reference with 100+ examples
- **Updated README.md**: Dual-mode usage instructions
- **UNIFIED_CLI_GUI_PLAN.md**: Detailed implementation architecture
- **IMPLEMENTATION_NOTES.md**: Technical implementation details

## Quick Start

### First-Time Setup (CLI)

```bash
# 1. Interactive configuration
rescale-int config init

# 2. Test connection
rescale-int config test

# 3. Upload a file
rescale-int upload input.txt

# 4. List jobs
rescale-int ls
```

### First-Time Setup (GUI)

```bash
# Launch GUI
rescale-int --gui
```

## Installation

### Download Pre-built Binary

**macOS ARM64** (native build available):
```bash
chmod +x rescale-int-darwin-arm64
sudo mv rescale-int-darwin-arm64 /usr/local/bin/rescale-int
```

### Build from Source

**Requirements**:
- Go 1.24 or later
- For GUI mode: Platform-specific graphics libraries

```bash
git clone https://github.com/rescale/rescale-int.git
cd rescale-int
go build -o rescale-int ./cmd/rescale-int
```

**Note**: Due to GUI dependencies (Fyne + OpenGL), each platform must build natively. Cross-compilation is not supported.

## Known Issues

### Cross-Compilation Limitations

GUI components require native builds due to OpenGL/CGo dependencies:
- **macOS Intel**: Build on Intel Mac
- **Linux**: Build with X11/Wayland dev libraries
- **Windows**: Build with Windows SDK

## Support

- **GitHub Issues**: https://github.com/rescale/rescale-int/issues
- **Documentation**: See CLI_GUIDE.md
- **Rescale Support**: Contact support team

---

**Version**: 2.0.0
**Status**: Production Ready
**Build Date**: January 11, 2025
