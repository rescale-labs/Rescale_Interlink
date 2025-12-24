# Rescale Interlink v3.6.x Roadmap

**Created:** December 23, 2025
**Status:** v3.6.0 Released
**Target:** Next major feature release

---

## Priority Tiers

- **P0 (Critical)**: Architectural violations, blocking issues - must fix in v3.6.0
- **P1 (High)**: Important features/fixes - target v3.6.1
- **P2 (Medium)**: Nice-to-have improvements - target v3.6.2
- **P3 (Low)**: Future consideration, may defer

---

## v3.6.0 Release Summary (December 23, 2025)

### Completed in v3.6.0

| Item | Description | Status |
|------|-------------|--------|
| 1.1 | Local Filesystem Abstraction | ✅ DONE |
| 1.2 | Hidden File Detection Consolidation | ✅ DONE |
| 1.3 | CLI API Client Consolidation | ⏸️ DEFERRED (not architectural) |
| 2.1 | Windows Download Timing Instrumentation | ✅ DONE (investigation ready) |
| 3.1 | Page Size Consistency | ✅ DONE |
| 3.2 | Maximize Button Testing | 🔄 Manual testing required |

### What Was Delivered

1. **`internal/localfs/` Package** - New unified local filesystem abstraction
   - `hidden.go` - `IsHidden()`, `IsHiddenName()` functions
   - `options.go` - `ListOptions`, `WalkOptions` structs
   - `browser.go` - `ListDirectory()`, `Walk()`, `WalkFiles()` functions
   - `localfs_test.go` - Comprehensive unit tests

2. **Hidden File Detection Consolidation** - 9 duplicate locations migrated to use `localfs.IsHidden()`:
   - `internal/util/multipart/multipart.go`
   - `internal/cli/folder_upload_helper.go`
   - `internal/core/engine.go` (4 instances)
   - `internal/gui/scan_preview.go`
   - `internal/gui/local_browser.go`

3. **GUI Page Size Consistency** - Removed `SetPageSize(200)` override in `remote_browser.go`, now uses default (25).

4. **Windows Download Timing** - Added `RESCALE_TIMING=1` instrumentation to diagnose end-of-download slowness:
   - `[TIMING] Download transfer complete: Xms`
   - `[TIMING] Checksum verification: Xms` (likely culprit for large files)

---

## 1. Architectural Debt - North Star Violations

### 1.1 Local Filesystem Abstraction (P0) - ✅ COMPLETED v3.6.0

**Was:** No shared `internal/localfs/` package. Each component directly used OS primitives with duplicated logic.

**Now:** Created `internal/localfs/` package with unified interface:

```go
// internal/localfs/hidden.go
func IsHidden(path string) bool      // Check if path is hidden
func IsHiddenName(name string) bool  // Check if filename is hidden

// internal/localfs/browser.go
func ListDirectory(path string, opts ListOptions) ([]FileEntry, error)
func Walk(root string, opts WalkOptions, fn WalkFunc) error
func WalkFiles(root string, opts WalkOptions, fn WalkFunc) error
```

**Files created:**
- `internal/localfs/hidden.go`
- `internal/localfs/options.go`
- `internal/localfs/browser.go`
- `internal/localfs/localfs_test.go`

**Files migrated:**
- `internal/gui/local_browser.go` - Uses `localfs.IsHiddenName()`
- `internal/cli/folder_upload_helper.go` - Uses `localfs.IsHidden()`
- `internal/core/engine.go` - Uses `localfs.IsHidden()` (4 locations)
- `internal/gui/scan_preview.go` - Uses `localfs.IsHiddenName()`
- `internal/util/multipart/multipart.go` - Uses `localfs.IsHidden()`

---

### 1.2 Hidden File Detection Duplication (P0) - ✅ COMPLETED v3.6.0

**Was:** `strings.HasPrefix(name, ".")` duplicated in 9 locations.

**Now:** All consolidated to `localfs.IsHidden()` and `localfs.IsHiddenName()`.

---

### 1.3 CLI API Client Helper (P2) - Target v3.6.1

**Analysis Result:** Not an architectural violation, but worth cleaning up for maintainability.

**Current State:**
- **Transfer operations** (upload/download): Already unified - both GUI and CLI use `upload.UploadFile()` and `download.DownloadFile()`
- **Non-transfer operations** (list jobs, get status, list files, etc.): 27 instances of boilerplate `api.NewClient()` creation

**Proposed Solution:** Simple helper function in `internal/cli/`:

```go
// internal/cli/api_helper.go
func getAPIClient() (*api.Client, error) {
    cfg, err := loadConfig()
    if err != nil {
        return nil, fmt.Errorf("failed to load config: %w", err)
    }
    return api.NewClient(cfg)
}
```

**Benefits:**
- Reduces 6 lines to 2 lines across 27 locations
- Single point for future enhancements (global rate limiter, credential caching)
- Consistent error handling and messaging
- Easy refactor - no architectural changes needed

**Files to modify:**
- Create: `internal/cli/api_helper.go`
- Update: `files.go`, `folders.go`, `jobs.go`, `pur.go`, `shortcuts.go`, `hardware.go`, `software.go`

**Effort:** ~1 hour (straightforward search-and-replace refactor)

---

## 2. Performance & Reliability

### 2.1 Large File Download Performance on Windows (P0) - ✅ INSTRUMENTED v3.6.0, add even more instrumentation in 3.6.1, and will need to revisit in v3.6.2

**Issue:** Large file downloads reportedly slow when almost complete on Windows.  This seems to impact some uploads too, and in general, it's not clear what Interlink is doing during uploads and downloads. We need detailed instrumentation to be printed to the terminal.

**v3.6.0 Action:** Added timing instrumentation via `RESCALE_TIMING=1`:

```
[TIMING] Download transfer complete: 45000ms
[TIMING] Checksum verification: 30000ms   ← Likely culprit for large files
```

**Root Cause Hypothesis:** The `verifyChecksum()` function reads the ENTIRE file again after download to compute SHA-512. For a 10GB file, this means reading 10GB from disk AFTER transfer "completes".

**Why Windows specifically?** The checksum read is platform-agnostic, but Windows may amplify the impact due to:
1. **Windows Defender/Antivirus**: Real-time scanning intercepts file reads and scans content on access
2. **NTFS vs ext4/APFS**: Different file system caching behavior and buffer management
3. **File handle behavior**: Windows may hold locks longer or flush differently

The timing instrumentation will reveal whether:
- Checksum is slow everywhere (algorithm issue → streaming hash fix)
- Checksum is slow only on Windows (platform-specific I/O → investigate Defender exclusions, larger buffers)
- Something else is slow (truncate, close, etc.)

**v3.6.1 Potential Fixes (after confirming with instrumentation):**
- Option A: Compute checksum during download (streaming hash) - best fix if checksum is slow everywhere
- Option B: Use `--skip-checksum` flag (already exists) - immediate workaround
- Option C: Use larger buffer for checksum read (64KB → 256KB+) - may help on Windows
- Option D: Document Windows Defender exclusion for download directory

**Files modified in v3.6.0:**
- `internal/cloud/download/download.go` - Added timing around download and checksum verification

### 2.2 API Configuration Hardening (P1) - Target v3.6.1

**Issue:** Ensure API config handling is robust across all edge cases.

**Tasks:**
- [ ] Audit all API key loading paths (env, file, flag, config)
- [ ] Verify precedence is consistently applied
- [ ] Add validation for malformed API keys
- [ ] Test config migration from old paths
- [ ] Ensure token refresh works after long idle periods

---

## 3. GUI Usability & Polish

### 3.1 Page Size Consistency (P0) - ✅ COMPLETED v3.6.0

**Was:** Default page size 25 in `file_list_widget.go` but RemoteBrowser overrode to 200.

**Now:** Removed `SetPageSize(200)` call in `remote_browser.go:142`. Uses default 25 for consistency.

### 3.2 Maximize Button Cross-Platform (P0) - Manual Testing Required

**Tasks:**
- [ ] Test maximize on macOS (darwin-arm64, darwin-amd64)
- [ ] Test maximize on Linux (X11 and Wayland)
- [ ] Test maximize on Windows (with and without Mesa)
- [ ] Document any Fyne limitations

---

## 4. Upload & Job Initiation UX (P1) - Target v3.6.1

### 4.1 Folder Selection UI (P1)

**Issue:** Need UI to select folders for upload (vs individual files).

### 4.2 Single Job "Add Input" UX (P1)

**Issue:** Current input file selection UX is confusing.

### 4.3 PUR Mixed File/Folder Selection (P1)

**Issue:** PUR currently only scans for directories with a specific naming pattern.

### 4.4 Status and transfer speeds don't update correctly (P1) - Target v3.6.1

**Issue:** The message in the bottom-left corner of the UI, for example reading "Scanning remote directory..." when the user goes to download a large folder, sometimes doesn't update: for example in the example I gave, it will keep saying "Scanning remote......" even when the download begins.  I've also noticed that the download and upload speeds are sometimes very delayed in appearing, even to the point of not appearing at all until a download is done (like 10s of seconds delay).


---

## 5. Metadata, Tags & Automation Support (P1) - Target v3.6.1

**Feature:** Support Rescale tags, Automations, and custom metadata fields.

Review job id=kkGGJd to see an example JSON of a job that has an Automation attached to it.


---

## 6. Windows Service & Installer (P1) - Target v3.6.2

### 6.1 Windows Installer Package
### 6.2 Windows Service Mode
### 6.3 Windows Tray Integration
### 6.4 Auto-Downloader Service Functionality

---

## 7. Deferred / Future Consideration

- **macOS Service (launchd)** - Lower priority than Windows
- **Linux Service (systemd)** - Lower priority
- **Mobile apps** - Out of scope
- **Web interface** - Out of scope
- **Multi-software selection per job** - High complexity

---

## Implementation Order

### v3.6.0 - Architectural Foundation ✅ RELEASED

1. ✅ **1.1** Local filesystem abstraction (`internal/localfs/`)
2. ✅ **1.2** Hidden file detection (part of 1.1)
3. ⏸️ **1.3** CLI API client consolidation - DEFERRED
4. ✅ **2.1** Windows download timing instrumentation
5. ✅ **3.1** Page size consistency
6. 🔄 **3.2** Maximize button verification - manual testing

### v3.6.1 - Features & Fixes

7. **1.3** CLI API client helper (P2 - easy win, ~1 hour)
8. **2.1** Windows download fix (based on v3.6.0 instrumentation data)
9. **2.2** API config hardening
10. **4.1-4.3** Upload/job initiation UX
11. **5** Metadata, tags, automation support

### v3.6.2 - Windows Service

12. **6.1-6.4** Windows installer, service, tray, auto-downloader


### v3.6.2 Feature Enhancement: Transfer Queue (GUI) for Rescale Interlink

## Problem
Today, transfers (upload/download) are “one-at-a-time” from a UX standpoint:
- In the **GUI**, while a file is uploading/downloading, the user can’t conveniently line up the next transfers.
- The user ends up **waiting** to click “Upload” / “Download” again instead of preparing the next work.

## Goal
Add a **Transfer Queue** so users can:
- Queue multiple **uploads and downloads** into a running list
- Keep browsing/selecting files while transfers are active
- Let the tool execute the queue automatically (with controlled concurrency)

## High-Level Behavior

### Core Concept
Introduce a shared **Transfer Manager** that owns a FIFO queue of transfer tasks.

Each task:
- type: `UPLOAD | DOWNLOAD`
- src/dst (local path, remote file/folder id/path)
- metadata (size, encryption mode if relevant, overwrite/skip/merge policy)
- state: `QUEUED | RUNNING | PAUSED | DONE | FAILED | CANCELED`
- progress: bytes, rate, ETA, error message

### Scheduling
- Default: run up to `N` concurrent transfers (reuse existing max-concurrent/thread/resource manager concepts)
- Preserve existing per-file progress reporting, but add **queue-level** status:
  - “3 queued, 1 running, 12 completed, 1 failed”

---

## GUI Requirements (File Browser / Transfers UI)

### UI Additions
Add a **Transfers / Queue panel** (can be a new section in File Browser tab or a small new tab):
- List rows: `[Type] [Name] [Size] [From → To] [Status] [Progress bar] [Rate] [Actions]`

Actions per item:
- `Cancel` (queued or running)
- `Retry` (failed)
- Optional: `Move Up/Down` (nice-to-have)

Global actions:
- `Pause All` / `Resume All`
- `Clear Completed`
- `Clear Failed` (optional)

### Queueing UX
While a transfer is running:
- User can still select files and click:
  - `Upload (Queue)` or `Download (Queue)` (or keep button name the same, but it enqueues if busy)
- The UI should immediately show items in `QUEUED` state.

### Notifications / StatusBar
- StatusBar shows brief updates:
  - “Queued 5 uploads”
  - “Running: upload foo.zip (2.1 MB/s)”
  - “Transfer failed: bar.tar.gz (click to view)”

### Thread Safety / Eventing
- Transfer Manager emits events to existing event bus:
  - `TransferQueued`, `TransferStarted`, `TransferProgress`, `TransferCompleted`, `TransferFailed`, `TransferCanceled`, `QueueStatsUpdated`
- GUI subscribes and updates safely (Fyne-thread-safe patterns already in use).

---

## Non-Goals (keep scope tight)
- Not redesigning encryption/upload/download algorithms
- Not changing remote conflict semantics (reuse existing flags: overwrite/skip/merge/resume where applicable)
- Not building a full “sync” system

---

## Acceptance Criteria
- While a transfer is active in the GUI, the user can add more uploads/downloads and see them appear as `QUEUED`.
- Queue automatically starts the next item when capacity is available.
- User can cancel a queued item; canceling a running item stops it cleanly and marks `CANCELED`.
- Failures do not kill the whole queue by default; failed item is marked `FAILED` and queue continues (unless a “stop on error” option is set).
- Queue state and progress updates are visible in:
  - GUI queue panel + StatusBar
  - CLI `transfers status/list`


---

## Testing Checklist

### v3.6.0 Testing

- [x] All unit tests pass
- [x] E2E tests with S3 backend (upload/download verified)
- [x] E2E tests with Azure backend (upload verified)
- [x] Timing instrumentation verified working
- [x] darwin-arm64 binary built and verified
- [ ] GUI testing on macOS (arm64) - maximize button
- [ ] GUI testing on Windows - maximize button
- [ ] Large file download with timing on Windows

---

## Notes

### Already Completed (from original list):
- Remove delete button - Done in v3.5.0
- Single + PLUR UX/flow - Done
- Generic scan of folders/files to initiate AI job pipeline - Done

### v3.6.0 Decisions:
- Item 1.3 (CLI API client consolidation) DEFERRED - analysis showed it's boilerplate, not architectural
- Focused on localfs abstraction which IS a real unification issue

---

**Document Version:** 2.0
**Last Updated:** December 23, 2025
