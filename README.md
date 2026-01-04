# Rescale Interlink - Unified CLI and GUI for Rescale Platform

A unified tool combining comprehensive command-line interface and graphical interface for managing Rescale jobs, built with Go (backend) and Wails with React/TypeScript (GUI).

![alt text](./logo.png)

![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-blue)
![Go Version](https://img.shields.io/badge/go-1.24+-blue)
![FIPS](https://img.shields.io/badge/FIPS%20140--3-compliant-green)
![License](https://img.shields.io/badge/license-MIT-blue)
![Status](https://img.shields.io/badge/status-v4.0.8-green)

---

> **FIPS 140-3 Compliance (FedRAMP Moderate)**
>
> This tool is built with FIPS 140-3 compliant cryptography using Go 1.24's native FIPS module.
> To maintain FIPS compliance, **you must build with `GOFIPS140=latest`**:
>
> ```bash
> GOFIPS140=latest go build -o rescale-int ./cmd/rescale-int
> ```
>
> Or use `make build` which includes this automatically. Verify FIPS status with `rescale-int --version`.
>
> See [Go FIPS 140-3 Documentation](https://go.dev/doc/security/fips140) for details.

---

## Features

### Dual-Mode Architecture

- **CLI Mode** (default): Full-featured command-line interface
- **GUI Mode** (`--gui` flag): Interactive graphical interface built with Wails (React/TypeScript)
- **Single Binary**: One executable for both modes on all platforms

### CLI Features

- **Configuration Management**: Interactive setup with `config init`
- **File Operations**: Upload, download, list, delete files
- **Folder Management**: Create, list, bulk upload with connection reuse
  - **Folder Caching**: 99.8% reduction in API calls for folder operations
- **Job Operations**: Submit, monitor, control, download results
- **PUR Integration**: Batch job pipeline execution
- **Command Shortcuts**: Quick aliases (`upload`, `download`, `ls`)
- **Shell Completion**: Bash, Zsh, Fish, PowerShell support
- **Progress Tracking**: Multi-progress bars for concurrent operations
- **Streaming Encryption**: Per-part AES-256-CBC encryption during upload
- **Multi-part/Multi-threaded Transfers**: Dynamic thread allocation based on file size
- **Resume Support**: Resume interrupted downloads from exact byte position

### GUI Features (v4.0.0 - Wails)

The GUI has been rebuilt from the ground up using [Wails](https://wails.io/) with a React/TypeScript frontend:

- **Six-Tab Interface**:
  - **Setup**: Configuration management, API key setup, proxy settings, test connection
  - **Single Job**: Configure and submit individual jobs with template builder
  - **PUR (Parallel Upload Run)**: Batch job pipeline execution with directory scanning
  - **File Browser**: Two-pane local/remote file browser with upload/download
  - **Transfers**: Real-time transfer queue with progress, cancel, retry
  - **Activity**: Live log display with filtering and search

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
  - Auto-switch to Transfers tab on upload/download
  - Delete remote files/folders
  - Workspace name and ID displayed in header

- **Job Management**:
  - Template builder with searchable software/hardware selection
  - CSV/JSON/SGE job file load/save
  - Directory scanning with pattern matching
  - Real-time job status updates

---

## Recent Changes

**v4.0.0 (December 27, 2025) - Wails Migration + Bug Fixes:**
- **Complete GUI Rewrite**: Migrated from Fyne to Wails framework
  - React + TypeScript frontend with Vite build
  - Tailwind CSS for modern, responsive UI
  - Zustand for state management
  - TanStack Table for virtual scrolling
- **Bug Fixes**:
  - Fixed download "is a directory" error when file name conflicts with folder (auto-renames with `.file` suffix)
  - Fixed download path missing filename in GUI (was passing directory only)
  - Added visible checkboxes to file browser selection
  - Fixed version display (was showing "dev" instead of "v4.0.0")
- **GUI Improvements**:
  - Workspace name and ID displayed in header after connecting
  - Auto-switch to Transfers tab after starting upload/download
- **Code Quality**:
  - Removed PartTimer dead code from timing.go
  - Removed DefaultWalkOptions dead code from options.go
- **Preserved Backend**: All CLI commands and transfer logic unchanged
- **6-Tab Interface**: Setup, SingleJob, PUR, FileBrowser, Transfers, Activity
- **Smaller Binary**: 20MB macOS arm64 (down from 29MB with Fyne)
- **Modern File Dialogs**: Native OS file/folder pickers via Wails runtime
- **Real-time Events**: Progress and log updates via Wails event bridge

**v3.6.4 (December 25, 2025) - Services Layer (Final Fyne Version):**
- Prepared GUI-agnostic services layer for Wails migration
- TransferService, FileService, EventBus architecture

See [RELEASE_NOTES.md](RELEASE_NOTES.md) for complete version history.

---

## Quick Start

### Prerequisites

- Go 1.24 or later (minimum required for build)
- Node.js 18+ (for GUI development)
- macOS, Linux (with X11/Wayland), or Windows
- Rescale API key

### Installation

#### Option 1: Use Pre-built Binary

Download from releases page:
- **macOS ARM64**: `rescale-int-darwin-arm64`
- **macOS Intel**: `rescale-int-darwin-amd64`
- **Linux**: `rescale-int-linux-amd64`
- **Windows**: `rescale-int-windows-amd64.zip`

```bash
# macOS/Linux - make executable and move to PATH
chmod +x rescale-int
sudo mv rescale-int /usr/local/bin/
```

**Note**: On macOS, you may need to allow the app in System Settings > Privacy & Security, or run:
```bash
xattr -d com.apple.quarantine rescale-int
```

#### Option 2: Build from Source

**Prerequisites:**

```bash
# Install Wails CLI
go install github.com/wailsapp/wails/v2/cmd/wails@latest

# Install Node.js (if not present)
# macOS: brew install node
# Linux: apt install nodejs npm (or use nvm)
```

**Build:**

```bash
# Clone the repository
git clone https://github.com/rescale/rescale-int.git
cd rescale-int

# Install frontend dependencies
cd frontend && npm install && cd ..

# Build with FIPS 140-3 compliance
GOFIPS140=latest wails build -platform darwin/arm64

# Or use make
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

```bash
# Launch GUI
rescale-int --gui
```

1. Go to **Setup** tab
2. Configure:
   - API URL (default: https://platform.rescale.com)
   - API Key (environment variable, file, or direct input)
3. Click **Test Connection**
4. Click **Apply Changes**
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

```bash
# Start GUI
rescale-int --gui
```

The GUI provides six tabs:

1. **Setup**: Configure API credentials, proxy settings, transfer options
2. **Single Job**: Create and submit individual jobs with the template builder
3. **PUR**: Parallel Upload Run - batch job pipeline for multiple directories
4. **File Browser**: Two-pane file manager for local and remote files
5. **Transfers**: Monitor active transfers with progress and controls
6. **Activity**: View real-time logs with filtering and search

### Configuration

**CLI:**
```bash
# Interactive setup
rescale-int config init

# View configuration
rescale-int config show

# Test connection
rescale-int config test
```

**Environment variables:**
```bash
export RESCALE_API_KEY="your-api-key"
export RESCALE_API_URL="https://platform.rescale.com"
```

**Token file (recommended for scripts):**
```bash
# Create token file with restricted permissions
echo "your-api-key" > ~/.config/rescale/token
chmod 600 ~/.config/rescale/token

# Use token file
rescale-int --token-file ~/.config/rescale/token <command>
```

---

## Architecture

### High-Level Overview

```
+-------------------------------------------------------------+
|                   rescale-int v4.0.8                     |
|                  Unified CLI + GUI Binary                    |
+-------------------------------------------------------------+
|                                                              |
|  +------------------+             +------------------+       |
|  |    CLI Mode      |             |    GUI Mode      |       |
|  |    (default)     |             |   (--gui flag)   |       |
|  +------------------+             +------------------+       |
|  |                  |             |                  |       |
|  | * config cmds    |             | * Wails Runtime  |       |
|  | * files cmds     |             | * React Frontend |       |
|  | * folders cmds   |             | * 6 Tabs         |       |
|  | * jobs cmds      |             | * Event Bridge   |       |
|  | * pur cmds       |             |                  |       |
|  +--------+---------+             +--------+---------+       |
|           |                                |                 |
|           +---------------+----------------+                 |
|                           |                                  |
|                  +--------v--------+                         |
|                  |  Shared Core    |                         |
|                  +-----------------+                         |
|                  | * Config        |                         |
|                  | * API Client    |                         |
|                  | * Engine        |                         |
|                  | * Services      |                         |
|                  | * Cloud I/O     |                         |
|                  +-----------------+                         |
+-------------------------------------------------------------+
```

### Key Components

**CLI Interface** (`internal/cli/`)
- Cobra-based command structure
- Config, Files, Folders, Jobs, PUR commands
- Progress tracking with mpb progress bars
- Shell completion support

**GUI Interface** (`internal/wailsapp/` + `frontend/`)
- Wails v2 application with React/TypeScript frontend
- Go bindings for all backend operations
- Event bridge for real-time updates
- Six-tab interface (Setup, SingleJob, PUR, FileBrowser, Transfers, Activity)

**Services Layer** (`internal/services/`)
- TransferService: Upload/download orchestration
- FileService: File/folder CRUD operations
- GUI-agnostic design for CLI/GUI reuse

**Cloud Backend** (`internal/cloud/`)
- S3 and Azure blob storage support
- Streaming encryption (per-part AES-256-CBC)
- Multi-part/multi-threaded transfers
- Resume support for interrupted downloads

**Core Engine** (`internal/core/`)
- Configuration management
- API client with rate limiting
- Job validation and state management

### Project Structure

```
rescale-int/
├── cmd/rescale-int/          # Main entry point
│   └── main.go               # CLI/GUI router
│
├── frontend/                 # Wails React frontend (v4.0.0)
│   ├── src/
│   │   ├── App.tsx           # Main app with tabs
│   │   ├── components/       # React components
│   │   │   ├── tabs/         # Tab implementations
│   │   │   └── widgets/      # Reusable widgets
│   │   └── stores/           # Zustand stores
│   ├── wailsjs/              # Auto-generated Wails bindings
│   └── package.json
│
├── internal/
│   ├── cli/                  # CLI commands
│   │   ├── root.go
│   │   ├── files.go
│   │   ├── folders.go
│   │   ├── jobs.go
│   │   └── pur.go
│   │
│   ├── wailsapp/             # Wails Go bindings (v4.0.0)
│   │   ├── app.go            # Main Wails app
│   │   ├── config_bindings.go
│   │   ├── transfer_bindings.go
│   │   ├── file_bindings.go
│   │   ├── job_bindings.go
│   │   └── event_bridge.go
│   │
│   ├── services/             # GUI-agnostic services
│   │   ├── transfer_service.go
│   │   └── file_service.go
│   │
│   ├── cloud/                # Cloud storage backends
│   │   ├── upload/           # S3/Azure uploaders
│   │   ├── download/         # S3/Azure downloaders
│   │   └── providers/        # Provider implementations
│   │
│   ├── api/                  # Rescale API client
│   ├── events/               # Event bus system
│   ├── core/                 # Core engine
│   └── pur/                  # PUR pipeline packages
│
├── _archive_fyne_gui/        # Archived Fyne code (reference)
│
├── CLI_GUIDE.md              # Complete CLI reference
├── ARCHITECTURE.md           # System architecture
├── CONTRIBUTING.md           # Contribution guide
└── README.md                 # This file
```

---

## Development

### Building

```bash
# Development with hot reload
wails dev -appargs "--gui"

# Production build (macOS arm64)
GOFIPS140=latest wails build -platform darwin/arm64

# Cross-platform builds (use Makefile)
make build-darwin-arm64
make build-darwin-amd64
make build-linux-amd64
make build-windows-amd64
```

### Frontend Development

```bash
# Install dependencies
cd frontend && npm install

# Build frontend
npm run build

# Type checking
npm run typecheck
```

### Regenerating Wails Bindings

After changing Go binding methods:
```bash
wails generate module
```

---

## Performance

### System Requirements

**Operating System:**
- **macOS**: 10.15 (Catalina) or later
- **Windows**: Windows 10 or later (64-bit)
- **Linux**: GLIBC 2.27+
  - RHEL/CentOS/Rocky/Alma 8+
  - Ubuntu 18.04+
  - Debian 10+

**Resources:**
- **RAM**: 50-100MB typical
- **CPU**: Minimal (event-driven)
- **Disk**: ~20MB binary (macOS arm64)
- **Network**: As needed for API calls

---

## Troubleshooting

### GUI Won't Start

**Linux**: Ensure WebKitGTK is installed:
```bash
# Ubuntu/Debian
sudo apt install libwebkit2gtk-4.1-0

# RHEL/CentOS (use webkit2_41 tag)
sudo dnf install webkit2gtk4.1
```

**macOS**: Allow unsigned app in Security settings

**Windows**: WebView2 runtime should be bundled (or installed automatically)

### Connection Issues

1. Verify API key: `echo $RESCALE_API_KEY`
2. Check proxy settings in Setup tab
3. Try `system` proxy mode

### Build Failures

```bash
# Clean and rebuild
go clean -cache
cd frontend && rm -rf node_modules dist && npm install && npm run build && cd ..
wails build
```

---

## Known Limitations

- No support for Rescale CFS or Publisher capabilities
- Not drop-in compatible with Rescale CLI (different command structure)
- Terminal resize during CLI progress bars causes visual artifacts (transfers continue correctly)

---

## License

MIT License - see [CONTRIBUTING.md](CONTRIBUTING.md) for details

---

## Documentation

- **[CLI_GUIDE.md](CLI_GUIDE.md)** - Complete command-line reference with examples
- **[ARCHITECTURE.md](ARCHITECTURE.md)** - System design and code organization
- **[TESTING.md](TESTING.md)** - Test strategy and procedures
- **[CONTRIBUTING.md](CONTRIBUTING.md)** - Developer onboarding guide
- **[RELEASE_NOTES.md](RELEASE_NOTES.md)** - Version history and release details
- **[MULTI-PLATFORM_WAILS_PACKAGING_GUIDE.md](MULTI-PLATFORM_WAILS_PACKAGING_GUIDE.md)** - Build guide for all platforms

---

**Version**: 4.0.8
**Status**: Production Ready
**Last Updated**: January 3, 2026
