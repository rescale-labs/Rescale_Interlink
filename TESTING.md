# Testing Guide - Rescale Interlink

**Last Updated**: September 26, 2026
**Version**: 4.9.9

For comprehensive feature details, see [FEATURE_SUMMARY.md](FEATURE_SUMMARY.md).

---

## Table of Contents

- [Running Tests](#running-tests)
- [Test Coverage](#test-coverage)
- [Manual Testing Procedures](#manual-testing-procedures)
- [GUI Testing](#gui-testing)
- [Troubleshooting Tests](#troubleshooting-tests)
- [Adding New Tests](#adding-new-tests)
- [Continuous Integration](#continuous-integration)
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

On Windows without administrator rights, `build\windows_local_build\check.ps1 -Test`
vets and runs the Go suite (the packages CI tests: all but the root package) in FIPS
mode with the portable toolchain that `install-deps.ps1` sets up (see
[CONTRIBUTING.md](CONTRIBUTING.md)).

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

295 Go test files (292 under `internal/`, two under `installer/` and one under `cmd/rescale-int-tray/`) across 57 packages,
plus 16 frontend vitest files. Grouped by functional area:

#### CLI & Commands

| Package | Key Coverage |
|---------|--------------|
| `internal/cli` | Command parsing, flag aliases, config commands, daemon commands, file deletion, job-file decode, conflict resolution, job monitoring, folder-upload abort/failure paths, download helper (incl. the skip-existing size gate), shortcut concurrency |
| `internal/cli/compat` | Compat mode detection, arg normalization, commands, execution, JSON output, submit, parity, skip-existing size gate |

#### Core Infrastructure

| Package | Key Coverage |
|---------|--------------|
| `internal/api` | Client, retry policy and budget, pagination |
| `internal/core` | Engine pipeline orchestration, upload-progress reporting |
| `internal/events` | EventBus pub/sub, ring buffer |
| `internal/config` | CSV config, API config, jobs CSV, daemon config, platforms (incl. internal- and production-tagged variants), proxy features (FIPS / non-FIPS), token ACL on Windows |
| `internal/models` | Job serialization, including the SSH access fields |
| `internal/pathutil` | Path resolution, `~` expansion, download-folder write checks |
| `internal/validation` | Path validation |

#### Cloud & Transfer

| Package | Key Coverage |
|---------|--------------|
| `internal/cloud` | Timing utilities, retry notices |
| `internal/cloud/credentials` | Credential management |
| `internal/cloud/download` | Corrupt-file quarantine, downloaded-size verification |
| `internal/cloud/providers/s3` | S3 upload progress reader, provider behavior, pre-encrypt part-count plan and short-upload refusal |
| `internal/cloud/providers/azure` | Azure client, SAS token lookup, pre-encrypt block-count plan and short-upload refusal |
| `internal/cloud/state` | Resume state serialization |
| `internal/cloud/storage` | Disk-full and quota error classification |
| `internal/cloud/transfer` | Transfer orchestration, object-format parsing, concurrent chunked download and range-fetch retry |
| `internal/cloud/upload` | Upload flow |
| `internal/transfer` | Batch executor, queue (incl. paginated batch rows), speed window, manager |
| `internal/transfer/folder` | Folder creation, orchestrator |
| `internal/transfer/scan` | Remote folder scanning |

#### Services & GUI Bindings

| Package | Key Coverage |
|---------|--------------|
| `internal/wailsapp` | Job bindings, job-status bindings, path helpers, version bindings, daemon bindings, config bindings, API key source bindings, progress + failure-path tests |
| `internal/services` | Transfer service |

#### PUR (Parallel Upload and Run)

| Package | Key Coverage |
|---------|--------------|
| `internal/pur/doe` | Sweep designs (full factorial, OFAT, Latin hypercube, Sobol, Monte Carlo and the rest), cases CSV parsing, seeded reproducibility |
| `internal/pur/filescan` | File scanning |
| `internal/pur/parser` | SGE script parsing, SSH directive round-trip |
| `internal/pur/pattern` | Pattern detection |
| `internal/pur/pipeline` | Pipeline orchestration, failed-job accounting, JobSpec → JobRequest mapping |
| `internal/pur/state` | State file persistence: concurrent saves and checkpoints, in-memory runs, unconfirmed creations, a private file without credentials |
| `internal/pur/validation` | Job spec validation, including the license pair |

#### Networking & Rate Limiting

| Package | Key Coverage |
|---------|--------------|
| `internal/http` | Proxy, retry logic and elapsed budget, NTLM transport under FIPS |
| `internal/ratelimit` | Token bucket, registry, store (incl. degraded-mode notices) |
| `internal/ratelimit/coordinator` | Cross-process coordination, one coordinator per user |

#### Background Auto-Download

| Package | Key Coverage |
|---------|--------------|
| `internal/daemon` | Daemon lifecycle, monitor, state pruning, transfer tracker, status snapshot errors, workspace folders, claims between clients, job folder names |
| `internal/service` | Detection, daemon status and user matching, removal of an earlier version's service |
| `internal/ipc` | Client/server, messages, per-user pipe names and owner-only pipes, error texts |

#### Security & Crypto

| Package | Key Coverage |
|---------|--------------|
| `internal/crypto` | Encryption, streaming encryption |
| `internal/reporting` | Error classification, redaction, reportability |

#### Platform & Utilities

| Package | Key Coverage |
|---------|--------------|
| `internal/diskspace` | Cross-platform disk space checking, margin-vs-message accuracy |
| `internal/localfs` | Directory browser, WalkStream |
| `internal/mesainit` | The CLI-or-GUI choice from the command line |
| `internal/logging` | TeeWriter (log → EventBus), colour only on a terminal |
| `internal/platform` | Sleep prevention |
| `internal/progress` | Bar-safe log sink |
| `internal/resources` | Thread pool, memory management, upload plan geometry and shared memory budget |
| `internal/watch` | Job watch engine |
| `internal/util/analysis` | Analysis utilities |
| `internal/util/buffers` | Buffer pooling |
| `internal/util/filter` | Path patterns for `--path-filter` and PUR file scans, matched part by part |
| `internal/util/glob` | Glob pattern matching |
| `internal/util/multipart` | Multipart scan: absolute and resolved run folders, skipped directories |
| `internal/util/paths` | Path collision detection |
| `internal/util/sanitize` | String sanitization |
| `internal/util/tags` | File tag utilities |
| `internal/util/tar` | Archive creation: explicit file sets, entry names and symlinks, partial-archive cleanup, finalization failures |

#### Other

| Package | Key Coverage |
|---------|--------------|
| `installer` | What the MSI source must and must not contain; the toolchain pins agree across workflows and scripts |
| `cmd/rescale-int-tray` | Windows only: Start refusals are shown, start at login only when enabled and after the download folder appears |

#### Frontend (vitest)

| File | Key Coverage |
|------|--------------|
| `stores/transferStore.test.ts` | Poll scheduling (no overlapping ticks, expanded-batch page refresh cadence), local-vs-daemon fetch namespaces, error classification, enumeration reconciliation |
| `stores/fileBrowserStore.test.ts` | Local directory load (errors, slow path, stale responses), Trash browser, My Library search pagination, Legacy Files owner filter and sorting, per-mode setter scoping |
| `stores/runStore.test.ts` | `mergePolledJobRow` — a polled row must not downgrade an in-progress upload, but must accept terminal updates |
| `stores/errorReportStore.test.ts` | Report modal open/dismiss, duplicate suppression cooldown |
| `components/tabs/JobStatusTab.test.tsx` | Fetch on tab activation, "Load next" paging, Refresh disabled during a page load, recovery when a tab-switch refresh supersedes an in-flight page |
| `components/tabs/FileBrowserTab.test.tsx` | Upload gating (job output folders, in-flight and failed folder loads) and destination resolution — the confirmation dialog names the folder the current view resolved, not the previous one |
| `components/widgets/TemplateBuilder.test.tsx` | License UX — CUSTOM/RLM preset auto-switch and its hint lifecycle |
| `components/widgets/RemoteFilePicker.test.tsx` | Workspace invalidation on API key change, discarding stale listings |

Also: `jobStore`, the PUR, Setup and Single Job tabs, and the `ErrorSummary`,
`RemoteBrowser` and `StatsBar` widgets.

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
./bin/v4.9.9/darwin-arm64/rescale-int files upload /tmp/test.txt

# Note the file ID from output, then download
./bin/v4.9.9/darwin-arm64/rescale-int files download <FILE_ID> --outdir /tmp

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
FOLDER_ID=$(./bin/v4.9.9/darwin-arm64/rescale-int folders create --name "Test_$(date +%s)" | grep -oE '[a-zA-Z0-9]{6}')

# Upload directory
./bin/v4.9.9/darwin-arm64/rescale-int folders upload-dir /tmp/test_upload --parent-id $FOLDER_ID

# Verify
./bin/v4.9.9/darwin-arm64/rescale-int folders list --folder-id $FOLDER_ID
```

### Compat Mode Testing

```bash
# Verify compat mode activates
./bin/v4.9.9/darwin-arm64/rescale-int --compat --version

# Test via symlink
ln -s v4.9.9/darwin-arm64/rescale-int ./bin/rescale-cli
./bin/rescale-cli --version

# Test credential chain
./bin/rescale-cli -p $(cat /path/to/token) status -j JOB_ID

# Test argument normalization
./bin/rescale-cli upload -f file1.txt file2.txt file3.txt  # multi-value -f

# Test exit code convention
./bin/rescale-cli status -j NONEXISTENT; echo "Exit code: $?"  # should be 33
```

### Jobs Watch Testing

```bash
# Single-job watch
./bin/v4.9.9/darwin-arm64/rescale-int jobs watch -j JOB_ID -d ./output -i 30

# Newer-than watch (all jobs after reference)
./bin/v4.9.9/darwin-arm64/rescale-int jobs watch --newer-than REF_JOB_ID -d ./output

# Watch with file filtering
./bin/v4.9.9/darwin-arm64/rescale-int jobs watch -j JOB_ID -d ./output --filter "*.dat" --exclude "debug*"
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
# macOS (Apple Silicon). GOFIPS140=certified is what the startup FIPS check tests —
# a GUI built without it exits 2. -tags fips selects the FIPS-only proxy/NTLM paths,
# so production builds need both.
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
./bin/v4.9.9/darwin-arm64/rescale-int config test

# Check logs
./bin/v4.9.9/darwin-arm64/rescale-int files list --verbose
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

Keep every test hermetic. Point `HOME` (and `USERPROFILE`, `APPDATA` and `LOCALAPPDATA`
where Windows paths are involved) at `t.TempDir()`, clear credential variables such as
`RESCALE_API_KEY`, and serve API and storage calls from a local `httptest` server. A test
must never read the developer's configuration or token, or reach the live platform.

### GUI Test Checklist

- [ ] Feature works in relevant tab(s)
- [ ] No UI freezes or deadlocks
- [ ] Progress indicators update correctly
- [ ] Error messages display properly
- [ ] Clean shutdown after operations

---

## Continuous Integration

`.github/workflows/release.yml` runs on every `v*` tag push, and when a maintainer runs
it by hand from any branch (Actions → Release Rescale Interlink → Run workflow, with a
`vX.Y.Z` or `vX.Y.Z-rc.N` version). Either way it makes a draft release; a manual run's
draft has no Linux tarball, and its tag is created when the draft is published. A
`check` job runs first and never replaces a release: a manual run stops if its version
already has a tag or a release, draft or published, so a second manual run for one
version waits for the first and then stops; a tag push whose release was already published from
the same commit builds nothing, and from another commit it stops. The release job checks
again just before it creates the draft. Then comes a `verify` gate; the platform builds
declare `needs: [verify]`, so a failing check blocks the release instead of shipping
alongside it.

**`verify`** (macos-14; Go 1.26.7 downloaded and checked against its published SHA-256,
Node.js 24.21.0 via `actions/setup-node`):

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

The Linux build runs in `.github/workflows/release-linux.yml`, in an `almalinux:8`
container, through `build/linux/build-release.sh`; it runs no test suite. Go, Node.js
and the AppImage tools it downloads are checked against pinned SHA-256 sums, the Wails
CLI comes through `go install`, and the system packages from the AlmaLinux and EPEL
repositories through `dnf`; `ldd -r` resolves the GUI and its WebKit helper executables, and the build fails if any shipped binary
needs a GLIBC newer than 2.28. Its AppImage carries its own gate:
`build/linux/bundle-webkit.sh` copies the host's WebKit helper executables into the
AppDir with `$ORIGIN`-relative RPATHs, and `build/linux/verify-appimage.sh` then
extracts the **finished AppImage** — deliberately the image rather than the AppDir — and
fails the release before the artifact ships if the helpers are missing, not executable,
or resolve their WebKit/GTK dependencies from outside the bundle.

**`.github/workflows/test.yml`** runs on pull requests and on pushes to `release/**`: the
Go suite (without the root package, which embeds `frontend/dist`) natively on
`windows-latest` and, with `-race`, on `ubuntu-latest`, with Go 1.26.7 checked against
its published SHA-256. One step in each job lists the platform-only tests by package
and fails the job unless each ran and passed: on Linux the upload-lock tests; on Windows
those and the Windows-only tests of the retired service, the per-user pipes, the tray,
drive-relative scan roots, the app's Start and Stop, and `daemon stop --force`'s process
checks. A skip or a
missing test fails the job. A third job, on macos-14, runs the `verify` job's frontend
install, tests, lint and build and then `go vet -tags fips ./...`, with Node.js 24.21.0.

**Not automated**: a macOS Go test run on pull requests (the `verify` job runs it only
for releases), and performance regression detection.

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

- **Go suite**: 295 test files across 57 packages — roughly 1,520 top-level test
  functions, plus subtests. Don't treat any of the reported case totals as a
  checksum: some tests branch on `runtime.GOOS`, so what `make test` counts, and what it
  skips, depends on the platform you measure on. 9 of the project's own packages have no test files.
- **Frontend suite**: 16 vitest files.
- **CI**: the `verify` job in `.github/workflows/release.yml` runs both suites, plus
  `go vet -tags fips` and the frontend lint and build, on every release run. The
  platform builds are gated on it. `.github/workflows/test.yml` runs the Go suite
  natively on Windows and Linux for pull requests and pushes to `release/**`, where a
  macOS job also runs the frontend tests, lint and build and `go vet`.
- v4.9.8 adds:
  - `TestShouldProbeResolvedDirectory` — predicate gating the walker's defensive Stat to non-regular entries (covers regular file, directory, irregular file, named pipe).
  - `TestWalkStream_SkippedChannelDrainsCleanly` — regression guard for the new `skippedChan` ensuring it closes cleanly when no entries are emitted.
- v4.9.9 adds coverage for the behavior changes in this release, including: CLI exit
  codes on partial batch failure and on an aborted prompt; the retry elapsed-time
  budget and the notice threshold; the bar-safe log sink; disk-space margin-versus-
  message accuracy and the EDQUOT spellings; `--job-file` decode with the SSH access
  fields and unknown-key reporting; daemon state pruning and status-snapshot errors;
  paginated batch rows; and the frontend Job Status tab and File Browser filters.
  The upload-integrity work adds:
  - `internal/cloud/providers/s3/pre_encrypt_test.go` and
    `internal/cloud/providers/azure/pre_encrypt_test.go` — every part or block is
    uploaded, an upload short of the full byte count is refused rather than committed,
    resume state belonging to another object is discarded, oversized files are rejected
    before any request, and the plan's worker cap is respected.
  - `internal/resources/upload_plan_test.go` — part-size and part-count geometry against
    the S3 and Azure limits, the memory floor, and the shared budget across concurrent
    and batched uploads.
  - `internal/crypto/encryption_test.go` — short reads, mid-stream short reads, and read
    errors during file encryption, with a differential check that the ciphertext still
    matches the previous implementation byte for byte.
  - `frontend/src/components/tabs/FileBrowserTab.test.tsx` — upload gating and
    destination resolution in the File Browser.
- **Known Bugs**: 0
- **Quality Gates**:
  - `make test` and `npm run test:run` must pass
  - No race conditions detected
  - Coverage >75% for new code
  - Manual GUI smoke test passes
