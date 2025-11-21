# Rescale Interlink - Unified CLI and GUI for Rescale Platform

A unified tool combining comprehensive command-line interface and graphical interface for managing Rescale jobs, built with Go and Fyne.

![alt text](./logo.png)

![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-blue)
![Go Version](https://img.shields.io/badge/go-1.24+-blue)
![License]See below
![Status](https://img.shields.io/badge/status-v2.4.8-green)

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
- **Command Shortcuts**: Quick aliases (`upload`, `download`, `ls`, `run`)
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


### Recent Improvements

**v2.4.8 (November 20, 2025) - Massive Download Performance Improvement via API efficienct:**
- **99% reduction in API overhead** for job downloads (from ~188s to <1s for 289 files)
- Eliminated GetFileInfo API calls by using cached metadata from v2 ListJobFiles
- Downloads now limited by S3/Azure transfer speed, not API calls
- Ex. saves ~3 minutes for 289-file job download

**v2.4.7 (November 20, 2025) - v2 API Support:**
- Switched job file listing to v2 endpoint (12.5x faster rate limit: 20 req/sec vs 1.6 req/sec)
- Smart API routing based on endpoint type
- Added jobs-usage rate limiter for optimal performance

**v2.4.6 (November 20, 2025) - Rate Limiting Fixes:**
- Corrected rate limits to 80% of hard limits (better safety margin)
- Dual-mode upload (fast/safe) with conflict detection

**v2.3.0 (November 17, 2025) - Critical Bug Fixes:**
1. **Fixed Resume Logic**: Resume now correctly handles PKCS7 padding (1-16 bytes) when checking encrypted file sizes
2. **Decryption Progress Feedback**: Added progress message before large file decryption
3. **Progress Bar Corruption Fix**: Routed all output through mpb io.Writer to prevent ghost bars

See [RELEASE_NOTES.md](RELEASE_NOTES.md) and [FEATURE_SUMMARY.md](FEATURE_SUMMARY.md) for complete details.

## Quick Start

### Prerequisites

- Go 1.21 or later (minimum required for build)
- macOS, Linux (with X11/Wayland), or Windows
- Rescale API key

### Installation

#### Option 1: Use Pre-built Binary

Download from releases page:
- **macOS ARM64**: `rescale-int-darwin-arm64`
- **macOS Intel**: `rescale-int-darwin-amd64`
- **Linux**: `rescale-int-linux-amd64`
- **Windows**: `rescale-int-windows.exe`

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

```bash
# Clone the repository
git clone https://github.com/rescale/rescale-int.git
cd rescale-int

# Build
go build -o rescale-int ./cmd/rescale-int

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
rescale-int folders upload-dir --folder-id abc123 --dir ./simulation_data -r
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
rescale-int folders download-dir <folder-id> --outdir ./my-folder

# Download with automatic overwrite
rescale-int folders download-dir <folder-id> --outdir ./data --overwrite

# Continue on errors
rescale-int folders download-dir <folder-id> --continue-on-error
```

**List jobs:**
```bash
rescale-int ls --limit 20
```

**Monitor job:**
```bash
rescale-int jobs tail --id WfbQa --follow
```

**Download job results:**
```bash
rescale-int jobs download --id WfbQa --outdir ./results
```

**Run batch job pipeline:**
```bash
rescale-int run jobs.csv --state state.csv
```

**See [CLI_GUIDE.md](CLI_GUIDE.md) for complete command reference.**

### GUI Mode

```bash
# Start GUI
rescale-int --gui
```

The GUI provides:
- Setup tab: Configuration and connection testing
- Jobs tab: Job management and execution
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
│                    rescale-int v2.0                          │
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
│   ├── main.go           # CLI/GUI router
│   ├── setup_tab.go      # GUI: Configuration UI
│   ├── jobs_tab.go       # GUI: Jobs management UI
│   └── activity_tab.go   # GUI: Logs and activity UI
│
├── internal/
│   ├── cli/              # CLI commands
│   │   ├── root.go       # Root command & routing
│   │   ├── config_commands.go
│   │   ├── files.go
│   │   ├── folders.go
│   │   ├── jobs.go
│   │   ├── pur.go
│   │   ├── shortcuts.go
│   │   └── *_test.go     # Unit tests
│   │
│   ├── gui/              # GUI interface
│   │   └── gui.go        # GUI initialization
│   │
│   ├── events/           # Event bus system
│   │   ├── events.go
│   │   └── events_test.go
│   │
│   ├── core/             # Core engine
│   │   ├── engine.go
│   │   └── engine_test.go
│   │
│   ├── progress/         # Progress tracking
│   │   └── progress.go
│   │
│   └── pur/              # PUR pipeline packages
│       ├── api/          # Rescale API client
│       ├── config/       # Configuration management
│       ├── models/       # Data models
│       ├── state/        # State management
│       ├── upload/       # File upload (S3/Azure)
│       ├── download/     # File download
│       ├── tar/          # Archive creation
│       └── pipeline/     # Job pipeline
│
├── CLI_GUIDE.md          # Complete CLI reference
├── UNIFIED_CLI_GUI_PLAN.md  # Implementation plan
├── IMPLEMENTATION_NOTES.md  # Technical notes
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
- Not drop-in compatible with Rescale CLI () due to difference in commands/structure (though functionality is similar)

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

Same license as PUR CLI (check parent project)

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

- **[FEATURE_SUMMARY.md](FEATURE_SUMMARY.md)** - Comprehensive verified feature list (v2.3.0)
- **[CLI_GUIDE.md](CLI_GUIDE.md)** - Complete command-line reference with examples
- **[ARCHITECTURE.md](ARCHITECTURE.md)** - System design and code organization
- **[TESTING.md](TESTING.md)** - Test strategy and procedures
- **[CONTRIBUTING.md](CONTRIBUTING.md)** - Developer onboarding guide
- **[LESSONS_LEARNED.md](LESSONS_LEARNED.md)** - Development insights and best practices
- **[TODO_AND_PROJECT_STATUS.md](TODO_AND_PROJECT_STATUS.md)** - Current status and roadmap
- **[RELEASE_NOTES.md](RELEASE_NOTES.md)** - Version history and release details
- **[DOCUMENTATION_SUMMARY.md](DOCUMENTATION_SUMMARY.md)** - Documentation navigation guide

---

**Version**: 2.4.8
**Status**: Production Ready
**Last Updated**: November 20, 2025
