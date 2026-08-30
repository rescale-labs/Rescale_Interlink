# Rescale Interlink - Unified CLI and GUI for Rescale Platform

A unified tool combining comprehensive command-line interface and graphical interface for managing Rescale jobs, built with Go (backend) and Wails with React/TypeScript (GUI).

![Rescale Interlink](./logo.png)

![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-blue)
![Go Version](https://img.shields.io/badge/go-1.26.7-blue)
![FIPS](https://img.shields.io/badge/FIPS%20140--3-compliant-green)
![License](https://img.shields.io/badge/license-MIT-blue)
![Status](https://img.shields.io/badge/status-v4.9.9-green)

---

> **FIPS 140-3 Compliance (FedRAMP Moderate)**
>
> Built with FIPS 140-3 compliant cryptography using the Go 1.26.7 native FIPS module.
> Every `make` build target sets `GOFIPS140=certified` — the Go Cryptographic Module
> version that holds a CMVP validation certificate — and builds with the `fips` build
> tag. Verify FIPS status with `rescale-int --version`.
>
> See [Go FIPS 140-3 Documentation](https://go.dev/doc/security/fips140) for details.

---

## What's New in v4.9.9

- **Design of experiments in PUR.** One base job can be expanded into a parameter sweep — one Rescale job per design point — from the CLI (`pur doe`) or the PUR tab's sweep builder, with each case's values rendered into its own command line. Eight designs, per-case job name and tag templates, and one shared input deck for the whole sweep.
- **New Job Status tab.** The GUI gains a dedicated tab listing your most recent jobs with status, dates and a name/ID filter, loading a page at a time rather than fetching everything up front.
- **File Browser: search, owner filter, sorting and better pagination.** The remote pane can search by file name, restrict a listing to your own files or files shared with you, and sort by name, size or upload date, with the pagination cursor carried through search.
- **Transfers tell the truth about what happened.** A cancelled folder transfer now reads as cancelled rather than as a clean completion, on the batch row and in the CLI. Storage retries and API rate-limit throttling are surfaced instead of silently stalling a transfer ([#22](https://github.com/rescale-labs/Rescale_Interlink/issues/22)), including for the detached auto-download daemon.
- **CLI progress bars and exit codes fixed.** Diagnostic log lines are routed through the progress-bar writer instead of landing inside a redrawing frame ([#23](https://github.com/rescale-labs/Rescale_Interlink/issues/23)). And a run that printed a failure summary no longer exits 0: `folders upload-dir` and `pur run` return an error when any item failed, aborting at a prompt stops the batch instead of continuing through the remaining files, and a prompt that cannot run (no terminal) records the file as failed rather than dropping it silently — so scripts and CI see a failed run as failed.
- **Auto-download daemon reliability.** A broken daemon now says so on every surface that reports its state, a zero-task download batch no longer wedges the poll loop, and its unbounded internal state is now bounded.
- **Disk space refusals now agree with themselves.** A download could be refused with "need 292366 MB, have 312832 MB available" — need below have. The pre-flight check's own figures are reported verbatim, and free space is measured on the download directory's filesystem rather than its parent ([#34](https://github.com/rescale-labs/Rescale_Interlink/issues/34)).
- **Upload integrity, and much larger files.** Multipart part size now scales with the file instead of capping at 64 MB, so uploads are no longer limited to 640 GB on S3 or 3.2 TB on Azure by part-count ceilings, and a file beyond a backend's maximum is refused up front rather than failing mid-transfer. Every upload path now verifies that all bytes and all parts arrived before the file is registered, so a short read during upload or encryption can no longer commit a truncated object.
- **Job submission carries SSH access settings.** `cidrRule`, `publicKey` and `sshPort` from a job file or an SGE script now reach the API instead of being dropped during decode ([#43](https://github.com/rescale-labs/Rescale_Interlink/issues/43)).
- **Linux AppImage renders on hosts with a different WebKit.** The AppImage now bundles the WebKitGTK helper processes it forks, with a release gate that verifies they resolve their libraries from inside the bundle. Previously a host WebKit mismatch left a window that painted but never rendered content.
- **Toolchain pinned; tag builds gated on tests.** A checksum-verified Go 1.26.7 toolchain, pinned Node 20 (also checksum-verified on the Linux build), deterministic `npm ci` installs, and a release pipeline that runs the full test suite before it builds or signs anything.

See [RELEASE_NOTES.md](RELEASE_NOTES.md) for complete version history.

---

## Features

### Dual-Mode Architecture

- **CLI Binary** (`rescale-int`): Full-featured command-line interface
- **GUI Binary** (`rescale-int-gui`): Interactive graphical application built with Wails (React/TypeScript)
- **Platform Packages**: Each platform has both binaries packaged together (AppImage, zip, or MSI)

### CLI Features

- **Configuration Management**: Interactive setup with `config init`
- **File Operations**: Upload, download, list, and delete files (delete moves to Trash by default; `--permanent` for irreversible delete)
- **File Tags**: `files tags list`, `add`, `remove`, and `set` (replaces a file's whole tag list; with no tags, clears it)
- **Folder Management**: Create, list, bulk upload and bulk download with connection reuse and folder caching
- **Job Operations**: Submit, monitor, control, download results
- **Job Watch**: Monitor running jobs and incrementally download output files
- **Catalog Lookups**: Browse available hardware, software and automations (`hardware list`, `software list`, `automations list`)
- **Compatibility Mode**: Drop-in replacement for `rescale-cli` (10 commands)
- **PUR Integration**: Batch job pipeline execution
- **Design of Experiments**: Expand one base job into a parameter sweep (`pur doe`), with values rendered into each case's command line
- **Error Reporting**: Diagnostic reports with redacted context for server errors
- **Adaptive Concurrency**: Automatic thread scaling based on file size distribution
- **Command Shortcuts**: Quick aliases (`upload`, `download`, `ls`)
- **Shell Completion**: Bash, Zsh, Fish, PowerShell support
- **Progress Tracking**: Multi-progress bars for concurrent operations
- **Streaming Encryption**: Per-part AES-256-CBC encryption during upload
- **Multi-part/Multi-threaded Transfers**: Dynamic thread allocation based on file size
- **Resume Support**: Resume interrupted downloads from exact byte position

### GUI Features

Built with [Wails](https://wails.io/) (Go backend, React/TypeScript frontend):

- **Seven-Tab Interface**:
  - **Setup**: API key, proxy configuration, logging settings, test connection, auto-download daemon
  - **Single Job**: Configure and submit individual jobs (directory with tar options, local files, or remote files)
  - **PUR (Multiple Jobs)**: Batch job pipeline (PUR = Parallel Upload and Run) with Pipeline Settings (workers, tar options), directory scanning, and a DOE parameter sweep builder
  - **Job Status**: Paged listing of your recent jobs with status, dates and a name/ID filter
  - **File Browser**: Two-pane local/remote file browser with upload/download
  - **Transfers**: Real-time transfer queue with progress, cancel, retry, disk space error banner
  - **Activity Logs**: Live log display with filtering, search, and run history panel

- **Modern UI**:
  - React-based responsive design
  - Tailwind CSS styling
  - Virtual scrolling for large lists (TanStack Table + TanStack Virtual)
  - Real-time progress updates via Wails events

- **File Browser**:
  - Two-pane layout (local left, remote right)
  - My Library / My Jobs / Legacy / Trash browse modes
  - Search by file name, filter by owner (your files or files shared with you), sort by name, size or upload date
  - Paged remote listings, with the page cursor carried through search
  - Multi-file selection with checkboxes and Shift/Ctrl
  - Upload/download with concurrent transfers
  - Delete remote files/folders (deleted entries are visible and recoverable in Trash)

- **Job Management**:
  - Template builder with searchable software/hardware selection, and tags
  - CSV/JSON/SGE job file load/save
  - Directory scanning with pattern matching
  - Real-time job status updates
  - Active runs survive tab navigation and app restart
  - Single Job's step navigation has a Back button that keeps the form state you already entered

---

## Quick Start

### Prerequisites

- macOS, Linux, or Windows
- Rescale API key
- For building from source: Go 1.26.7, Node.js 20, and the [Wails v2 CLI](https://wails.io/) for the GUI binary

### Installation

#### Option 1: Use Pre-built Packages

Download the latest release for your platform from [GitHub Releases](https://github.com/rescale-labs/Rescale_Interlink/releases).

| Platform | Release asset | Contents |
|----------|---------------|----------|
| macOS (Apple Silicon) | `rescale-interlink-v<version>-macos_aarch64.zip` | `rescale-int-gui.app` + `rescale-int` CLI |
| Linux (x64) | `rescale-interlink-v<version>-linux-amd64.tar.gz` | `rescale-int-gui.AppImage` + `rescale-int` CLI |
| Windows (x64) | `rescale-interlink-v<version>-win_amd64.msi` (installer) or `rescale-interlink-v<version>-win_amd64.zip` (portable) | `rescale-int-gui.exe` + `rescale-int.exe` CLI |

**macOS:** Unzip, move `rescale-int-gui.app` to Applications. Copy `rescale-int` to a directory in your PATH for CLI usage.

**Linux:** Extract the tarball, `chmod +x` both binaries. Double-click the AppImage or run `./rescale-int --help` for CLI.

**Windows:** Unzip and run `rescale-int-gui.exe`, or use the MSI installer for Start Menu integration.

#### Option 2: Build from Source

```bash
# Clone the repository
git clone https://github.com/rescale-labs/Rescale_Interlink.git
cd Rescale_Interlink

# Build the CLI binary for the current platform (FIPS 140-3 automatically).
# Output goes to bin/<version>/<os>-<arch>/rescale-int — never the project root.
make build
```

The GUI binary is built by Wails, not by `make`, and needs the frontend installed first:

```bash
cd frontend && npm ci && cd ..

# macOS example; the CGO_LDFLAGS is macOS-only
GOFIPS140=certified CGO_LDFLAGS="-framework UniformTypeIdentifiers" \
  wails build -tags fips -platform darwin/arm64
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the macOS GUI build invocation and the rest of
the development setup; `.github/workflows/release.yml` has the exact commands the Windows and macOS release
builds run.

### First Run (CLI Mode)

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

### First Run (GUI Mode)

1. Launch the application (double-click `.app`, `.AppImage`, or `.exe`)
2. Go to **Setup** tab
3. Configure API URL and API Key
4. Click **Test Connection**, then **Save All Settings**
5. Ready to submit jobs!

---

## Usage

### CLI Mode (Default)

**Upload files:**
```bash
rescale-int upload model.tar.gz input.dat
```

**Bulk upload directory:**
```bash
rescale-int folders upload-dir ./simulation_data --parent-id abc123
```

**Bulk download a remote folder:**
```bash
rescale-int folders download-dir <folder-id> --outdir ./downloads
```

**Download files:**
```bash
# Download single file
rescale-int download <file-id> --outdir ./downloads

# Download with conflict handling
rescale-int files download <file-id> --overwrite
rescale-int files download <file-id> --skip
rescale-int files download <file-id> --resume
```

**List jobs:**
```bash
rescale-int ls --limit 20
```

**Monitor job:**
```bash
rescale-int jobs tail --job-id WfbQa --interval 5
```

**Watch job and download results as they appear:**
```bash
rescale-int jobs watch -j WfbQa -d ./results
```

**Download job results:**
```bash
rescale-int jobs download --id WfbQa --outdir ./results
```

**Run batch job pipeline:**
```bash
rescale-int pur run --jobs-csv jobs.csv --state state.csv
```

**Expand one base job into a parameter sweep:**
```bash
rescale-int pur doe --template base.csv --output sweep.csv \
  --param "alpha=10:20:3" --param "beta=15:25:3"
```

**See [CLI_GUIDE.md](CLI_GUIDE.md) for complete command reference.**

### GUI Mode

The GUI provides seven tabs:

1. **Setup**: API credentials, proxy settings, logging, daemon control
2. **Single Job**: Create and submit individual jobs (directory with tar options, local files, or remote files)
3. **PUR (Multiple Jobs)**: Parallel Upload and Run - batch job pipeline, from folders, input files, or a DOE parameter sweep
4. **Job Status**: Browse your recent jobs a page at a time, filtered by name or ID
5. **File Browser**: Two-pane file manager for local and remote files
6. **Transfers**: Monitor active transfers with progress and controls
7. **Activity Logs**: View real-time logs with filtering and search

### Daemon Control

The auto-download daemon automatically downloads completed jobs. Control it via CLI or GUI:

```bash
# Start daemon in background with IPC control
rescale-int daemon run --background --ipc --download-dir ./results

# Query running daemon status — includes the most recent scan failure,
# how long ago it happened, and what to do about it
rescale-int daemon status

# List downloaded jobs (--failed for the failures), and mark
# failed jobs to be retried on the next poll cycle
rescale-int daemon list
rescale-int daemon retry

# Stop running daemon
rescale-int daemon stop
```

In the GUI, the Setup tab provides start/stop/pause/resume buttons, status indicators, and "Scan Now" for immediate job checks. A scan that fails (expired key, dead network, proxy trouble) is reported with its age rather than showing only as a last-scan timestamp that stops advancing. On Windows, a tray icon provides the same controls. Both Windows distributions ship `rescale-int-tray.exe`; the MSI additionally registers it to start with your session, while from the portable zip you start it yourself. Either way the daemon runs as a session subprocess until you install the optional Windows service from the tray's "Install Service (Admin)" item, which prompts for elevation. macOS and Linux do not include a tray — the main GUI fills that role.

For auto-start on login (macOS launchd, Linux systemd), see [CLI_GUIDE.md](CLI_GUIDE.md#auto-start-on-login).

### Configuration

```bash
# Interactive setup
rescale-int config init

# View configuration
rescale-int config show

# Test connection
rescale-int config test
```

Environment variables: `RESCALE_API_KEY`, `RESCALE_API_URL`. For scripts, use a token file with `--token-file`.

---

## Architecture

See [ARCHITECTURE.md](ARCHITECTURE.md) for detailed system design and code organization.

### Project Structure

An abridged view. [ARCHITECTURE.md](ARCHITECTURE.md) has the full package tree.

```
rescale-int/
├── main.go                   # GUI+CLI binary entry point (rescale-int-gui)
├── cmd/
│   ├── rescale-int/          # CLI-only binary entry point
│   └── rescale-int-tray/     # Windows system tray companion (MSI install only)
│
├── frontend/                 # Wails React frontend
│   ├── src/
│   │   ├── App.tsx           # Main app with tabs
│   │   ├── components/
│   │   │   ├── tabs/         # The seven tab implementations
│   │   │   └── widgets/      # Shared widgets (JobsTable, TemplateBuilder, ...)
│   │   └── stores/           # Zustand stores
│   └── wailsjs/              # Auto-generated Wails bindings
│
├── internal/
│   ├── cli/                  # CLI commands (Cobra)
│   │   ├── compat/           # rescale-cli compatibility mode
│   │   ├── root.go
│   │   ├── files.go
│   │   ├── folders.go
│   │   ├── jobs.go
│   │   ├── jobs_watch.go     # jobs watch command
│   │   └── pur.go
│   │
│   ├── wailsapp/             # Wails Go bindings
│   ├── services/             # GUI-agnostic services (TransferService, FileService)
│   ├── cloud/                # Cloud storage backends (S3, Azure)
│   ├── daemon/               # Auto-download daemon
│   ├── service/              # Windows service mode
│   ├── ipc/                  # Inter-process communication
│   ├── api/                  # Rescale API client (v3 + v2)
│   ├── ratelimit/            # Token bucket rate limiting + cross-process coordinator
│   ├── events/               # Event bus system
│   ├── watch/                # Shared job watch/poll engine
│   ├── reporting/            # Error diagnostic reports
│   ├── platform/             # OS-specific (sleep inhibit)
│   ├── transfer/             # Transfer orchestration
│   │   ├── scan/             # Remote folder scanning
│   │   └── folder/           # Folder upload primitives
│   ├── resources/            # Thread pool and adaptive concurrency
│   ├── progress/             # CLI progress bars (mpb wrapper)
│   ├── crypto/               # AES-256-CBC streaming encryption
│   ├── core/                 # Core engine
│   └── pur/                  # PUR pipeline packages
│
├── build/                    # Packaging assets and platform build scripts
├── CLI_GUIDE.md              # Complete CLI reference
├── ARCHITECTURE.md           # System architecture
├── CONTRIBUTING.md           # Contribution guide
└── README.md                 # This file
```

---

## Development

```bash
# Development with hot reload
wails dev -appargs "--gui"

# CLI builds (all FIPS 140-3)
make build-darwin-arm64
make build-linux-amd64
make build-windows-amd64

# Everything make can do
make help
```

Checks, matching what the release pipeline's `verify` job runs before it builds anything.
CI runs the frontend steps first, because `main.go` embeds `frontend/dist`:

```bash
cd frontend && npm ci && npm run test:run && npm run lint && npm run build && cd ..
GOFIPS140=certified go vet -tags fips ./...
make test                                 # go test under GOFIPS140=certified -tags fips
```

`make check` (compile check, no binary output) is a local convenience; the pipeline does
not run it.

After changing Go binding methods: `wails generate module`

See [CONTRIBUTING.md](CONTRIBUTING.md) for detailed development setup and guidelines.

---

## Troubleshooting

**Connection Issues:** Verify API key (`echo $RESCALE_API_KEY`), check proxy settings in Setup tab, try `system` proxy mode.

**Build Failures:** Clean and rebuild: `make clean && cd frontend && rm -rf node_modules && npm ci && cd .. && make build`

**A CLI transfer looks like it is doing nothing:** transfer diagnostics are hidden at default verbosity so they cannot corrupt the progress bars. Re-run with `--verbose` (or `--debug`, or set `RESCALE_DEBUG`) to see them; they are then interleaved above the bars. Rate-limit and retry notices are always shown.

---

## Known Limitations

- Compat mode covers 10 of rescale-cli's commands; software publisher (spub) commands are not yet supported
- No support for Rescale CFS or Publisher capabilities. In compat mode, `upload --copy-to-cfs` and `upload -T/--Target` return an explicit "not yet implemented" error rather than silently doing nothing
- Terminal resize during CLI progress bars causes visual artifacts (transfers continue correctly)
- The system tray and the installable system service are Windows-only. macOS and Linux run the auto-download daemon as a session-scoped subprocess; auto-start on login is manual (see [CLI_GUIDE.md](CLI_GUIDE.md#auto-start-on-login))

---

## License

MIT License - see [LICENSE](LICENSE) for the full text

---

## Documentation

- **[CLI_GUIDE.md](CLI_GUIDE.md)** - Complete command-line reference with examples
- **[ARCHITECTURE.md](ARCHITECTURE.md)** - System design and code organization
- **[SECURITY.md](SECURITY.md)** - Security documentation (FIPS, proxy, logging, IPC)
- **[TESTING.md](TESTING.md)** - Test strategy and procedures
- **[CONTRIBUTING.md](CONTRIBUTING.md)** - Developer onboarding guide
- **[RELEASE_NOTES.md](RELEASE_NOTES.md)** - Version history and release details
- **[FEATURE_SUMMARY.md](FEATURE_SUMMARY.md)** - Comprehensive feature list with source references

---

**Version**: 4.9.9
**Status**: Production Ready
**Last Updated**: August 25, 2026
