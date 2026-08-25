# Testing Guide - Rescale Interlink

**Last Updated**: August 12, 2026
**Version**: 4.9.9

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
# Run the whole suite the way CI does: FIPS module + fips build tag
make test

# Same, with a coverage profile and HTML report
make test-coverage
```

`make test` expands to `GOFIPS140=certified go test -tags fips -v ./...`. Use it rather
than a bare `go test ./...` — a handful of files are behind the `fips` build tag
(`internal/config/proxy_features_fips_test.go`,
`internal/config/proxy_features_nonfips_test.go`,
`internal/http/ntlm_transport_fips_test.go`), so an untagged run silently exercises the
non-FIPS half only.

```bash
# Plain runs (skip the fips-tagged files)
go test ./...
go test -v ./...

# Coverage
go test -cover ./...
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out

# Race detection
GOFIPS140=certified go test -tags fips -race ./...
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

125 Go test files (124 under `internal/`, one under `installer/`) across 49 packages,
plus 7 frontend vitest files. Grouped by functional area:

#### CLI & Commands

| Package | Test Files | Key Coverage |
|---------|-----------|--------------|
| `internal/cli` | 8 | Command parsing, flag aliases, config commands, daemon commands, job-file decode, conflict resolution, folder-upload abort/failure paths, shortcut concurrency |
| `internal/cli/compat` | 8 | Compat mode detection, arg normalization, commands, parity |

#### Core Infrastructure

| Package | Test Files | Key Coverage |
|---------|-----------|--------------|
| `internal/api` | 3 | Client, retry policy and budget, pagination |
| `internal/core` | 2 | Engine pipeline orchestration, upload-progress reporting |
| `internal/events` | 1 | EventBus pub/sub, ring buffer |
| `internal/config` | 14 | CSV config, API config, jobs CSV, daemon config, platforms (incl. internal- and production-tagged variants), proxy features (FIPS / non-FIPS), token ACL on Windows |
| `internal/models` | 1 | Job serialization, including the SSH access fields |
| `internal/pathutil` | 2 | Path resolution |
| `internal/validation` | 1 | Path validation |

#### Cloud & Transfer

| Package | Test Files | Key Coverage |
|---------|-----------|--------------|
| `internal/cloud` | 2 | Timing utilities, retry notices |
| `internal/cloud/credentials` | 1 | Credential management |
| `internal/cloud/providers/s3` | 2 | S3 upload progress reader, provider behavior |
| `internal/cloud/providers/azure` | 1 | Azure client, SAS token lookup |
| `internal/cloud/state` | 1 | Resume state serialization |
| `internal/cloud/storage` | 1 | Disk-full and quota error classification |
| `internal/cloud/transfer` | 1 | Transfer orchestration |
| `internal/cloud/upload` | 1 | Upload flow |
| `internal/transfer` | 4 | Batch executor, queue (incl. paginated batch rows), speed window, manager |
| `internal/transfer/folder` | 2 | Folder creation, orchestrator |
| `internal/transfer/scan` | 1 | Remote folder scanning |

#### Services & GUI Bindings

| Package | Test Files | Key Coverage |
|---------|-----------|--------------|
| `internal/wailsapp` | 15 | Job bindings, job-status bindings, path helpers, version bindings, daemon bindings, config bindings, API key source bindings, progress + failure-path tests |
| `internal/services` | 1 | Transfer service |

#### PUR (Parallel Upload and Run)

| Package | Test Files | Key Coverage |
|---------|-----------|--------------|
| `internal/pur/filescan` | 1 | File scanning |
| `internal/pur/parser` | 1 | SGE script parsing, SSH directive round-trip |
| `internal/pur/pattern` | 1 | Pattern detection |
| `internal/pur/pipeline` | 2 | Pipeline orchestration, failed-job accounting, JobSpec → JobRequest mapping |

#### Networking & Rate Limiting

| Package | Test Files | Key Coverage |
|---------|-----------|--------------|
| `internal/http` | 3 | Proxy, retry logic and elapsed budget, NTLM transport under FIPS |
| `internal/ratelimit` | 3 | Token bucket, registry, store (incl. degraded-mode notices) |
| `internal/ratelimit/coordinator` | 5 | Cross-process coordination |

#### Background Service

| Package | Test Files | Key Coverage |
|---------|-----------|--------------|
| `internal/daemon` | 6 | Daemon lifecycle, monitor, state pruning, transfer tracker, status snapshot errors |
| `internal/service` | 4 | Windows service, detection, install/uninstall flows |
| `internal/ipc` | 7 | Client/server, messages, pipe, security, user-scope catalog tests |

#### Security & Crypto

| Package | Test Files | Key Coverage |
|---------|-----------|--------------|
| `internal/crypto` | 2 | Encryption, streaming encryption |
| `internal/reporting` | 1 | Error classification, redaction, reportability |

#### Platform & Utilities

| Package | Test Files | Key Coverage |
|---------|-----------|--------------|
| `internal/diskspace` | 1 | Cross-platform disk space checking, margin-vs-message accuracy |
| `internal/localfs` | 2 | Directory browser, WalkStream |
| `internal/logging` | 1 | TeeWriter (log → EventBus) |
| `internal/platform` | 1 | Sleep prevention |
| `internal/progress` | 1 | Bar-safe log sink |
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

#### Frontend (vitest)

| File | Key Coverage |
|------|--------------|
| `stores/transferStore.test.ts` | Poll scheduling (no overlapping ticks, expanded-batch page refresh cadence), local-vs-daemon fetch namespaces, error classification, enumeration reconciliation |
| `stores/fileBrowserStore.test.ts` | Local directory load (errors, slow path, stale responses), Trash browser, My Library search pagination, Legacy Files owner filter and sorting, per-mode setter scoping |
| `stores/runStore.test.ts` | `mergePolledJobRow` — a polled row must not downgrade an in-progress upload, but must accept terminal updates |
| `stores/errorReportStore.test.ts` | Report modal open/dismiss, duplicate suppression cooldown |
| `components/tabs/JobStatusTab.test.tsx` | Fetch on tab activation, "Load next" paging, Refresh disabled during a page load, recovery when a tab-switch refresh supersedes an in-flight page |
| `components/widgets/TemplateBuilder.test.tsx` | License UX — CUSTOM/RLM preset auto-switch and its hint lifecycle |
| `components/widgets/RemoteFilePicker.test.tsx` | Workspace invalidation on API key change, discarding stale listings |

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
./bin/rescale-int files upload /tmp/test.txt

# Note the file ID from output, then download
./bin/rescale-int files download <FILE_ID> --outdir /tmp

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
./bin/rescale-int folders upload-dir /tmp/test_upload --parent-id $FOLDER_ID

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
./bin/rescale-int jobs watch -j JOB_ID -d ./output -i 30

# Newer-than watch (all jobs after reference)
./bin/rescale-int jobs watch --newer-than REF_JOB_ID -d ./output

# Watch with file filtering
./bin/rescale-int jobs watch -j JOB_ID -d ./output --filter "*.dat" --exclude "debug*"
```

---

## GUI Testing

### Development Mode Testing

```bash
# Install Wails CLI (one-time setup)
go install github.com/wailsapp/wails/v2/cmd/wails@v2.12.0

# Install frontend dependencies (matches wails.json's frontend:install)
cd frontend && npm ci && cd ..

# Run in development mode with hot-reload
wails dev
```

### Production Build Testing

```bash
# macOS (Apple Silicon)
CGO_LDFLAGS="-framework UniformTypeIdentifiers" wails build -platform darwin/arm64

# FIPS-compliant production build
GOFIPS140=certified CGO_LDFLAGS="-framework UniformTypeIdentifiers" wails build -tags fips -platform darwin/arm64

# Test production build
open build/bin/rescale-int-gui.app
```

### Frontend Unit Tests

```bash
cd frontend

# Run the vitest suite once (what CI runs)
npm run test:run

# Watch mode
npm run test

# Lint (CI runs this with --max-warnings 0)
npm run lint

# Build verification (runs tsc, then vite build)
npm run build

# Type checking only
npx tsc --noEmit
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
- All tabs render correctly (Setup, Single Job, PUR, Job Status, File Browser, Transfers, Activity Logs)
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

3. **Single Job Tab**
   - Three input modes: directory, local files, remote files
   - Tar options visible only in directory mode
   - Back button between step one and step two returns without losing entered values
   - Tags typed into the template's tag field survive save and template reload
   - Submit / Queue Job workflow

4. **Job Status Tab**
   - Loads on tab activation, refreshes on return
   - Name filter narrows the list
   - "Load next" pages forward; Refresh is disabled while a page load is in flight
   - Status badges render for every terminal and in-progress state

5. **File Browser Tab**
   - Two-pane local/remote navigation
   - Four remote browse modes: My Library, My Jobs, Legacy, Trash
   - Search within My Library / My Jobs; owner filter and column sorting on Legacy
   - A search or listing failure shows an error, not an empty library
   - Upload and download operations
   - Delete operations with confirmation; restore/purge from Trash

6. **Transfers Tab**
   - Batch grouping with collapsible rows
   - Progress bars, speed, and ETA display
   - Cancel and retry operations; a cancelled batch reads as cancelled, not completed
   - Storage retry notices appear in the batch rows and the Activity log
   - Disk space error banner
   - Daemon auto-download rows with per-row Cancel/Retry via IPC

7. **Activity Logs Tab**
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

`.github/workflows/release.yml` runs on every `v*` tag push. The first job is a
`verify` gate; the platform builds declare `needs: [verify]`, so a failing check blocks
the release instead of shipping alongside it.

**`verify`** (macos-14; Go 1.26.7 downloaded and checked against its published SHA-256,
Node.js pinned to major version 20 via `actions/setup-node`):

| Step | Command |
|------|---------|
| Install frontend deps | `npm ci` (deterministic, from `package-lock.json`) |
| Frontend tests | `npm run test:run` |
| Frontend lint | `npm run lint` |
| Frontend build | `npm run build` — also produces `frontend/dist`, which `main.go` embeds |
| Go vet | `GOFIPS140=certified go vet -tags fips ./...` |
| Go tests | `make test` (the FIPS-tagged suite) |

The frontend is built before the Go steps on purpose: `frontend/dist` is not in the
repository, and without it the Go build cannot load package `main`.

**Release builds in the workflow** (both gated on `verify`, then a `release` job that
needs them both):
- Windows build (portable zip + MSI, Azure Trusted Signing)
- macOS build (Apple Silicon, Developer ID signed + notarized)

The Linux build runs outside GitHub Actions, on a Rescale HPC job. Its AppImage carries
its own gate: `build/linux/bundle-webkit.sh` copies the host's WebKit helper
executables into the AppDir with `$ORIGIN`-relative RPATHs, and
`build/linux/verify-appimage.sh` then extracts the **finished AppImage** — deliberately
the image rather than the AppDir — and fails the release before the artifact ships if the
helpers are missing, not executable, or resolve their WebKit/GTK dependencies from
outside the bundle.

**Not automated**: test runs on pull requests, cross-platform test execution (the suite
runs on macOS only), and performance regression detection.

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

### Current State (v4.9.9)

- **Go suite**: 125 test files across 49 packages, 0 failing. `make test` reports about
  1,800 passing cases (top-level tests plus subtests) and 2 skipped. The exact figure
  moves with the platform you measure on — some tests branch on `runtime.GOOS` — so treat
  it as a magnitude, not a checksum. 16 packages have no test files.
- **Frontend suite**: 7 vitest files, 70 passing tests.
- **CI**: the `verify` job in `.github/workflows/release.yml` runs both suites, plus
  `go vet -tags fips` and the frontend lint and build, on every `v*` tag push. The
  platform builds are gated on it.
- v4.9.8 adds:
  - `TestShouldProbeResolvedDirectory` — predicate gating the walker's defensive Stat to non-regular entries (covers regular file, directory, irregular file, named pipe).
  - `TestWalkStream_SkippedChannelDrainsCleanly` — regression guard for the new `skippedChan` ensuring it closes cleanly when no entries are emitted.
  - `TestWalkCollect_SymlinkSliceContainsBrokenSymlinks` — documents the `Symlinks` slice contract for `WalkCollect` after the junction-skip refactor.
- v4.9.9 adds coverage for the behavior changes in this release, including: CLI exit
  codes on partial batch failure and on an aborted prompt; the retry elapsed-time
  budget and the notice threshold; the bar-safe log sink; disk-space margin-versus-
  message accuracy and the EDQUOT spellings; `--job-file` decode with the SSH access
  fields and unknown-key reporting; daemon state pruning and status-snapshot errors;
  paginated batch rows; and the frontend Job Status tab and File Browser filters.
- **Known Bugs**: 0
- **Quality Gates**:
  - `make test` and `npm run test:run` must pass
  - No race conditions detected
  - Coverage >75% for new code
  - Manual GUI smoke test passes
