# Rescale Interlink - Unified CLI and GUI for Rescale Platform

A unified tool combining comprehensive command-line interface and graphical interface for managing Rescale jobs, built with Go (backend) and Wails with React/TypeScript (GUI).

![alt text](./logo.png)

![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-blue)
![Go Version](https://img.shields.io/badge/go-1.24+-blue)
![FIPS](https://img.shields.io/badge/FIPS%20140--3-compliant-green)
![License](https://img.shields.io/badge/license-MIT-blue)
![Status](https://img.shields.io/badge/status-v4.4.3-green)

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

- **CLI Binary** (`rescale-int`): Full-featured command-line interface
- **GUI Binary** (`rescale-int-gui`): Interactive graphical application built with Wails (React/TypeScript)
- **Platform Packages**: Each platform has both binaries packaged together (AppImage, zip, or MSI)

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

**v4.4.3 (January 25, 2026) - Daemon Startup UX + Service Mode Fixes:**
- **P0 Critical Fixes:**
  - **Windows Service Entrypoint**: `daemon run` now properly detects Windows Service context and delegates to `RunAsMultiUserService()` - fixes Error 1053 on service start
  - **Windows Per-User Config Paths**: Multi-user service now correctly finds `daemon.conf` at `AppData\Roaming\Rescale\Interlink\daemon.conf`
  - **Windows Stale PID Handling**: `IsDaemonRunning()` now validates PID using `windows.OpenProcess()` - stale PID files are cleaned up automatically
- **P1 UX Improvements:**
  - **Tray Immediate Error Feedback**: Errors now update UI immediately (not on 5-second poll cycle)
  - **GUI Inline Guidance**: Visible amber message when auto-download is disabled explains how to enable
- **P2 Polish:**
  - **Unified Path Resolution**: New `internal/pathutil` package with shared `ResolveAbsolutePath()` function used by CLI, GUI, and Tray
  - **Relaxed Tray Preflight**: Replaced strict parent directory check with `os.MkdirAll()` - supports new paths
  - **macOS/Linux Auto-Start Docs**: Removed `--background` flag from launchd/systemd examples (conflicts with process managers)

**v4.3.7 (January 14, 2026) - Daemon Logging Improvements:**
- **Silent filtering for Auto Download**: Jobs with "Auto Download = not set" or "disabled" are now filtered silently
- **Optimized API calls**: Custom field is checked FIRST, before checking downloaded tag
- **Improved scan summary**: Shows `filtered` count separately from `skipped` count

**v4.2.1 (January 9, 2026) - Enhanced Eligibility Configuration:**
- **New `daemon.conf` file**: Single INI file for all daemon settings (replaces scattered config)
- **CLI config commands**: `daemon config show|path|edit|set|init` for managing daemon settings
- **Config file + CLI flags**: Daemon reads from config file, CLI flags override config values
- **Windows tray "Configure..."**: Opens GUI directly to daemon configuration
- **Cross-platform consistency**: Same config format and behavior on Mac, Linux, and Windows

**v4.1.0 (January 7, 2026) - Cross-Platform Daemon Control:**
- **GUI Daemon Control**: Start, stop, pause, resume, and monitor the auto-download daemon from the GUI
- **Unix IPC**: Domain socket communication (`~/.config/rescale/interlink.sock`) for Mac/Linux
- **Daemon Background Mode**: New `--background` and `--ipc` flags for daemon command
- **Windows Service Detection**: GUI shows "Managed by Windows Service" when applicable
- **Setup Tab Enhancements**: Status indicators, control buttons, and "Scan Now" functionality
- **New Files**: `daemon_bindings.go`, `ipc_handler.go`, `daemonize_unix.go`

**v4.0.8 (January 6, 2026) - Unified API Key Handling:**
- Unified API key handling for auto-download feature
- Windows Service/Tray fixes
- Improved credential management

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

#### Option 1: Use Pre-built Packages

Download from [GitHub Releases](https://github.com/rescale-labs/Rescale_Interlink/releases):

| Platform | Package | Contents |
|----------|---------|----------|
| macOS (Apple Silicon) | `rescale-interlink-v4.4.3-darwin-arm64.zip` | `rescale-int-gui.app` |
| Linux (x64) | `rescale-interlink-v4.4.3-linux-amd64.tar.gz` | `rescale-int-gui.AppImage` + `rescale-int` CLI |
| Windows (x64) | `rescale-interlink-v4.4.3-windows-amd64.zip` | `rescale-int-gui.exe` + `rescale-int.exe` |
| Windows Installer | `RescaleInterlink-v4.4.3.msi` | Full installer with Start Menu integration |

**macOS:**
```bash
# Unzip and move app to Applications
unzip rescale-interlink-v4.4.3-darwin-arm64.zip
mv rescale-int-gui.app /Applications/

# First run: allow in System Settings > Privacy & Security
# Or remove quarantine:
xattr -d com.apple.quarantine /Applications/rescale-int-gui.app
```

**Linux:**
```bash
# Extract and make executable
tar -xzf rescale-interlink-v4.4.3-linux-amd64.tar.gz
chmod +x rescale-int-gui.AppImage rescale-int

# Run GUI (double-click or):
./rescale-int-gui.AppImage

# Use CLI:
./rescale-int --help
```

**Windows:**
```powershell
# Unzip and run GUI:
Expand-Archive rescale-interlink-v4.4.3-windows-amd64.zip
.\rescale-int-gui.exe

# Or install MSI for Start Menu integration
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
# macOS - double-click the app or:
open /Applications/rescale-int-gui.app

# Linux - double-click AppImage or:
./rescale-int-gui.AppImage

# Windows - double-click exe or:
.\rescale-int-gui.exe
```

1. Go to **Setup** tab
2. Configure:
   - API URL (default: https://platform.rescale.com)
   - API Key (environment variable, file, or direct input)
3. Click **Test Connection**
4. Click **Apply Changes**
5. (Optional) Start the auto-download daemon from Setup tab
6. Ready to submit jobs!

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
# Launch GUI application
# macOS:
open /Applications/rescale-int-gui.app

# Linux:
./rescale-int-gui.AppImage

# Windows:
.\rescale-int-gui.exe
```

The GUI provides six tabs:

1. **Setup**: Configure API credentials, proxy settings, transfer options, **daemon control**
2. **Single Job**: Create and submit individual jobs with the template builder
3. **PUR**: Parallel Upload Run - batch job pipeline for multiple directories
4. **File Browser**: Two-pane file manager for local and remote files
5. **Transfers**: Monitor active transfers with progress and controls
6. **Activity**: View real-time logs with filtering and search

### Daemon Control (v4.1.0)

The auto-download daemon automatically downloads completed jobs. Control it via CLI or GUI:

**CLI:**
```bash
# Start daemon in background with IPC control
rescale-int daemon run --background --ipc --download-dir ./results

# Query running daemon status
rescale-int daemon status

# Stop running daemon
rescale-int daemon stop
```

**GUI (Setup Tab):**
- Status indicator: green (running), yellow (paused), gray (stopped)
- Start/Stop/Pause/Resume buttons
- "Scan Now" button for immediate job check
- Shows uptime, version, jobs downloaded when running

**Windows Tray (MSI only):**
- Right-click tray icon for menu
- Start Service / Pause / Resume / Trigger Scan
- Open Interlink to launch GUI

### Auto-Start on Login (Mac/Linux)

On **Windows with MSI installer**, the daemon starts automatically as a Windows Service.

On **Mac and Linux**, you can configure auto-start manually using the system's init system:

<details>
<summary><b>macOS (launchd)</b></summary>

Create `~/Library/LaunchAgents/com.rescale.interlink.daemon.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.rescale.interlink.daemon</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/bin/rescale-int</string>
        <string>daemon</string>
        <string>run</string>
        <string>--download-dir</string>
        <string>/Users/USERNAME/Downloads/rescale-jobs</string>
        <string>--ipc</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/Users/USERNAME/Library/Logs/rescale-interlink.log</string>
    <key>StandardErrorPath</key>
    <string>/Users/USERNAME/Library/Logs/rescale-interlink.error.log</string>
</dict>
</plist>
```

**Note:** Do NOT use `--background` with launchd. Launchd expects the process to stay in the foreground;
`--background` forks and exits, causing launchd to think the daemon crashed.

**Commands:**
```bash
# Replace USERNAME with your actual username in the plist file

# Install (enable auto-start)
launchctl load ~/Library/LaunchAgents/com.rescale.interlink.daemon.plist

# Uninstall (disable auto-start)
launchctl unload ~/Library/LaunchAgents/com.rescale.interlink.daemon.plist

# Check status
launchctl list | grep rescale
```
</details>

<details>
<summary><b>Linux (systemd)</b></summary>

Create `~/.config/systemd/user/rescale-interlink.service`:

```ini
[Unit]
Description=Rescale Interlink Auto-Download Daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/rescale-int daemon run --download-dir %h/Downloads/rescale-jobs --ipc
Restart=on-failure
RestartSec=10

[Install]
WantedBy=default.target
```

**Note:** Do NOT use `--background` with systemd. Systemd expects `Type=simple` services to stay in the foreground;
`--background` forks and exits, causing systemd to think the daemon crashed.

**Commands:**
```bash
# Install (enable auto-start)
systemctl --user daemon-reload
systemctl --user enable rescale-interlink
systemctl --user start rescale-interlink

# Check status
systemctl --user status rescale-interlink

# View logs
journalctl --user -u rescale-interlink -f

# Disable auto-start
systemctl --user disable rescale-interlink
```
</details>

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
+------------------------------------------------------------------+
|                    Rescale Interlink v4.4.3                       |
+------------------------------------------------------------------+
|                                                                   |
|  +--------------------+               +--------------------+      |
|  |   CLI Binary       |               |   GUI Binary       |      |
|  |   (rescale-int)    |               |   (rescale-int-gui)|      |
|  +--------------------+               +--------------------+      |
|  |                    |               |                    |      |
|  | * config cmds      |               | * Wails Runtime    |      |
|  | * files cmds       |               | * React Frontend   |      |
|  | * folders cmds     |               | * 6 Tabs           |      |
|  | * jobs cmds        |               | * Event Bridge     |      |
|  | * pur cmds         |               | * Daemon Control   |      |
|  | * daemon cmds      |               |                    |      |
|  +--------+-----------+               +--------+-----------+      |
|           |                                    |                  |
|           +----------------+-------------------+                  |
|                            |                                      |
|                   +--------v--------+                             |
|                   |  Shared Core    |                             |
|                   +-----------------+                             |
|                   | * Config        |                             |
|                   | * API Client    |                             |
|                   | * Engine        |                             |
|                   | * Services      |                             |
|                   | * Cloud I/O     |                             |
|                   | * IPC Layer     |                             |
|                   +-----------------+                             |
|                            |                                      |
|                   +--------v--------+                             |
|                   | Daemon Process  |  (separate process)         |
|                   +-----------------+                             |
|                   | * Auto-download |                             |
|                   | * IPC Server    |                             |
|                   | * Job polling   |                             |
|                   +-----------------+                             |
+------------------------------------------------------------------+
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

**Daemon** (`internal/daemon/`)
- Auto-download background service
- IPC handler for control commands
- Unix daemonization (fork, setsid, PID file)

**IPC Layer** (`internal/ipc/`)
- Unix domain sockets (Mac/Linux): `~/.config/rescale/interlink.sock`
- Named pipes (Windows): `\\.\pipe\rescale-interlink`
- JSON protocol for status, pause, resume, scan, shutdown commands

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
│   ├── daemon/               # Auto-download daemon (v4.1.0)
│   │   ├── daemon.go         # Main daemon logic
│   │   ├── daemonize_unix.go # Unix fork/setsid
│   │   └── ipc_handler.go    # IPC command handler
│   │
│   ├── ipc/                  # Inter-process communication
│   │   ├── client_unix.go    # Unix domain socket client
│   │   ├── server_unix.go    # Unix domain socket server
│   │   └── messages.go       # IPC protocol messages
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

**Version**: 4.4.3
**Status**: Production Ready
**Last Updated**: January 25, 2026
