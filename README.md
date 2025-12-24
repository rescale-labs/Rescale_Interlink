# Rescale Interlink - Unified CLI and GUI for Rescale Platform

A unified tool combining comprehensive command-line interface and graphical interface for managing Rescale jobs, built with Go and Fyne.

![alt text](./logo.png)

![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-blue)
![Go Version](https://img.shields.io/badge/go-1.24+-blue)
![FIPS](https://img.shields.io/badge/FIPS%20140--3-compliant-green)
![License](https://img.shields.io/badge/license-MIT-blue)
![Status](https://img.shields.io/badge/status-v3.6.0-green)

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

### ✅ Dual-Mode Architecture

- **CLI Mode** (default): Full-featured command-line interface
- **GUI Mode** (`--gui` flag): Interactive graphical interface
- **Single Binary**: One executable for both modes on all platforms

### ✅ CLI Features

- **Configuration Management**: Interactive setup with `config init`
- **File Operations**: Upload, download, list, delete files
- **Folder Management**: Create, list, bulk upload with connection reuse
  - **Folder Caching**: 99.8% reduction in API calls for folder operations
- **Job Operations**: Submit, monitor, control, download results
- **PUR Integration**: Batch job pipeline execution
- **Command Shortcuts**: Quick aliases (`upload`, `download`, `ls`)
- **Shell Completion**: Bash, Zsh, Fish, PowerShell support
- **Progress Tracking**: Multi-progress bars for concurrent operations

### ✅ GUI Features

- **Configuration Management**: Visual UI for all settings
- **API Integration**: Test connections, validate credentials
- **Job Validation (Plan)**: Comprehensive CSV and job spec validation
- **State Management**: Load and view job states from previous runs
- **Job Monitoring**: Automatic polling of Rescale job statuses (30s interval)
- **Real-time Logging**: Live log display with filtering and search
- **Event System**: Professional pub/sub architecture for UI updates
- **Pipeline Execution**: Full integration with job submission
- **File Browser** (v2.6.0, enhanced v3.6.0): Two-pane local/remote file browser with:
  - Delete functionality for remote files/folders (local delete via OS file manager)
  - Search/filter by filename
  - Pagination support (default 40 items/page, configurable 20-200)
  - Transfer rate display for uploads/downloads (e.g., "2.5 MB/s")
  - Unified StatusBar for operation feedback
  - Dialog helpers for confirmations and error display


### Recent Improvements

**v3.6.0 (December 23, 2025) - Architectural Foundation:**
- **New `internal/localfs/` Package**: Unified local filesystem abstraction
  - `IsHidden()`, `IsHiddenName()` - consolidated hidden file detection (was duplicated 9 times)
  - `ListDirectory()`, `Walk()`, `WalkFiles()` - unified directory operations
- **Hidden File Detection Consolidation**: Migrated all `strings.HasPrefix(name, ".")` patterns to `localfs.IsHidden()`
- **GUI Page Size Consistency**: Remote browser now uses default page size (25) for consistency
- **Windows Download Timing**: Added `RESCALE_TIMING=1` instrumentation to diagnose end-of-download slowness
  - Hypothesis: `verifyChecksum()` reads entire file after download for SHA-512

**v3.5.0 (December 22, 2025) - File Browser Robustness:**
- **Definitive Fix for Leftover Entries Bug**: Comprehensive rewrite of list rendering
  - **Typed Row Template**: `fileRowTemplate` struct eliminates brittle `Objects[...]` indexing
  - **Total Overwrite + Refresh**: All fields set in all branches, explicit `.Refresh()` on every `canvas.Text` and `widget.RichText`
  - **Generation Gating**: `viewGeneration` counter detects and blanks stale recycled rows
  - **UnselectAll on Navigation**: Clears selection highlight persistence between folders
  - **Double Refresh Pattern**: Handles scroll/length edge cases in Fyne's widget.List
  - **Safety Guard**: `onItemTapped` validates item ID exists before taking action
- **Local Delete Button Removed**: Simplified UI - local file deletion managed by OS file manager
- **Debug Mode**: Set `RESCALE_GUI_DEBUG=1` for detailed file browser state logging

**v3.4.13 (December 22, 2025) - Windows Mesa Auto-Extract:**
- **App-Local Mesa Deployment**: Mesa DLLs bundled alongside EXE for automatic software rendering
  - Windows loads DLLs from EXE directory before System32 (when not in KnownDLLs)
  - No configuration needed - "just works" on Windows VMs/RDP sessions
- **Self-Extract + Re-Exec**: If user only copies EXE (forgetting DLLs), app auto-extracts and restarts
  - Embedded DLLs extract to EXE directory on first run
  - Process re-execs so new DLLs load correctly
  - Works transparently - user sees brief restart at most
- **Clear Error Messages**: If extraction fails (read-only location), actionable error with fix instructions
- **Simplified Mesa Code**: Removed complex preload logic that couldn't work after System32 loaded

**v3.4.12 (December 22, 2025) - File Browser Self-Healing Architecture:**
- **Self-Healing View State**: File browser uses `viewDirty` flag pattern for robust state management
  - Filter/sort operations set `viewDirty = true`, recompute happens lazily on next read
  - Missing invalidation = performance issue, not bug (self-healing)
  - `filterQuery` is source of truth for filter state (not `filteredItems != nil`)
- **Debug Logging**: Set `RESCALE_GUI_DEBUG=1` to trace file browser state changes

**v3.4.8 (December 19, 2025) - Deep Performance Optimization + Windows Mesa Variants:**
- **GUI Performance Optimizations**: Comprehensive performance improvements across all GUI components
  - FileListWidget: Removed 4 redundant Refresh() calls, precomputed lowercase for sorting (10x fewer allocations), added O(1) ID-to-index lookups
  - FileBrowserTab: Batched lock acquisition, removed redundant progress bar refresh calls
  - JobsTab: Added O(1) job name index lookups for real-time updates
  - ActivityTab: Cached formatted log entries for O(1) filtering
- **Windows Build Variants**: Two Windows binaries now available
  - Standard build (smaller, requires GPU): `rescale-int-windows-amd64.zip`
  - Mesa build (larger, software rendering for VMs/RDP): `rescale-int-windows-amd64-mesa.zip`
- Builds on v3.4.7 network filesystem fixes (using cached DirEntry metadata)

**v3.4.5 (December 18, 2025) - Accelerated Scroll:**
- **AcceleratedScroll Widget**: Custom scroll container with 3x scroll speed
  - Built from scratch to properly receive scroll events (fixes Fyne issue #775)
  - All 10 GUI scroll containers now use accelerated scrolling
  - Includes draggable scroll bars with theme-aware rendering

**v3.4.4 (December 17, 2025) - GUI Usability + Configuration Fixes:**
- **Software Workflow Improvements**:
  - Analysis dropdown now shows "Name (Code)" format for better readability
  - Version dropdown populated from API when software selected
  - Core count dropdown shows valid options per hardware type (fetched from API)
- **GUI Enhancements**:
  - Version and FIPS status now displayed in window title
  - Fixed file selection count not updating (async callback bug)
  - RHEL 9 Wayland auto-detection forces X11 for consistent window decorations
- **Critical GUI Freeze Fix**: Fixed mutex contention in `engine.UpdateConfig()` that caused extended UI freezes when proxy was configured
- **Configuration Path Fixes**: Fixed bugs preventing `config init` workflow from working properly
  - Token filename mismatch: `config init` now saves to correct filename (`token` instead of `rescale_token`)
  - Config path mismatch: Commands now use `~/.config/rescale/config.csv` instead of local `config.csv`
  - New config directory: Changed from `~/.config/rescale-int/` to `~/.config/rescale/` for consistency
- **Proxy Improvements**: Basic auth proxy now respects `ProxyWarmup` flag; reduced timeout from 30s to 15s
- **Linux Requirements**: Documented GLIBC 2.27+ requirement (RHEL/CentOS 8+, Ubuntu 18.04+)

**v3.4.2 (December 16, 2025) - Dynamic Thread Reallocation + Code Cleanup:**
- **Dynamic Thread Reallocation**: Transfers can now acquire additional threads mid-flight
  - New `TryAcquire()` and `GetMaxForFileSize()` methods in resource manager
  - Background scaler goroutines check for available threads every 500ms
  - Improves throughput when threads become available during large transfers
- **GUI Thread Safety**: Fixed Fyne thread safety errors in `setup_tab.go` applyConfig
- **Code Cleanup**: Removed dead code, stale comments, and version annotations per Go community standards
- **Documentation**: Fixed internal inconsistencies in ARCHITECTURE.md

**v3.4.0 (December 12, 2025) - Background Service Mode + GUI Stability:**
- **Background Service Mode**: New `daemon` command for auto-downloading completed jobs
  - `rescale-int daemon run --download-dir ./results` - Start daemon
  - `rescale-int daemon status` - Check state and statistics
  - `rescale-int daemon list` - List downloaded/failed jobs
  - `rescale-int daemon retry --all` - Retry failed downloads
  - Job name filtering: `--name-prefix`, `--name-contains`, `--exclude`
  - Configurable poll interval (default 5 minutes)
  - Run once mode for cron: `--once`
- **GUI Stability Fixes**: Thread safety improvements for Fyne framework
  - Fixed config save blocking/lockup during proxy warmup
  - Added panic recovery to transfer goroutines
  - Async config apply with progress dialog
- **My Jobs Upload Button**: Upload disabled when viewing My Jobs (only available in My Library)
- **Proxy Launch Fix**: GUI now launches correctly when proxy is configured without saved password

**v3.2.0 (November 30, 2025) - GUI Improvements & Bug Fixes:**
- **JSON Job Template Support**: Load from JSON and Save as JSON buttons in Single Job Tab
- **SearchableSelect Fix**: Dropdown no longer appears when value is set programmatically
- **Fyne Thread Safety**: Fixed "Error in Fyne call thread" warnings in Activity Tab
- **Hardware Scan UX**: Scan button now enables when any valid software code is entered
- **Dialog Sizing**: Configure New Job dialog enlarged (900×800) with proper text wrapping

**v3.0.1 (November 28, 2025) - Streaming Encryption (Major Release):**
- **Streaming Encryption**: Per-part AES-256-CBC encryption during upload (no temp file)
- **Eliminates 2x Disk Usage**: No longer creates temporary encrypted file for large uploads
- **FIPS 140-3 Mandatory**: Non-FIPS binaries now refuse to run (exit code 2)
- **--pre-encrypt Flag**: Legacy encryption mode for backward compatibility with Python client
- **Format Detection**: Downloads automatically detect streaming vs legacy format
- **HKDF-SHA256**: Per-part key derivation using Go 1.24 standard library (FIPS validated)
- **Bug Fix**: Fixed single-part upload partSize metadata (affected files 16-99MB)

**v2.7.0 (November 26, 2025) - FIPS 140-3 Compliance + Security:**
- **FIPS 140-3 Compliance**: Built with Go 1.24's native FIPS module for FedRAMP Moderate
- **Security**: Fixed vulnerability in API key display (now shows length only, not partial key)
- **Security**: Updated golang.org/x/crypto to fix SSH vulnerabilities
- **API Key Precedence**: Clear warnings when multiple API key sources are detected
  - Priority: `--api-key` > `RESCALE_API_KEY` env > `--token-file` > default token file
- **License**: Updated to standard MIT license

**v2.6.0 (November 26, 2025) - GUI Enhancements:**
- **File Browser Improvements**: Added delete buttons for local and remote files/folders
- **StatusBar Component**: Unified status display with level-based icons and activity spinner
- **Dialog Helpers**: User-friendly error messages and operation result summaries
- **Search/Filter**: Type to filter files/folders by name in File Browser
- **Pagination**: Default 40 items/page, configurable range 20-200
- **Transfer Rate Display**: Real-time transfer speed shown during uploads/downloads
- **Visual Refinements**: Button spacing, filename truncation, white list backgrounds
- **Code Cleanup**: Removed 16 dead code files from cmd/rescale-int/

**v2.5.0 (November 23, 2025) - CLI Usability + Conflict Handling:**
- **Short Flags**: Added single-letter flags to all CLI commands (e.g., `-s` for `--search`, `-j` for `--job-id`)
- **Hardware List Default**: Now shows active hardware only by default; use `-a/--all` to include inactive types
- **Conflict Handling**: Comprehensive duplicate/conflict detection for uploads and downloads
  - File uploads: `--check-duplicates`, `--skip-duplicates`, `--allow-duplicates`
  - Folder uploads: `--skip-folder-conflicts`, `--merge-folder-conflicts`
  - Folder downloads: `--skip`, `--overwrite`, `--merge`
- **Dry-Run Mode**: Preview uploads/downloads without transferring (`--dry-run`)
- **Aligned with rescale-cli**: Short flags follow rescale-cli conventions where applicable

**v2.4.9 (November 22, 2025) - Security Improvements + Bug Fixes:**
- **Security**: Removed credential persistence from config files (API keys, proxy passwords no longer saved)
- **Security**: Added `--token-file` flag for secure API key storage
- **Security**: Added secure password prompting for proxy authentication
- **Bug Fix**: Fixed pipeline resource leak (defer-in-loop causing thread pool exhaustion)
- **Bug Fix**: Fixed S3 context leak in multipart uploads
- **Bug Fix**: Enhanced PKCS7 padding verification (defense-in-depth)

**v2.4.8 (November 20, 2025) - Massive Download Performance Improvement:**
- **99% reduction in API overhead** for job downloads (from ~188s to <1s for 289 files)
- Eliminated GetFileInfo API calls by using cached metadata from v2 ListJobFiles
- Downloads now limited by S3/Azure transfer speed, not API calls

**v2.4.7 (November 20, 2025) - v2 API Support:**
- Switched job file listing to v2 endpoint (12.5x faster rate limit)
- Smart API routing based on endpoint type

**v2.3.0 (November 17, 2025) - Critical Bug Fixes:**
- Fixed Resume Logic for PKCS7 padding
- Added Decryption Progress Feedback
- Fixed Progress Bar Corruption

See [RELEASE_NOTES.md](RELEASE_NOTES.md) for complete details.

## Quick Start

### Prerequisites

- Go 1.24 or later (minimum required for build)
- macOS, Linux (with X11/Wayland), or Windows
- Rescale API key

### Installation

#### Option 1: Use Pre-built Binary

Download from releases page:
- **macOS ARM64**: `rescale-int-darwin-arm64`
- **macOS Intel**: `rescale-int-darwin-amd64`
- **Linux**: `rescale-int-linux-amd64`
- **Windows**: Two variants available:
  - `rescale-int-windows-amd64.zip` - Standard (smaller, requires GPU)
  - `rescale-int-windows-amd64-mesa.zip` - Mesa (larger, software rendering for VMs/RDP)

```bash
# macOS/Linux - make executable and move to PATH
chmod +x rescale-int
sudo mv rescale-int /usr/local/bin/
```

**Note**: On macOS, you may need to allow the app in System Settings → Privacy & Security, or run:
```bash
xattr -d com.apple.quarantine rescale-int
```

#### Option 2: Build from Source

**Linux Prerequisites (required for GUI/Fyne):**

The GUI uses [Fyne](https://fyne.io/), which requires X11 development libraries on Linux:

```bash
# RHEL/CentOS/Rocky/Alma:
sudo dnf install -y libX11-devel libXcursor-devel libXrandr-devel libXinerama-devel libXi-devel libXxf86vm-devel mesa-libGL-devel mesa-libGLU-devel

# Ubuntu/Debian:
sudo apt-get install -y libx11-dev libxcursor-dev libxrandr-dev libxinerama-dev libxi-dev libxxf86vm-dev libgl1-mesa-dev libglu1-mesa-dev
```

**Build:**

```bash
# Clone the repository
git clone https://github.com/rescale/rescale-int.git
cd rescale-int

# Build (with FIPS 140-3 compliance)
GOFIPS140=latest go build -o rescale-int ./cmd/rescale-int

# Run
./rescale-int
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

### Optional: Enable Tab Completion

Tab completion lets you press **Tab** to auto-complete commands and flags, making the CLI much faster to use.

**Quick Setup (choose your shell):**

<details>
<summary><b>macOS with zsh</b> (default on modern Macs)</summary>

```bash
# 1. Create completions directory
mkdir -p ~/.zsh/completions

# 2. Generate completion script
rescale-int completion zsh > ~/.zsh/completions/_rescale-int

# 3. Add to ~/.zshrc (if not already there)
echo 'fpath=(~/.zsh/completions $fpath)' >> ~/.zshrc
echo 'autoload -Uz compinit && compinit' >> ~/.zshrc

# 4. Restart your terminal (or run: source ~/.zshrc)
```

**Test it:**
```bash
rescale-int j<Tab>              # Completes to "jobs"
rescale-int jobs d<Tab>         # Shows: download, delete
rescale-int jobs download --<Tab>  # Shows all available flags
```
</details>

<details>
<summary><b>macOS with bash</b></summary>

```bash
# 1. Install bash-completion (if not already installed)
brew install bash-completion@2

# 2. Generate completion script
rescale-int completion bash > $(brew --prefix)/etc/bash_completion.d/rescale-int

# 3. Add to ~/.bash_profile (if not already there)
echo '[[ -r "$(brew --prefix)/etc/profile.d/bash_completion.sh" ]] && . "$(brew --prefix)/etc/profile.d/bash_completion.sh"' >> ~/.bash_profile

# 4. Restart your terminal
```
</details>

<details>
<summary><b>Linux with bash</b></summary>

```bash
# 1. Install bash-completion (if not already installed)
# Ubuntu/Debian:
sudo apt-get install bash-completion
# RHEL/CentOS:
sudo yum install bash-completion

# 2. Generate completion script
rescale-int completion bash | sudo tee /etc/bash_completion.d/rescale-int

# 3. Restart your terminal
```
</details>

For other shells (fish, PowerShell) or detailed instructions, run:
```bash
rescale-int completion --help
```

## Usage

### CLI Mode (Default)

**Upload files:**
```bash
rescale-int upload model.tar.gz input.dat
```

**Bulk upload directory (5-10x faster with connection reuse):**
```bash
rescale-int folders upload-dir ./simulation_data --parent-id abc123
```

**Download files:**
```bash
# Download single file
rescale-int download <file-id> --outdir ./downloads

# Download multiple files concurrently
rescale-int files download <id1> <id2> <id3> --max-concurrent 5

# Download with conflict handling
rescale-int files download <file-id> --overwrite  # Overwrite existing
rescale-int files download <file-id> --skip       # Skip existing
rescale-int files download <file-id> --resume     # Resume interrupted
```

**Download folders:**
```bash
# Download entire folder recursively
rescale-int folders download-dir <folder-id> -o ./my-folder

# Download with merge (skip existing files)
rescale-int folders download-dir <folder-id> --merge -o ./data

# Download with automatic overwrite
rescale-int folders download-dir <folder-id> --overwrite -o ./data

# Preview what would be downloaded
rescale-int folders download-dir <folder-id> --dry-run --merge -o ./data

# Continue on errors
rescale-int folders download-dir <folder-id> --continue-on-error --merge
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

The GUI provides:
- Setup tab: Configuration and connection testing
- PUR tab: Parallel Upload and Run job management and execution
- File Browser tab: Two-pane local/remote file browser
- Activity tab: Real-time logs and monitoring

Jobs automatically update in real-time as they progress through:
- Tar creation
- Upload to Rescale
- Job creation
- Job submission

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
echo "your-api-key" > ~/.config/rescale-int/token
chmod 600 ~/.config/rescale-int/token

# Use token file
rescale-int --token-file ~/.config/rescale-int/token <command>
```

**Custom config file:**
```bash
rescale-int --config /path/to/config.csv <command>
```

### Shell Completion

Enable tab-completion for commands:

```bash
# Bash
rescale-int completion bash > /etc/bash_completion.d/rescale-int

# Zsh
rescale-int completion zsh > "${fpath[1]}/_rescale-int"

# Fish
rescale-int completion fish > ~/.config/fish/completions/rescale-int.fish
```

## Architecture

### High-Level Overview

```
┌──────────────────────────────────────────────────────────────┐
│                     rescale-int v3.6.0                        │
│                  Unified CLI + GUI Binary                     │
├──────────────────────────────────────────────────────────────┤
│                                                               │
│  ┌────────────────────┐           ┌────────────────────┐    │
│  │     CLI Mode       │           │     GUI Mode       │    │
│  │   (default)        │           │   (--gui flag)     │    │
│  ├────────────────────┤           ├────────────────────┤    │
│  │                    │           │                    │    │
│  │ • config commands  │           │ • Setup Tab        │    │
│  │ • files commands   │           │ • Jobs Tab         │    │
│  │ • folders commands │           │ • Activity Tab     │    │
│  │ • jobs commands    │           │                    │    │
│  │ • pur commands     │           │ • Event Bus        │    │
│  │ • shortcuts        │           │ • Real-time UI     │    │
│  │                    │           │                    │    │
│  └────────┬───────────┘           └─────────┬──────────┘    │
│           │                                 │                │
│           └─────────────┬───────────────────┘                │
│                         │                                     │
│                  ┌──────▼──────────┐                         │
│                  │  Shared Core     │                         │
│                  ├──────────────────┤                         │
│                  │ • Config Manager │                         │
│                  │ • API Client     │                         │
│                  │ • State Manager  │                         │
│                  │ • PUR Pipeline   │                         │
│                  │ • Progress Track │                         │
│                  └──────────────────┘                         │
│                                                               │
└──────────────────────────────────────────────────────────────┘
```

### Key Components

**CLI Interface** (`internal/cli/`)
- Cobra-based command structure
- Config, Files, Folders, Jobs, PUR commands
- Shortcuts for common operations
- Shell completion support
- Progress tracking with progress bars

**GUI Interface** (`internal/gui/`)
- Fyne-based graphical interface
- Three-tab design (Setup, Jobs, Activity)
- Real-time job monitoring
- Event-driven updates

**Event System** (`internal/events/`)
- Non-blocking pub/sub pattern
- Progress, Log, StateChange events
- Thread-safe with buffered channels
- Shared between CLI and GUI

**Core Engine** (`internal/core/`)
- Configuration management
- Job validation (Plan)
- State loading and persistence
- API client integration
- Job monitoring

**PUR Integration** (`internal/pur/`)
- Shared pipeline packages
- Config, models, API client, state management
- Upload, download, tar, encryption modules
- Multi-part upload support

## Development

### Project Structure

```
rescale-int/
├── cmd/rescale-int/      # Main entry point
│   └── main.go           # CLI/GUI router
│
├── internal/
│   ├── cli/              # CLI commands
│   │   ├── root.go       # Root command & routing
│   │   ├── config_commands.go
│   │   ├── files.go
│   │   ├── folders.go
│   │   ├── jobs.go
│   │   ├── pur.go
│   │   └── shortcuts.go
│   │
│   ├── gui/              # GUI interface (Fyne-based)
│   │   ├── gui.go        # GUI initialization
│   │   ├── setup_tab.go  # Configuration UI
│   │   ├── jobs_tab.go   # Jobs management UI
│   │   ├── activity_tab.go # Logs and activity UI
│   │   └── file_browser_tab.go # File browser UI
│   │
│   ├── cloud/            # Cloud storage backends
│   │   ├── upload/       # S3/Azure uploaders
│   │   ├── download/     # S3/Azure downloaders
│   │   └── credentials/  # Credential management
│   │
│   ├── api/              # Rescale API client
│   ├── events/           # Event bus system
│   ├── core/             # Core engine
│   ├── progress/         # Progress tracking
│   │
│   └── pur/              # PUR pipeline packages
│       ├── parser/       # SGE script parsing
│       ├── pattern/      # Pattern detection
│       ├── pipeline/     # Job pipeline
│       └── state/        # State management
│
├── CLI_GUIDE.md          # Complete CLI reference
├── ARCHITECTURE.md       # System architecture
├── CONTRIBUTING.md       # Contribution guide
└── README.md             # This file
```

### Building

```bash
# Development build
go build -o rescale-int ./cmd/rescale-int

# Production build with optimizations
go build -ldflags="-s -w" -o rescale-int ./cmd/rescale-int

# Cross-compile for Linux
GOOS=linux GOARCH=amd64 go build -o rescale-int-linux ./cmd/rescale-int

# Cross-compile for Windows
GOOS=windows GOARCH=amd64 go build -o rescale-int.exe ./cmd/rescale-int
```

### Dependencies

```bash
# Update dependencies
go get -u fyne.io/fyne/v2@latest
go mod tidy

# Verify
go mod verify
```

## Performance

### System Requirements

**Operating System:**
- **macOS**: 10.15 (Catalina) or later (Apple Silicon only)
- **Windows**: Windows 10 or later (64-bit)
- **Linux**: GLIBC 2.27+ required
  - RHEL/CentOS/Rocky/Alma 8+
  - Ubuntu 18.04+
  - Debian 10+
  - **NOT supported**: CentOS/RHEL 7 or older (end-of-life, GLIBC too old)

**Resources:**
- **RAM**: 50-100MB typical
- **CPU**: Minimal (event-driven)
- **Disk**: ~32MB binary
- **Network**: As needed for API calls

### Benchmarks

- Event bus: <1ms latency for pub/sub
- UI updates: Real-time, deadlock-free
- Table rendering: Optimized for <1000 jobs
- Job monitoring: 30-second polling interval

## Troubleshooting

### GUI Won't Start

**Linux**: Ensure X11 or Wayland is available
```bash
export DISPLAY=:0
./rescale-int
```

**macOS**: Allow unsigned app in Security settings

**Windows**: Run as administrator if needed

### Connection Issues

1. Verify API key: `echo $RESCALE_API_KEY`
2. Check proxy settings in Setup tab
3. Try `system` proxy mode

### Build Failures

```bash
# Clean and rebuild
go clean -cache
go mod tidy
go build -v ./cmd/rescale-int
```

## Debugging

The application includes comprehensive instrumentation for debugging:

- **Event tracing**: Track events through the system
- **Profiling server**: Access at http://localhost:6060
- **Goroutine monitoring**: Automatic health checks
- **Stack dumps**: Triggered on event stalls

For debugging options, use `-v` for verbose output or set `DEBUG_RETRY=true` environment variable.

## Known Limitations

- No support for Rescale CFS or Publisher capabilities
- Not drop-in compatible with Rescale CLI due to difference in commands/structure (though functionality is similar)
- **Download resume**: Full byte-offset resume is supported for encrypted file downloads. The `--resume` flag will continue interrupted downloads from the exact byte position using HTTP Range requests. Resume state is tracked via JSON sidecar files (`.download.resume`). Note: Decryption must start from the beginning due to AES-CBC mode constraints, but this happens automatically once the encrypted file download completes.

### Terminal Window Resizing

⚠️ **Do not resize the terminal window during active downloads/uploads** - the progress bars will become garbled and display incorrectly. This is a known limitation of terminal-based progress bar libraries.

**What happens:**
- Progress bars may appear multiple times
- Partial lines and visual artifacts
- Display becomes messy

**Important:** The download/upload will continue working correctly - only the visual display is affected.

**If you accidentally resize:**
1. Let the current file finish (it will complete successfully)
2. Stop the operation (Ctrl+C)
3. Resize terminal to desired size
4. Restart the command - it will resume from where it left off

**Best practice:** Set your terminal to the desired size before starting large downloads/uploads.

## License

MIT License - see [CONTRIBUTING.md](CONTRIBUTING.md) for details

## Support

For questions, issues, or feature requests:
- Contact Rescale support team

## Acknowledgments

Bartek D. -- Rescale Client & endpoints usage
Dinal P. -- early prototyping

- Built with [Fyne](https://fyne.io/) - Cross-platform GUI toolkit
- Integrates with [PUR CLI](../pur/) - Parallel uploader and runner
- Rescale API integration

## Documentation

- **[CLI_GUIDE.md](CLI_GUIDE.md)** - Complete command-line reference with examples
- **[ARCHITECTURE.md](ARCHITECTURE.md)** - System design and code organization
- **[TESTING.md](TESTING.md)** - Test strategy and procedures
- **[CONTRIBUTING.md](CONTRIBUTING.md)** - Developer onboarding guide
- **[RELEASE_NOTES.md](RELEASE_NOTES.md)** - Version history and release details

---

**Version**: 3.6.0
**Status**: Production Ready, FIPS 140-3 Mandatory
**Last Updated**: December 23, 2025
