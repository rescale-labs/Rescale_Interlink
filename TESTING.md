# Testing Guide - Rescale Interlink

**Last Updated**: February 22, 2026
**Version**: 4.7.3

For comprehensive feature details, see [FEATURE_SUMMARY.md](FEATURE_SUMMARY.md).

---

## Table of Contents

- [Running Tests](#running-tests)
- [Test Coverage](#test-coverage)
- [v2.3.0 Regression Tests](#v230-regression-tests)
- [Manual Testing Procedures](#manual-testing-procedures)
- [Test Results History](#test-results-history)
- [Known Test Results](#known-test-results)

---

## Running Tests

### Quick Test Suite

```bash
# Run all unit tests
go test ./...

# Run with verbose output
go test -v ./...

# Run with coverage
go test -cover ./...
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out

# Run with race detection
go test -race ./...
```

### Package-Specific Tests

```bash
# Core engine tests
go test -v ./internal/core/...

# Event system tests
go test -v ./internal/events/...

# CLI tests
go test -v ./internal/cli/...

# PUR integration tests
go test -v ./internal/pur/...
```

### Integration Tests

```bash
# File operations test
./test_file_commands.sh

# Folder operations test
./test_folder_commands.sh

# Job operations test
./test_job_commands.sh
```

---

## Test Coverage

### Current Coverage by Package

| Package | Tests | Coverage | Status |
|---------|-------|----------|--------|
| `internal/core` | 9 | ~75% | Good |
| `internal/events` | 8 | ~90% | Excellent |
| `internal/cli` | 15 | ~70% | Good |
| `internal/pur/config` | 9 | ~85% | Excellent |
| `internal/pur/pattern` | 4 | ~80% | Good |
| `internal/pur/sanitize` | 4 (16 sub-tests) | ~90% | Excellent |
| `internal/http` | 4 | ~70% | Good |
| `internal/wailsapp` | 17 | ~55% | Growing (v4.7.3: +3 tests for history/sanitization) |
| `internal/cloud/transfer` | 5 | ~60% | Good |
| **Total** | **70+** | **~80%** | **Good** |

### GUI Testing (Wails v2 + React/TypeScript)

**Status**: Manual testing with development mode hot-reload

**v4.0.4 Note**: GUI uses Wails v2 framework with React/TypeScript frontend.
The Go backend (`internal/wailsapp/`) exposes bindings that are auto-generated
into `frontend/wailsjs/go/` for TypeScript consumption.

#### Development Mode Testing

```bash
# Install Wails CLI (one-time setup)
go install github.com/wailsapp/wails/v2/cmd/wails@latest

# Install frontend dependencies
cd frontend && npm install && cd ..

# Run in development mode with hot-reload
wails dev

# This provides:
# - Hot-reload for frontend changes
# - Automatic Go rebuilds on backend changes
# - DevTools access (F12)
# - Console logging visible in terminal
```

#### Production Build Testing

```bash
# macOS (Apple Silicon)
CGO_LDFLAGS="-framework UniformTypeIdentifiers" wails build -platform darwin/arm64

# FIPS-compliant production build
GOFIPS140=latest CGO_LDFLAGS="-framework UniformTypeIdentifiers" wails build -platform darwin/arm64

# Test production build
open build/bin/rescale-int.app

# Or via CLI
./build/bin/rescale-int.app/Contents/MacOS/rescale-int
```

#### Frontend Unit Tests

```bash
cd frontend

# Run React component tests (if present)
npm test

# Build verification
npm run build

# Type checking
npm run type-check  # or: npx tsc --noEmit
```

#### Backend Binding Tests

```bash
# Test wailsapp bindings compile correctly
go build ./internal/wailsapp/...

# Test event system
go test -v ./internal/events/...

# After changing Go bindings, regenerate TypeScript
wails generate module
```

**Validation Points**:
- GUI launches without errors
- All tabs render correctly (Setup, SingleJob, PUR, FileBrowser, Transfers, Activity Logs)
- Real-time event updates via event bridge
- No UI freezes or deadlocks
- Error boundaries catch and display component errors (v4.0.4)
- Clean shutdown

### Coverage Goals

- Core packages: >80%
- API client: >70%
- Overall: >75%

---

## v2.3.0 Regression Tests

### Bug Fix #1: Resume Logic (PKCS7 Padding)

**Test Scenario**: Download large file, interrupt, resume

**Steps**:
```bash
# 1. Start downloading a large file (>100MB)
rescale-int files download <large-file-id> -o test_file.dat

# 2. Interrupt with Ctrl+C after partial download

# 3. Resume download (should detect .encrypted file and resume)
rescale-int files download <large-file-id> -o test_file.dat
```

**Expected Behavior**:
- Resume should detect existing `.encrypted` file
- Size validation should accept range: `expectedSize+1` to `expectedSize+16` bytes
- Should NOT re-download if file is complete (within padding range)
- On size mismatch, error message shows expected range: "expected 1000001-1000016, got 999999"

**Verification**:
```bash
# Check that resumed download didn't start from 0
# File should complete successfully without re-downloading
```

**Source**: `internal/cli/download_helper.go:163-186`

### Bug Fix #2: Decryption Progress Feedback

**Test Scenario**: Download and decrypt a large file (>1GB)

**Steps**:
```bash
# Download large encrypted file
rescale-int files download <large-file-id> -o large_file.dat
```

**Expected Behavior**:
- After download completes, before decryption starts, should see message:
  ```
  Decrypting large_file.dat (this may take several minutes for large files)...
  ```
- Message appears for both S3 and Azure backends
- Prevents user confusion during long decryption (40+ minutes for 60GB files)

**Verification**:
```bash
# Watch for "Decrypting..." message after download progress bar completes
# Decryption should complete successfully
```

**Source**: `internal/cloud/download/s3_concurrent.go:458`, `azure_concurrent.go:483`

### Bug Fix #3: Progress Bar Corruption

**Test Scenario**: Upload multiple files with progress bars

**Steps**:
```bash
# Create test files
for i in {1..5}; do
    dd if=/dev/urandom of=/tmp/test_$i.dat bs=10M count=1 2>/dev/null
done

# Upload with progress bars
rescale-int files upload /tmp/test_1.dat /tmp/test_2.dat /tmp/test_3.dat
```

**Expected Behavior**:
- Clean progress bars for each file
- No "ghost bars" or corrupted output
- All status messages appear cleanly
- No overlapping progress bars
- Clean terminal output throughout

**Before (broken)**:
```
[1/3] Uploading test_1.dat...
Uploading file...    ← This line corrupts the progress bar
░░░░░░░░░░░░░░░░░░
[1/3] Uploading test_1.dat...   ← Ghost bar appears
```

**After (fixed)**:
```
[1/3] Uploading test_1.dat...
████████████████░░░░ 80% | 8.0 MB / 10.0 MB | 2.5 MB/s | ETA: 1s
```

**Verification**:
- All output routed through mpb io.Writer
- No direct `fmt.Printf` calls bypassing mpb

**Source**: 17 files updated across `internal/cli/` and `internal/pur/`

### Regression Test Checklist

Run these tests to verify v2.3.0 fixes:

- [ ] Resume download with complete encrypted file (should skip re-download)
- [ ] Resume download with partial encrypted file (should continue)
- [ ] Resume validation error shows size range (not exact match)
- [ ] Decryption message appears for large files (>100MB)
- [ ] Multiple file uploads show clean progress bars
- [ ] No progress bar corruption with concurrent operations
- [ ] Both S3 and Azure backends show decryption messages
- [ ] Streaming decryption works for large files (60GB+) without memory issues

---

## Manual Testing Procedures

### Test Script Overview

Three test scripts validate CLI interface and functionality:

1. **test_file_commands.sh** - File operations
2. **test_folder_commands.sh** - Folder operations
3. **test_job_commands.sh** - Job control operations

### File Operations Testing

**Script**: `test_file_commands.sh`

**Tests**:
1. Help commands functionality
2. Upload command validation (requires --filepath)
3. Download command validation (requires --fileid)
4. Multiple file path handling
5. API key requirement enforcement
6. File existence checking
7. Delete command structure

**Run**:
```bash
chmod +x test_file_commands.sh
./test_file_commands.sh
```

**Expected Output**:
```
=== Testing File Commands CLI Interface ===
Test 1: Help commands
✓ All help commands work

Test 2: Upload command validation
✓ Correctly requires --filepath argument
...
=== All CLI Interface Tests Passed! ===
```

### Folder Operations Testing

**Script**: `test_folder_commands.sh`

**Tests**:
1. Help commands functionality
2. Create command validation (requires --name)
3. Upload command validation (requires --folder-id)
4. File/directory requirement validation
5. Performance feature documentation
6. Concurrent upload design (semaphore pattern)
7. Connection reuse implementation
8. Progress bar integration
9. Error handling for batch operations
10. Goroutine synchronization

**Critical Validations**:
- Connection reuse documented and implemented
- 5-10x speedup claim validated
- Concurrent uploads (max 3) with semaphore
- Single API client for all operations

**Run**:
```bash
chmod +x test_folder_commands.sh
./test_folder_commands.sh
```

### Job Operations Testing

**Script**: `test_job_commands.sh`

**Tests**:
1. Help commands functionality
2. Get command validation (requires --job-id)
3. Stop command validation (requires --job-id)
4. Tail command validation (requires --job-id)
5. ListFiles command validation
6. Download command validation (requires job-id and file-id)
7. Tail interval configuration (default 10s)
8. List limit option
9. Stop confirmation feature
10. Download output path option
11. Real-time monitoring documentation
12. API client methods verification
13. Model types verification

**Run**:
```bash
chmod +x test_job_commands.sh
./test_job_commands.sh
```

### Live API Testing (Requires Credentials)

**Prerequisites**:
```bash
export RESCALE_API_KEY=$(cat /path/to/rescale_token.txt)
```

**Basic Upload/Download Test**:
```bash
# Create test file
echo "Test content" > /tmp/test.txt

# Upload
./bin/rescale-int files upload --filepath /tmp/test.txt

# Note the file ID from output, then download
./bin/rescale-int files download --fileid <FILE_ID> --outdir /tmp

# Verify
cat /tmp/test.txt
```

**Folder Upload Test**:
```bash
# Create test structure
mkdir -p /tmp/test_upload/subdir
echo "file1" > /tmp/test_upload/file1.txt
echo "file2" > /tmp/test_upload/subdir/file2.txt

# Create folder
FOLDER_ID=$(./bin/rescale-int folders create --name "Test_$(date +%s)" | grep -oE '[a-zA-Z0-9]{6}')

# Upload directory
./bin/rescale-int folders upload --folder-id $FOLDER_ID --dir /tmp/test_upload -r

# Verify
./bin/rescale-int folders list --folder-id $FOLDER_ID
```

**Performance Test (Connection Reuse)**:
```bash
# Create multiple test files
for i in {1..5}; do
    dd if=/dev/urandom of=/tmp/perf_test_$i.dat bs=100k count=1 2>/dev/null
done

# Test batch upload with connection reuse
time ./bin/rescale-int folders upload --folder-id $FOLDER_ID \
    --filepath /tmp/perf_test_1.dat \
    --filepath /tmp/perf_test_2.dat \
    --filepath /tmp/perf_test_3.dat \
    --filepath /tmp/perf_test_4.dat \
    --filepath /tmp/perf_test_5.dat

# Should see:
# - "Using connection reuse for optimal performance" message
# - "Performance note: Connection reuse provided ~5-10x speedup" message
# - Concurrent upload progress
```

### GUI Testing (Manual)

**Launch Test**:
```bash
./bin/rescale-int --gui &
GUI_PID=$!
sleep 2
ps -p $GUI_PID  # Should show running process
kill $GUI_PID   # Should terminate cleanly
```

**Functional Tests**:
1. **Setup Tab**
   - Configure API settings
   - Test connection
   - Apply changes
   - Verify Advanced Settings collapsible contains "Logging Settings" card (v4.7.2, was "Advanced Settings")
   - Verify Advanced Settings no longer shows Workers, Tar Options, or Directory Scan (v4.7.1)

2. **PUR Tab** (v4.7.2 Load/Save + v4.7.1 Pipeline Settings)
   - "Parallel Upload and Run" subtitle visible in progress bar header (v4.7.2)
   - "Configure Base Job Settings" heading in createNew state (v4.7.2)
   - "Load Existing Base Job Settings" dropdown with CSV, JSON, SGE options (v4.7.2)
   - Load CSV → first job becomes template, transitions to templateReady
   - Load JSON → first job becomes template, transitions to templateReady
   - Load SGE → loads as template, transitions to templateReady
   - Cancel file dialog → no error
   - Load invalid file → error banner appears
   - "Save As..." dropdown in templateReady state with CSV, JSON, SGE options (v4.7.2)
   - Save template as CSV/JSON/SGE → valid file written
   - Cancel save dialog → no error
   - "Scan to Create Jobs" heading and button text (v4.7.2, was "Scan for Jobs" / "Scan Directories")
   - Progress bar step label shows "Scan to Create Jobs" (v4.7.2, was "Scan Directories")
   - Pipeline Settings visible in scan step (workers + tar options)
   - Pipeline Settings visible in jobs-validated step (CSV-loaded workflow)
   - Change worker count → persists across app restart
   - Change tar options → persists across app restart
   - Scan prefix and validation pattern persist to config.csv on change
   - Compression dropdown shows "gzip"/"none" consistently
   - "Export CSV" button in jobsValidated state still present and functional

3. **SingleJob Tab** (v4.7.2 Label + v4.7.1 Tar Options)
   - "Load Existing Job Settings" dropdown label (v4.7.2, was "Load From...")
   - Dropdown still works with CSV, JSON, SGE options
   - Directory mode shows tar options (exclude/include/compression/flatten)
   - Local-files and remote-files modes do NOT show tar options
   - Tar option changes persist to config.csv

4. **Transfers Tab** (v4.7.1 Disk Space UX)
   - Download to nearly-full disk → amber banner with available/needed values
   - Individual rows show "No disk space" (not truncated raw error)
   - Hover tooltip shows full error message on all transfer rows
   - Click "Clear Completed" → banner persists (explicit dismiss only)
   - Click banner X → banner dismissed
   - New disk space error after dismiss → banner re-appears
   - Non-disk-space errors show raw error (truncated with tooltip)

5. **PUR Tab - Run Session Persistence (v4.7.3)**
   - Start a PUR run → navigate away → return → see choice screen with "Monitor Active Run" / "Prepare New Run"
   - Click "Monitor Active Run" → see live dashboard with progress
   - Navigate away → return → goes directly to monitor view (remembered)
   - Click "Prepare New Run" → see config with monitoring banner at top
   - Navigate away → return → goes directly to config view (remembered)
   - Let a PUR run complete → navigate to PUR tab → see completed results summary
   - Click "Start New Run" → fresh workflow
   - Start PUR run → click Cancel → verify status shows 'cancelled'
   - Start PUR run → "Prepare New Run" → configure → "Queue Run" button visible
   - Queue a run → verify queue status banner shows "queued"
   - First run completes → queued run auto-starts
   - **C10 monitor view fixes:**
   - Start PUR via workflow → during run, header shows "Pipeline Running", cancel button visible
   - Let pipeline complete → header changes to "Pipeline Complete", cancel disappears, "View Results" appears
   - Click "View Results" → navigates to completed results view
   - During active run, click "Prepare New Run" (executing view) → lands in config form (source path selection), monitoring banner visible
   - After completion, click "Prepare New Run" → lands in config form (not stuck in executing view)
   - From choice screen, click "Prepare New Run" → lands in config form with monitoring banner
   - From monitor view, click "Prepare New Run" → lands in config form

6. **SingleJob Tab - Run Persistence (v4.7.3)**
   - Configure job → submit → navigate away → return → see job status
   - Configure another job → "Queue Job" button visible when a run is active
   - Form state persists across tab navigation (software, hardware, command, etc.)
   - **C11 executing view fixes:**
   - Submit a single job → executing view shows "Submitting Job..." with cancel button and spinner
   - Let job complete → header changes to "Job Complete", spinner replaced with green checkmark, "Submit Another Job" button appears
   - Click "Submit Another Job" → returns to initial configuration form
   - During active job, cancel → shows failed state with "Job Cancelled" header
   - Cancel race condition: if job completes just before cancel, UI shows "Job Complete" (not "Job Failed")

7. **Activity Tab (v4.7.3 Run History)**
   - Complete a run → Activity tab shows "Run History" section with completed run
   - Click run → see expanded job table
   - Verify logs still appear, search/filter works

8. **Footer Indicator (v4.7.3)**
   - Start any run → footer shows active run type and progress count
   - Navigate between tabs → footer persists

9. **App Restart Recovery (v4.7.3)**
   - Start run → quit app → reopen
   - Verify completed runs recovered from state files (or clean state if lost)

10. **Regression**
   - PUR pipeline execution end-to-end still works
   - SingleJob submission end-to-end still works
   - File Browser unaffected
   - Transfers tab unaffected
   - Activity logs still show entries with correct `jobName` and `stage` fields

**Deadlock Test** (Stress Test):
```bash
# Launch GUI
./bin/rescale-int --gui

# Load CSV with 50+ jobs
# Click Run
# Observe: Table should update smoothly without freezing
# Expected: 60+ events/second processed without deadlocks
```

---

## Test Results History

### Round 1 Testing (January 2025)

**Status**: 10 major bugs found and fixed

**Bugs Fixed**:
1. Missing persistent flags (--api-key, --config, etc.)
2. Files list/delete wrong API paths
3. CreateFolder using wrong endpoint (required encryption key)
4. ListFolderContents wrong endpoint
5. ListFolderContents response parsing incorrect
6. MoveFileToFolder missing API prefix
7. DeleteFolder using files endpoint
8. Command shortcuts not working (delegation issues)
9. Upload shortcut args not passed correctly
10. Files uploaded to wrong folder (not using currentFolderId)

**Key Fixes**:
- All API endpoints corrected to use `/api/v3/` prefix
- Folder API separated from Files API
- Connection reuse implemented for multi-file uploads
- File registration uses `currentFolderId` (not move afterwards)

### Round 2 Testing (January 2025)

**Status**: 0 new bugs found, all Round 1 fixes validated

**Tests Completed**:
- GUI deadlock static analysis: PASSED
- Regression tests (10 bugs): ALL PASSED
- GUI launch/close: PASSED
- GUI unit tests (5 new tests): ALL PASSED
- Performance validation: PASSED (16s for 5x100KB files)
- Error handling: ALL PASSED (6 scenarios)
- Existing unit tests: ALL PASSED (60+ tests)

**Performance Results**:
- Connection reuse working as designed
- Messages displayed to user correctly
- Files correctly placed in target folders
- No deadlocks under load (60+ events/sec)

**GUI Deadlock Prevention Validated**:
```go
// Safe pattern confirmed in all GUI code:
jt.jobsLock.Lock()
// ... update data ...
jt.jobsLock.Unlock()  // ✓ Released BEFORE refresh

jt.table.Refresh()  // ✓ Called WITHOUT lock
```

### Recent Testing (v2.0.1 - November 12, 2025)

**Phase 7: Folder Caching**
- Tested with 500 repeated folder lookups
- Before: 500 API calls
- After: 1 API call + 499 cache hits
- Result: 99.8% reduction confirmed

**Phase 8: Rate Limiting**
- Tested with high-frequency operations
- General operations: 8.3 calls/sec average maintained
- Job submissions: 0.083 calls/sec enforced
- 429 responses: Exponential backoff working
- Retry-After headers: Respected correctly

**Phase 9: Multi-Progress Bars**
- Tested with 10 simultaneous uploads
- All progress bars displayed correctly
- No overlap or rendering issues
- Smooth updates without flicker
- Automatic cleanup on completion

**Phase 10: Disk Space Checking**
- Tested on macOS ARM64: PASSED
- Tested on Linux AMD64: PASSED
- Tested on Windows AMD64: PASSED
- 15% safety margin prevents failures
- Error messages clear and actionable
- v4.7.1: Amber error banner in Transfers tab for disk space failures with available/needed info
- v4.7.1: Short "No disk space" labels with full hover tooltip

### v4.7.3 E2E Testing (February 22, 2026)

**Builds**:
- CLI binary (FIPS 140-3): `make build-darwin-arm64` — PASSED
- Wails GUI binary (FIPS 140-3): `GOFIPS140=latest wails build -platform darwin/arm64` — PASSED
- TypeScript compilation: `cd frontend && npm run build` (487 modules, 0 errors) — PASSED
- Go unit tests: `go test ./...` — ALL PASSED (all packages including 3 new wailsapp tests)

**S3 Backend (AWS) E2E Tests**:

| Test | Result | Details |
|------|--------|---------|
| `--version` | PASS | `rescale-int version v4.7.3 (2026-02-22) [FIPS 140-3]` |
| `config test` | PASS | Connection verified, user: user@example.com |
| `jobs list --limit 3` | PASS | 3 of 802 jobs listed |
| `hardware list --search emerald` | PASS | 5 hardware types found |
| `software list --search openfoam` | PASS | Multiple OpenFOAM variants listed |
| `files list --limit 3` | PASS | Files listed with sizes and dates |
| `files upload` (test file) | PASS | File uploaded, ID: srQHGo |
| `folders list` | PASS | My Library contents listed |

**Azure Backend E2E Tests**:

| Test | Result | Details |
|------|--------|---------|
| `config test` | PASS | Connection verified, user: user+test@example.com |
| `jobs list --limit 3` | PASS | 3 of 48 jobs listed |
| `hardware list --search emerald` | PASS | 3 hardware types found |
| `software list --search openfoam` | PASS | Multiple OpenFOAM variants listed |
| `files list --limit 3` | PASS | Files listed with sizes and dates |
| `files upload` (test file) | PASS | File uploaded, ID: ArQHGo |
| `folders list` | PASS | My Library contents listed |

**Summary**: 15/15 E2E tests PASSED across both S3 and Azure backends. No regressions detected.

---

## Known Test Results

### Performance Benchmarks

**Event Bus**:
- Publish to receive latency: <1ms (p50), <5ms (p99)
- Sustained throughput: 1000 events/second
- No memory leaks after extended operation

**UI Updates**:
- Table refresh: <50ms for 100 rows
- Log append: <10ms per entry
- Search filter: <100ms for 1000 entries

**Memory Usage**:
- Baseline: 50 MB
- Per job: +100 KB
- 1000 jobs: ~150 MB total

**CPU Usage**:
- Idle: <1%
- During upload: 5-10% (compression)
- During monitoring: <2%

### Folder Operations

**Without Caching**:
- 500 folder lookups = 500 API calls
- Total time: ~250 seconds (0.5s per call)

**With Caching**:
- 500 folder lookups = 1 API call + 499 cache hits
- Total time: ~0.5 seconds
- Speedup: 500x for cached operations

### Multi-File Upload Performance

**Test Setup**: 420 files × 3MB each = ~1.2GB total

**Sequential (no connection reuse)**:
- Time: ~36 minutes
- Each file: New auth + upload

**Concurrent (maxConcurrent=5)**:
- Time: ~8 minutes
- Speedup: 4.5x faster

**Concurrent (maxConcurrent=10)**:
- Time: ~4 minutes
- Speedup: 9x faster

### Rate Limiting Effectiveness

**Test**: 1000 rapid API calls

**Without Rate Limiting**:
- 429 errors: 37% of requests
- Retries: 600+ attempts
- Total time: ~5 minutes (due to retries)

**With Rate Limiting**:
- 429 errors: 0%
- Retries: 0
- Total time: ~2 minutes (predictable pacing)
- Efficiency: 60% time reduction, 0% errors

---

## Troubleshooting Tests

### Test Failures

**Unit Tests Fail**:
```bash
# Clean and retry
go clean -cache
go mod tidy
go test ./...
```

**Integration Tests Fail**:
```bash
# Check binary exists
ls -la bin/rescale-int

# Rebuild
go build -o bin/rescale-int ./cmd/rescale-int

# Rerun
./test_file_commands.sh
```

**API Tests Fail**:
```bash
# Verify API key
echo $RESCALE_API_KEY

# Test connection
./bin/rescale-int config test

# Check logs
./bin/rescale-int files list --verbose
```

### Common Issues

**Race Detector Warnings**:
- Check for missing mutex locks
- Verify goroutine synchronization
- Review channel usage patterns

**Memory Leaks**:
```bash
# Profile memory
go test -memprofile=mem.prof ./internal/core/
go tool pprof mem.prof

# Check for:
# - Unclosed channels
# - Goroutine leaks
# - Large slice growth
```

**Slow Tests**:
```bash
# Profile CPU
go test -cpuprofile=cpu.prof ./internal/events/
go tool pprof cpu.prof

# Identify hot paths
# Optimize bottlenecks
```

---

## Adding New Tests

### Unit Test Template

```go
package mypackage

import "testing"

func TestMyFeature(t *testing.T) {
    // Setup
    input := "test data"

    // Execute
    result := MyFunction(input)

    // Verify
    if result != expected {
        t.Errorf("Expected %v, got %v", expected, result)
    }
}
```

### Integration Test Template

```bash
#!/bin/bash
set -e

echo "=== Testing My Feature ==="

# Test setup
mkdir -p /tmp/test
echo "data" > /tmp/test/file.txt

# Test execution
./bin/rescale-int mycommand --arg /tmp/test/file.txt

# Verification
if [ -f /tmp/test/output.txt ]; then
    echo "✓ Test passed"
else
    echo "✗ Test failed"
    exit 1
fi

# Cleanup
rm -rf /tmp/test
```

### GUI Test Checklist

- [ ] Feature works in Setup tab
- [ ] Feature works in Jobs tab
- [ ] Feature works in Activity tab
- [ ] No UI freezes or deadlocks
- [ ] Progress indicators update correctly
- [ ] Error messages display properly
- [ ] Clean shutdown after operations

---

## Continuous Integration (Future)

**Planned**:
- Automated test runs on PR creation
- Cross-platform build validation
- Performance regression detection
- Coverage reporting
- Automated releases

**Tools Considered**:
- GitHub Actions for CI/CD
- CodeCov for coverage reporting
- Benchmark tracking over time

---

## Test Metrics Summary

**Current State** (v3.2.0 - November 30, 2025):
- **Unit Tests**: 60+ passing
- **Integration Tests**: 3 scripts, all passing
- **GUI Tests**: Manual validation, no issues
- **Coverage**: ~80% overall
- **Known Bugs**: 0
- **Regression**: All 10 Round 1 bugs still fixed
- **v3.2.0 Changes**: JSON job template support, SearchableSelect fix, Fyne thread safety fix, Hardware scan UX improvements, dialog sizing improvements

**Quality Gates**:
- All unit tests must pass
- No race conditions detected
- Coverage >75% for new code
- Manual GUI smoke test passes
- Performance benchmarks met

---

### v2.3.0 Testing (November 17, 2025)

**Status**: All 3 critical bug fixes validated

**Bugs Fixed**:
1. **Resume Logic** - PKCS7 padding (1-16 bytes) handled correctly
2. **Decryption Progress** - Message added before long decryption operations
3. **Progress Bar Corruption** - All output routed through mpb io.Writer (17 files)

**Tests Completed**:
- Resume logic with complete encrypted files: PASSED
- Resume logic with partial encrypted files: PASSED
- Size validation shows range on mismatch: PASSED
- Decryption message for large files: PASSED
- Progress bar corruption fix: PASSED
- S3 backend decryption message: PASSED
- Azure backend decryption message: PASSED
- Streaming decryption for 60GB+ files: PASSED (no memory exhaustion)

**Regression Tests**: All previous tests still pass

---

### Automation Testing (v4.6.8)

**Unit Tests** (`go test ./internal/models/...`):
- `TestJobAutomationRequest_Serialization` — single automation normalizes and serializes `environmentVariables:{}`
- `TestJobAutomationRequest_SerializationMultiple` — multiple automations each get `environmentVariables:{}`
- `TestNormalizeAutomations_NilMap` — nil map becomes empty map
- `TestNormalizeAutomations_EmptyMap` — empty map stays empty
- `TestNormalizeAutomations_PopulatedMap` — populated map preserved
- `TestNormalizeAutomations_NoAutomations` — no automations does not panic
- `TestJobAutomationRequest_NilEnvVarsSerializesToNull` — raw nil without normalization produces `null`
- `TestJobAutomationRequest_NestedAutomationFormat` — verifies nested `{"automation":{"id":"..."}}` format

**E2E Testing** (requires Rescale API key):
1. **Single automation**: Create and submit a job with 1 automation via GUI or CLI → verify HTTP 201
2. **Multiple automations**: Create and submit a job with 2 automations → verify HTTP 201
3. **No automations**: Create and submit a job without automations → verify still works (regression check)
4. **Error visibility**: If job creation fails (bad analysis code, etc.), verify actual API error message is shown (not generic "giving up after N attempt(s)")

---

**Last Updated**: February 22, 2026
**Version**: 4.7.3
**Status**: All tests passing, code quality improvements (North Star alignment)
