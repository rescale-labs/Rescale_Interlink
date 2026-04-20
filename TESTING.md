# Testing Guide - Rescale Interlink

**Last Updated**: April 19, 2026
**Version**: 4.9.4

For comprehensive feature details, see [FEATURE_SUMMARY.md](FEATURE_SUMMARY.md).

---

## Table of Contents

- [Running Tests](#running-tests)
- [Test Coverage](#test-coverage)
- [Manual Testing Procedures](#manual-testing-procedures)
- [GUI Testing](#gui-testing)
- [Historical Testing Summary](#historical-testing-summary)

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

# CLI tests (includes compat mode)
go test -v ./internal/cli/...

# Transfer infrastructure
go test -v ./internal/transfer/...

# PUR integration tests
go test -v ./internal/pur/...

# Watch engine tests
go test -v ./internal/watch/...
```

---

## Test Coverage

### Current Coverage by Area

90 test files across 46 directories. Grouped by functional area:

#### CLI & Commands

| Package | Test Files | Key Coverage |
|---------|-----------|--------------|
| `internal/cli` | 6 | Command parsing, helpers, conflict resolution |
| `internal/cli/compat` | 8 | Compat mode detection, arg normalization, commands, parity |

#### Core Infrastructure

| Package | Test Files | Key Coverage |
|---------|-----------|--------------|
| `internal/core` | 1 | Engine pipeline orchestration |
| `internal/events` | 1 | EventBus pub/sub, ring buffer |
| `internal/config` | 7 | CSV config, API config, jobs CSV, daemon config, platforms |
| `internal/models` | 2 | Job serialization, credential models |
| `internal/validation` | 1 | Path validation |

#### Cloud & Transfer

| Package | Test Files | Key Coverage |
|---------|-----------|--------------|
| `internal/cloud` | 1 | Timing utilities |
| `internal/cloud/credentials` | 1 | Credential management |
| `internal/cloud/providers/s3` | 1 | S3 upload progress reader |
| `internal/cloud/providers/azure` | 1 | Azure client, SAS token lookup |
| `internal/cloud/state` | 1 | Resume state serialization |
| `internal/cloud/transfer` | 1 | Transfer orchestration |
| `internal/cloud/upload` | 1 | Upload flow |
| `internal/transfer` | 4 | Batch executor, queue, speed window, manager |
| `internal/transfer/folder` | 2 | Folder creation, orchestrator |
| `internal/transfer/scan` | 1 | Remote folder scanning |

#### Services & GUI Bindings

| Package | Test Files | Key Coverage |
|---------|-----------|--------------|
| `internal/wailsapp` | 4 | Job bindings, path helpers, version bindings, daemon bindings |
| `internal/services` | 1 | Transfer service |

#### PUR (Parallel Upload and Run)

| Package | Test Files | Key Coverage |
|---------|-----------|--------------|
| `internal/pur/filescan` | 1 | File scanning |
| `internal/pur/parser` | 1 | SGE script parsing |
| `internal/pur/pattern` | 1 | Pattern detection |
| `internal/pur/pipeline` | 1 | Pipeline orchestration |

#### Networking & Rate Limiting

| Package | Test Files | Key Coverage |
|---------|-----------|--------------|
| `internal/http` | 2 | Proxy, retry logic |
| `internal/ratelimit` | 3 | Token bucket, registry, store |
| `internal/ratelimit/coordinator` | 5 | Cross-process coordination |

#### Background Service

| Package | Test Files | Key Coverage |
|---------|-----------|--------------|
| `internal/daemon` | 4 | Daemon lifecycle, monitor, state, transfer tracker |
| `internal/service` | 2 | Windows service, detection |
| `internal/ipc` | 5 | Client/server, messages, pipe, security |

#### Security & Crypto

| Package | Test Files | Key Coverage |
|---------|-----------|--------------|
| `internal/crypto` | 2 | Encryption, streaming encryption |
| `internal/reporting` | 1 | Error classification, redaction, reportability |

#### Platform & Utilities

| Package | Test Files | Key Coverage |
|---------|-----------|--------------|
| `internal/diskspace` | 1 | Cross-platform disk space checking |
| `internal/localfs` | 2 | Directory browser, WalkStream |
| `internal/logging` | 1 | TeeWriter (log → EventBus) |
| `internal/platform` | 1 | Sleep prevention |
| `internal/resources` | 1 | Thread pool, memory management |
| `internal/watch` | 1 | Job watch engine |
| `internal/util/analysis` | 1 | Analysis utilities |
| `internal/util/buffers` | 1 | Buffer pooling |
| `internal/util/glob` | 1 | Glob pattern matching |
| `internal/util/multipart` | 1 | Multipart scan |
| `internal/util/paths` | 1 | Path collision detection |
| `internal/util/sanitize` | 1 | String sanitization |
| `internal/util/tags` | 1 | File tag utilities |

#### Other

| Package | Test Files | Key Coverage |
|---------|-----------|--------------|
| `installer` | 1 | Installer tests |

### Coverage Goals

- Core packages: >80%
- API client: >70%
- Overall: >75%

---

## Manual Testing Procedures

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
./bin/rescale-int folders upload-dir --folder-id $FOLDER_ID --dir /tmp/test_upload -r

# Verify
./bin/rescale-int folders list --folder-id $FOLDER_ID
```

### Compat Mode Testing

```bash
# Verify compat mode activates
./bin/rescale-int --compat --version

# Test via symlink
ln -s ./bin/rescale-int ./rescale-cli
./rescale-cli --version

# Test credential chain
./rescale-cli -p $(cat /path/to/token) status -j JOB_ID

# Test argument normalization
./rescale-cli upload -f file1.txt file2.txt file3.txt  # multi-value -f

# Test exit code convention
./rescale-cli status -j NONEXISTENT; echo "Exit code: $?"  # should be 33
```

### Jobs Watch Testing

```bash
# Single-job watch
./bin/rescale-int jobs watch -j JOB_ID -o ./output -i 30

# Newer-than watch (all jobs after reference)
./bin/rescale-int jobs watch --newer-than REF_JOB_ID -o ./output

# Watch with file filtering
./bin/rescale-int jobs watch -j JOB_ID -o ./output --filter "*.dat" --exclude "debug*"
```

---

## GUI Testing

### Development Mode Testing

```bash
# Install Wails CLI (one-time setup)
go install github.com/wailsapp/wails/v2/cmd/wails@latest

# Install frontend dependencies
cd frontend && npm install && cd ..

# Run in development mode with hot-reload
wails dev
```

### Production Build Testing

```bash
# macOS (Apple Silicon)
CGO_LDFLAGS="-framework UniformTypeIdentifiers" wails build -platform darwin/arm64

# FIPS-compliant production build
GOFIPS140=latest CGO_LDFLAGS="-framework UniformTypeIdentifiers" wails build -platform darwin/arm64

# Test production build
open build/bin/rescale-int.app
```

### Frontend Unit Tests

```bash
cd frontend

# Build verification
npm run build

# Type checking
npm run type-check  # or: npx tsc --noEmit
```

### Backend Binding Tests

```bash
# Test wailsapp bindings compile correctly
go build ./internal/wailsapp/...

# Test event system
go test -v ./internal/events/...

# After changing Go bindings, regenerate TypeScript
wails generate module
```

### GUI Functional Test Checklist

**Validation Points**:
- GUI launches without errors
- All tabs render correctly (Setup, SingleJob, PUR, FileBrowser, Transfers, Activity Logs)
- Real-time event updates via event bridge
- No UI freezes or deadlocks
- Error boundaries catch and display component errors
- Clean shutdown

**Tab-Specific Tests:**

1. **Setup Tab**
   - Configure API settings and test connection
   - Verify Advanced Settings collapsible contains "Logging Settings" card
   - Auto-download daemon enable/disable and status

2. **PUR Tab**
   - Load/Save job settings (CSV, JSON, SGE formats)
   - Pipeline Settings (workers, tar options)
   - Scan to Create Jobs workflow
   - Monitor active run / Prepare new run choice screen
   - Queue run when another run is active

3. **SingleJob Tab**
   - Three input modes: directory, local files, remote files
   - Tar options visible only in directory mode
   - Submit / Queue Job workflow

4. **File Browser Tab**
   - Two-pane local/remote navigation
   - Upload and download operations
   - Delete operations with confirmation

5. **Transfers Tab**
   - Batch grouping with collapsible rows
   - Progress bars, speed, and ETA display
   - Cancel and retry operations
   - Disk space error banner
   - Daemon auto-download rows (read-only)

6. **Activity Tab**
   - Log display with level filtering
   - Run history with expandable job tables

**Deadlock Stress Test**:
```bash
# Launch GUI, load CSV with 50+ jobs, click Run
# Table should update smoothly without freezing
# Expected: 60+ events/second processed without deadlocks
```

---

## Troubleshooting Tests

### Unit Tests Fail

```bash
# Clean and retry
go clean -cache
go mod tidy
go test ./...
```

### API Tests Fail

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

**Memory Profiling**:
```bash
go test -memprofile=mem.prof ./internal/core/
go tool pprof mem.prof
```

**CPU Profiling**:
```bash
go test -cpuprofile=cpu.prof ./internal/events/
go tool pprof cpu.prof
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

### GUI Test Checklist

- [ ] Feature works in relevant tab(s)
- [ ] No UI freezes or deadlocks
- [ ] Progress indicators update correctly
- [ ] Error messages display properly
- [ ] Clean shutdown after operations

---

## Continuous Integration

GitHub Actions workflows run on tag push for release builds:
- Windows build (portable + MSI, Azure Trusted Signing)
- macOS build (Apple Silicon, Developer ID signed + notarized)
- Linux build via Rescale HPC job

**Future planned**: Automated test runs on PR creation, cross-platform build validation, performance regression detection.

---

## Historical Testing Summary

### Early Development (2025)

- **Round 1** (January 2025): 10 major bugs found and fixed (API endpoints, folder API separation, connection reuse)
- **Round 2** (January 2025): 0 new bugs, all Round 1 fixes validated, 60+ unit tests passing
- **v2.3.0** (November 2025): 3 critical bug fixes validated (resume/PKCS7 padding, decryption progress, progress bar corruption across 17 files)
- **v2.0.1** (November 2025): Folder caching (99.8% API call reduction), rate limiting, multi-progress bars, disk space checking all validated

### v4.x Series (2026)

- **v4.7.3**: 15/15 E2E tests passed across S3 and Azure backends (file operations, job operations, hardware/software listing)
- **v4.6.8**: 8 automation serialization unit tests; E2E validation for single/multiple/no automations
- **v4.8.x**: Transfer system convergence validated — `RunBatch`/`RunBatchFromChannel` abstraction, conflict resolver, adaptive concurrency, FileInfo enrichment

### Current State (v4.9.4)

- **Unit Tests**: 102 test files across 46 packages (v4.9.4 adds Windows-tagged token-ACL tests, catalog-wide IPC authorization tests, session-scoped platform test, and daemon state / scan-summary tests)
- **Coverage**: All core packages tested
- **Known Bugs**: 0
- **Quality Gates**:
  - All unit tests must pass
  - No race conditions detected
  - Coverage >75% for new code
  - Manual GUI smoke test passes
