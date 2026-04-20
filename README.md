# Rescale Interlink - Unified CLI and GUI for Rescale Platform

A unified tool combining comprehensive command-line interface and graphical interface for managing Rescale jobs, built with Go (backend) and Wails with React/TypeScript (GUI).

![Rescale Interlink](./logo.png)

![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-blue)
![Go Version](https://img.shields.io/badge/go-1.24+-blue)
![FIPS](https://img.shields.io/badge/FIPS%20140--3-compliant-green)
![License](https://img.shields.io/badge/license-MIT-blue)
![Status](https://img.shields.io/badge/status-v4.9.4-green)

---

> **FIPS 140-3 Compliance (FedRAMP Moderate)**
>
> Built with FIPS 140-3 compliant cryptography using Go 1.24's native FIPS module.
> Use `make build` which includes FIPS automatically. Verify FIPS status with `rescale-int --version`.
>
> See [Go FIPS 140-3 Documentation](https://go.dev/doc/security/fips140) for details.

---

## What's New in v4.9.4

- **Unified Transfers tab**: Auto-download transfers now appear alongside GUI transfers with a `Daemon` badge, and per-row Cancel/Retry works across both engines.
- **Tag-based re-download**: Removing the `downloaded` tag on a job in the Rescale web UI triggers a fresh download on the next poll. Tag-apply failures are retried without re-downloading the files.
- **Adaptive daemon concurrency**: Auto-download now transfers multi-file jobs in parallel using the same engine as the GUI, not one file at a time.
- **Save-time path validation**: Mapped-drive and UNC download paths are rejected at save time on Windows with a clear error.
- **Security hardening**: Explicit Windows DACL on the API token file; tightened IPC authorization for all user-scoped operations.
- **Cross-platform clarity**: macOS/Linux Auto-Download tab now makes the session-scoped lifecycle explicit; tray is documented as Windows-MSI-only.

See [RELEASE_NOTES.md](RELEASE_NOTES.md) for complete version history.

---

## Features

### Dual-Mode Architecture

- **CLI Binary** (`rescale-int`): Full-featured command-line interface
- **GUI Binary** (`rescale-int-gui`): Interactive graphical application built with Wails (React/TypeScript)
- **Platform Packages**: Each platform has both binaries packaged together (AppImage, zip, or MSI)

### CLI Features

- **Configuration Management**: Interactive setup with `config init`
- **File Operations**: Upload, download, list, delete files
- **Folder Management**: Create, list, bulk upload with connection reuse and folder caching
- **Job Operations**: Submit, monitor, control, download results
- **Job Watch**: Monitor running jobs and incrementally download output files
- **Compatibility Mode**: Drop-in replacement for `rescale-cli` (10 commands)
- **PUR Integration**: Batch job pipeline execution
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

- **Six-Tab Interface**:
  - **Setup**: API key, proxy configuration, logging settings, test connection
  - **Single Job**: Configure and submit individual jobs (directory with tar options, local files, or remote files)
  - **PUR (Parallel Upload Run)**: Batch job pipeline with Pipeline Settings (workers, tar options), directory scanning
  - **File Browser**: Two-pane local/remote file browser with upload/download
  - **Transfers**: Real-time transfer queue with progress, cancel, retry, disk space error banner
  - **Activity**: Live log display with filtering, search, and run history panel

- **Modern UI**:
  - React-based responsive design
  - Tailwind CSS styling
  - Virtual scrolling for large lists (TanStack Table)
  - Real-time progress updates via Wails events

- **File Browser**:
  - Two-pane layout (local left, remote right)
  - My Library / My Jobs / Legacy browse modes
  - Multi-file selection with checkboxes and Shift/Ctrl
  - Upload/download with concurrent transfers
  - Delete remote files/folders

- **Job Management**:
  - Template builder with searchable software/hardware selection
  - CSV/JSON/SGE job file load/save
  - Directory scanning with pattern matching
  - Real-time job status updates
  - Active runs survive tab navigation and app restart

---

## Quick Start

### Prerequisites

- macOS, Linux, or Windows
- Rescale API key
- For building from source: Go 1.24+, Node.js 18+

### Installation

#### Option 1: Use Pre-built Packages

Download the latest release for your platform from [GitHub Releases](https://github.com/rescale-labs/Rescale_Interlink/releases).

| Platform | Contents |
|----------|----------|
| macOS (Apple Silicon) | `rescale-int-gui.app` + `rescale-int` CLI |
| Linux (x64) | `rescale-int-gui.AppImage` + `rescale-int` CLI |
| Windows (x64) | `rescale-int-gui.exe` + `rescale-int.exe` (zip or MSI installer) |

**macOS:** Unzip, move `rescale-int-gui.app` to Applications. Copy `rescale-int` to a directory in your PATH for CLI usage.

**Linux:** Extract the tarball, `chmod +x` both binaries. Double-click the AppImage or run `./rescale-int --help` for CLI.

**Windows:** Unzip and run `rescale-int-gui.exe`, or use the MSI installer for Start Menu integration.

#### Option 2: Build from Source

```bash
# Clone the repository
git clone https://github.com/rescale-labs/Rescale_Interlink.git
cd Rescale_Interlink

# Install frontend dependencies
cd frontend && npm install && cd ..

# Build (includes FIPS 140-3 compliance automatically)
make build
```

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
4. Click **Test Connection**, then **Apply Changes**
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

**See [CLI_GUIDE.md](CLI_GUIDE.md) for complete command reference.**

### GUI Mode

The GUI provides six tabs:

1. **Setup**: API credentials, proxy settings, logging, daemon control
2. **Single Job**: Create and submit individual jobs (directory with tar options, local files, or remote files)
3. **PUR**: Parallel Upload Run - batch job pipeline
4. **File Browser**: Two-pane file manager for local and remote files
5. **Transfers**: Monitor active transfers with progress and controls
6. **Activity**: View real-time logs with filtering and search

### Daemon Control

The auto-download daemon automatically downloads completed jobs. Control it via CLI or GUI:

```bash
# Start daemon in background with IPC control
rescale-int daemon run --background --ipc --download-dir ./results

# Query running daemon status
rescale-int daemon status

# Stop running daemon
rescale-int daemon stop
```

In the GUI, the Setup tab provides start/stop/pause/resume buttons, status indicators, and "Scan Now" for immediate job checks. On **Windows MSI installs only**, a tray icon provides the same controls; the portable Windows distribution, macOS, and Linux do not include a tray — the main GUI and (on Windows) toast notifications fill that role.

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

```
rescale-int/
├── cmd/
│   ├── rescale-int/          # CLI binary entry point
│   └── rescale-int-tray/     # Windows system tray companion (MSI install only)
│
├── frontend/                 # Wails React frontend
│   ├── src/
│   │   ├── App.tsx           # Main app with tabs
│   │   ├── components/       # React components
│   │   └── stores/           # Zustand stores
│   └── wailsjs/              # Auto-generated Wails bindings
│
├── internal/
│   ├── cli/                  # CLI commands
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
│   ├── ipc/                  # Inter-process communication
│   ├── api/                  # Rescale API client
│   ├── events/               # Event bus system
│   ├── watch/                # Shared job watch/poll engine
│   ├── reporting/            # Error diagnostic reports
│   ├── platform/             # OS-specific (sleep inhibit)
│   ├── transfer/             # Transfer orchestration
│   │   ├── scan/             # Remote folder scanning
│   │   └── folder/           # Folder upload primitives
│   ├── core/                 # Core engine
│   └── pur/                  # PUR pipeline packages
│
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

# Production build (macOS arm64)
make build-darwin-arm64

# Cross-platform builds
make build-linux-amd64
make build-windows-amd64
```

Frontend development:
```bash
cd frontend && npm install && npm run build
```

After changing Go binding methods: `wails generate module`

See [CONTRIBUTING.md](CONTRIBUTING.md) for detailed development setup and guidelines.

---

## Troubleshooting

**Connection Issues:** Verify API key (`echo $RESCALE_API_KEY`), check proxy settings in Setup tab, try `system` proxy mode.

**Build Failures:** Clean and rebuild: `make clean && cd frontend && rm -rf node_modules && npm install && cd .. && make build`

---

## Known Limitations

- Compat mode covers 10 of rescale-cli's commands; software publisher (spub) commands are not yet supported
- No support for Rescale CFS or Publisher capabilities
- Terminal resize during CLI progress bars causes visual artifacts (transfers continue correctly)

---

## License

MIT License - see [CONTRIBUTING.md](CONTRIBUTING.md) for details

---

## Documentation

- **[CLI_GUIDE.md](CLI_GUIDE.md)** - Complete command-line reference with examples
- **[ARCHITECTURE.md](ARCHITECTURE.md)** - System design and code organization
- **[SECURITY.md](SECURITY.md)** - Security documentation (FIPS, proxy, logging, IPC)
- **[TESTING.md](TESTING.md)** - Test strategy and procedures
- **[CONTRIBUTING.md](CONTRIBUTING.md)** - Developer onboarding guide
- **[RELEASE_NOTES.md](RELEASE_NOTES.md)** - Version history and release details
- **[FEATURE_SUMMARY.md](FEATURE_SUMMARY.md)** - Comprehensive feature list with source references
- **[docs/LOG_LOCATIONS.md](docs/LOG_LOCATIONS.md)** - Where Interlink writes logs, per platform

---

**Version**: 4.9.4
**Status**: Production Ready
**Last Updated**: April 19, 2026
