# Rescale Interlink CLI Guide

Complete command-line interface reference for `rescale-int` v4.7.1.

**Version:** 4.7.1
**Build Date:** February 21, 2026
**Status:** Production Ready, FIPS 140-3 Compliant (Mandatory)

For a comprehensive list of all features with source code references, see [FEATURE_SUMMARY.md](FEATURE_SUMMARY.md).

## Table of Contents

- [System Requirements](#system-requirements)
- [Installation](#installation)
- [Configuration](#configuration)
- [Global Flags](#global-flags)
- [Quick Start](#quick-start)
- [Command Reference](#command-reference)
  - [Config Commands](#config-commands)
  - [File Commands](#file-commands)
  - [Folder Commands](#folder-commands)
  - [Job Commands](#job-commands)
  - [Daemon Commands](#daemon-commands)
  - [Hardware Commands](#hardware-commands)
  - [Software Commands](#software-commands)
  - [PUR (Parallel Upload and Run) Commands](#pur-parallel-upload-and-run-commands)
  - [Shortcuts](#shortcuts)
- [Shell Completion](#shell-completion)
- [Examples](#examples)

## System Requirements

**Operating System:**
- **macOS**: 10.15 (Catalina) or later (Apple Silicon only)
- **Windows**: Windows 10 or later (64-bit)
- **Linux**: GLIBC 2.27+ required
  - RHEL/CentOS/Rocky/Alma 8+
  - Ubuntu 18.04+
  - Debian 10+
  - **NOT supported**: CentOS/RHEL 7 or older (end-of-life, GLIBC too old)

If you see an error like `GLIBC_2.27 not found`, your Linux distribution is too old and not supported.

## Installation

Download the appropriate binary for your platform from the releases page:

- **macOS (Apple Silicon)**: `rescale-int-darwin-arm64`
- **Linux**: `rescale-int-linux-amd64`
- **Windows**: `rescale-int-windows-amd64.exe`

Make the binary executable (macOS/Linux):
```bash
chmod +x rescale-int
sudo mv rescale-int /usr/local/bin/
```

## Configuration

### Interactive Setup

Run the interactive configuration wizard:

```bash
rescale-int config init
```

This will prompt you for:
- API key (required)
- API base URL (default: https://platform.rescale.com)
- Worker settings (tar, upload, job workers)
- Proxy configuration (optional)

**Note (v4.7.1):** Worker and tar settings are also configurable from the GUI PUR tab's Pipeline Settings section. Settings in config.csv are shared between CLI and GUI modes.

Configuration is saved to `~/.config/rescale/config.csv`

**Note:** If you have an existing configuration at the old location (`~/.config/rescale-int/`), it will be detected and used automatically. A migration message will suggest moving to the new location.

### Manual Configuration

Create a CSV file with key-value pairs:

```csv
key,value
api_base_url,https://platform.rescale.com
tar_workers,4
upload_workers,4
job_workers,4
proxy_mode,no-proxy
```

**Note:** API keys and proxy passwords are NOT stored in config files for security reasons (v2.4.9+).

### API Key Configuration

**Option 1: Environment Variable**
```bash
export RESCALE_API_KEY="your-api-key"
```

**Option 2: Token File (recommended for scripts)**
```bash
# Create token file with restricted permissions
echo "your-api-key" > ~/.config/rescale/token
chmod 600 ~/.config/rescale/token

# Use token file
rescale-int --token-file ~/.config/rescale/token <command>
```

**Option 3: Command-Line Flag (not recommended)**
```bash
rescale-int --api-key "your-api-key" <command>
```

### Priority Order

Configuration values are merged with this priority:
1. `--api-key` command-line flag (highest)
2. `--token-file` flag
3. `RESCALE_API_KEY` environment variable
4. Configuration file (non-credential settings only)
5. Default values (lowest)

### Proxy Configuration

For enterprise environments requiring proxy access, configure proxy settings in your config file:

```csv
key,value
proxy_mode,basic
proxy_host,proxy.company.com
proxy_port,8080
proxy_user,username
```

**Supported Proxy Modes:**
- `no-proxy` - Direct connection (default)
- `system` - Use system proxy settings (`HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` environment variables)
- `basic` - HTTP Basic authentication
- `ntlm` - NTLM authentication for corporate proxies

**Notes:**
- Proxy passwords are prompted at runtime for security (not stored in config files)
- All traffic (API calls + S3/Azure storage) routes through the configured proxy
- Use `no_proxy` config key for bypass rules (comma-separated hostnames, wildcards, CIDRs). As of v4.5.9, `no_proxy` is fully wired to the HTTP transport and configurable from the GUI Setup tab.

### Advanced Configuration Options

Additional configuration options for specialized use cases:

| Key | Description | Default |
|-----|-------------|---------|
| `tar_workers` | Number of concurrent tar operations | 4 |
| `upload_workers` | Number of concurrent upload workers | 4 |
| `job_workers` | Number of concurrent job submission workers | 4 |
| `exclude_pattern` | Patterns to exclude from tarballs (semicolon-separated, e.g., `*.log;*.tmp`) | (none) |
| `include_pattern` | Include-only patterns (mutually exclusive with exclude) | (none) |
| `flatten_tar` | Remove subdirectory structure in tarballs (`true`/`false`) | false |
| `run_subpath` | Scan prefix: subpath to navigate into before scanning for run directories (e.g., `Simcodes/Powerflow`) | (none) |
| `validation_pattern` | Pattern to validate runs (e.g., `*.avg.fnc`), opt-in | (none) |
| `tar_compression` | Compression type: `none` or `gzip` (legacy `gz` is auto-normalized to `gzip`) | none |
| `max_retries` | Maximum upload retry attempts | 1 |

**v4.7.1 Note:** In the GUI, worker and tar settings are configured via the **PUR tab's Pipeline Settings** section (visible in both the scan step and the jobs-validated step). Tar options are also available in the **SingleJob tab** when using directory input mode. The `run_subpath` and `validation_pattern` are configured on the **PUR tab** scan step and persist to `config.csv` automatically. These settings are no longer in the Setup tab's Advanced Settings.

## Global Flags

These flags are available on all commands:

### Debug and Logging

**`--verbose, -v`** - Enable verbose/debug output
```bash
rescale-int files upload myfile.txt --verbose
rescale-int pur run --config config.csv --jobs jobs.csv --state state.csv -v
```

**`--debug`** - Enable debug output (same as `--verbose`)
```bash
rescale-int files upload myfile.txt --debug
```

When debug mode is enabled:
- Shows detailed operation logs
- Displays debug-level messages for troubleshooting
- Useful for diagnosing upload/download issues

### Performance Tuning

**`--max-threads N`** - Set maximum concurrent threads (0 = auto-detect, range: 1-32)
```bash
rescale-int files upload large_file.dat --max-threads 10
```

**`--no-auto-scale`** - Disable automatic thread scaling
```bash
rescale-int files upload large_file.dat --no-auto-scale --max-threads 4
```

### Configuration Overrides

**`--config, -c PATH`** - Use specific configuration file
```bash
rescale-int pur run --config myconfig.csv --jobs jobs.csv --state state.csv
```

**`--api-key KEY`** - Override API key from all other sources
```bash
rescale-int files list --api-key your-api-key-here
```

**`--token-file PATH`** - Read API key from file (v2.4.9+)
```bash
rescale-int files list --token-file ~/.config/rescale/token
```

**`--api-url URL`** - Override API base URL
```bash
rescale-int files list --api-url https://platform.rescale.com
```

### GUI Mode

For GUI mode, set the RESCALE_DEBUG environment variable:
```bash
export RESCALE_DEBUG=1
./rescale-int --gui
```

This enables debug output in the GUI application console.

## Quick Start

### 1. Configure API credentials
```bash
rescale-int config init
```

### 2. Test connection
```bash
rescale-int config test
```

### 3. Upload a file
```bash
rescale-int upload input.txt
```

### 4. List your jobs
```bash
rescale-int ls
```

### 5. Run a job pipeline
```bash
rescale-int pur run --jobs-csv jobs.csv --state state.csv
```

## Command Reference

### Config Commands

#### config init
Initialize configuration interactively

```bash
rescale-int config init [--force]
```

**Flags:**
- `-f, --force` - Overwrite existing configuration

**Example:**
```bash
rescale-int config init
rescale-int config init -f  # Force overwrite
```

#### config show
Display current configuration

```bash
rescale-int config show
```

Shows merged configuration from file, environment, and flags.

#### config test
Test API connection

```bash
rescale-int config test
```

Verifies:
- Configuration is valid
- API credentials work
- Network connectivity
- Returns user information

#### config path
Show configuration file path

```bash
rescale-int config path
```

Displays the path to the config file and whether it exists.

### File Commands

#### files upload
Upload files to Rescale

```bash
rescale-int files upload <file> [file...] [flags]
```

**Features:**
- Automatic encryption (AES-256-CBC) before upload
- Multi-part upload for files >100MB (32MB chunks)
- Automatic resume on interruption (state saved to `.rescale-upload-state`)
- Progress bars with transfer speed and ETA
- Support for both S3 and Azure storage backends
- Duplicate detection with configurable handling modes

**Flags:**
- `-d, --folder-id string` - Target folder ID
- `-m, --max-concurrent int` - Maximum concurrent uploads (default: 5)
- `--check-duplicates` - Check for existing files before uploading (prompts for each duplicate)
- `--no-check-duplicates` - Skip duplicate checking (fast mode, may create duplicates)
- `--skip-duplicates` - Check and automatically skip files that already exist
- `--allow-duplicates` - Check but upload anyway (explicitly allows duplicates)
- `--dry-run` - Preview what would be uploaded without actually uploading

**Duplicate Detection Modes:**
- **Interactive mode (no flags)**: Prompts for duplicate handling mode at start
- **Non-interactive mode**: Defaults to no-check with warning; use explicit flags for other behavior

**Examples:**
```bash
# Upload single file (automatically encrypted)
rescale-int files upload input.txt

# Upload multiple files
rescale-int files upload data1.csv data2.csv results.tar.gz

# Upload to specific folder
rescale-int files upload model.tar.gz -d abc123

# Upload with duplicate checking (skip existing files)
rescale-int files upload *.dat --skip-duplicates

# Upload with duplicate checking (prompt for each conflict)
rescale-int files upload *.dat --check-duplicates

# Upload without duplicate checking (fast mode)
rescale-int files upload *.dat --no-check-duplicates

# Preview what would be uploaded
rescale-int files upload *.dat --dry-run --check-duplicates

# Upload large file (>100MB) - uses multi-part with resume capability
rescale-int files upload large_dataset.tar.gz
```

**Note:** Files are encrypted locally using AES-256-CBC before upload. Decryption happens automatically on download. See [FEATURE_SUMMARY.md](FEATURE_SUMMARY.md#security--encryption) for encryption details.

#### files download
Download files from Rescale

```bash
rescale-int files download <file-id> [file-id...] [flags]
```

**Features (v2.3.0):**
- Automatic decryption after download
- Chunked download for large files (>100MB, 32MB chunks)
- Progress bars during download and decryption
- Resume capability for interrupted downloads (state saved to `.rescale-download-state`)
- **v2.3.0 Fix:** Correct PKCS7 padding handling (1-16 bytes) in resume logic
- **v2.3.0 Enhancement:** Progress message before large file decryption
- Streaming decryption using 16KB chunks (prevents memory exhaustion)
- Concurrent chunk downloads for large files

**Flags:**
- `-o, --outdir string` - Output directory (default: current directory)
- `-m, --max-concurrent int` - Maximum concurrent downloads (default: 5)
- `-w, --overwrite` - Overwrite existing files without prompting
- `-S, --skip` - Skip existing files without prompting
- `-r, --resume` - Resume interrupted downloads without prompting

**Examples:**
```bash
# Download single file (automatically decrypted)
rescale-int files download abc123 -o ./results

# Download multiple files
rescale-int files download abc123 def456 ghi789 -o ./downloads

# Download large file - shows "Decrypting..." message for large files
rescale-int files download large-file-id -o output.dat

# Resume interrupted download (automatically detects .encrypted file)
# Just rerun the same command - it will resume from where it left off
rescale-int files download abc123 -o result.tar.gz
```

**v2.3.0 Improvements:**
- Resume now correctly validates encrypted file size accounting for PKCS7 padding
- Decryption progress message prevents confusion during long operations (40+ minutes for 60GB files)
- Streaming decryption maintains constant ~16KB memory footprint regardless of file size

**Note on Resume (v2.5.0):** The `--resume` flag supports full byte-offset resume for encrypted file downloads. Interrupted downloads continue from the exact byte position using HTTP Range requests. Resume state is tracked via `.download.resume` JSON sidecar files. Decryption starts from the beginning (AES-CBC mode constraint) but happens automatically once the encrypted file is complete.

#### files list
List files

```bash
rescale-int files list [--limit N]
```

**Flags:**
- `-n, --limit int` - Maximum number of files to list (default 20)

**Example:**
```bash
rescale-int files list --limit 50
```

#### files delete
Delete files

```bash
rescale-int files delete <file-id> [file-id...]
```

**Example:**
```bash
rescale-int files delete abc123 def456
```

### Folder Commands

#### folders create
Create a new folder

```bash
rescale-int folders create <name> [--parent-id ID]
```

**Flags:**
- `--parent-id string` - Parent folder ID (optional)

**Examples:**
```bash
# Create root-level folder
rescale-int folders create "My Simulations"

# Create subfolder
rescale-int folders create "CFD Cases" --parent-id abc123
```

#### folders list
List folder contents

```bash
rescale-int folders list [--folder-id ID]
```

**Flags:**
- `--folder-id string` - Folder ID (omit for root folders)

**Examples:**
```bash
# List root folders
rescale-int folders list

# List folder contents
rescale-int folders list --folder-id abc123
```

#### folders upload-dir
Upload entire directory to a folder

```bash
rescale-int folders upload-dir <directory> [flags]
```

**Flags:**
- `--parent-id string` - Parent folder ID (default: My Library root)
- `--max-concurrent int` - Maximum concurrent file uploads (default: 5)
- `--include-hidden` - Include hidden files (starting with .)
- `--sequential` - Use sequential mode (create all folders, then upload all files)
- `--continue-on-error` - Continue uploading on errors without prompting
- `-S, --skip-folder-conflicts` - Skip folders that already exist on Rescale
- `-m, --merge-folder-conflicts` - Merge into existing folders (skip existing files)
- `--check-conflicts` - Check for existing files before upload (slower but shows conflicts upfront)

**Conflict Handling Modes:**
- **Skip** (`-S`): If root folder already exists, abort the upload
- **Merge** (`-m`): Use existing folders and skip files that already exist
- **Interactive mode (no flags)**: Prompts for conflict handling mode when folder exists

**Performance Note:** Concurrent uploads (max 5 simultaneous) with connection reuse for maximum throughput.

**New in v2.0.1**:
- Folder caching reduces API calls by 99.8% for repeated operations
- Disk space pre-checking prevents mid-operation failures
- Multi-progress bars for concurrent uploads

**Examples:**
```bash
# Upload directory to My Library root
rescale-int folders upload-dir ./simulation_data

# Upload to specific parent folder
rescale-int folders upload-dir ./project --parent-id abc123

# Upload and merge into existing folder (skip existing files)
rescale-int folders upload-dir ./project --merge-folder-conflicts

# Upload and abort if folder already exists
rescale-int folders upload-dir ./project --skip-folder-conflicts

# Upload with high concurrency
rescale-int folders upload-dir ./project --max-concurrent 10

# Include hidden files
rescale-int folders upload-dir ./project --include-hidden

# Example: Folder caching in action
# First run: 1 API call to resolve folder
# Subsequent runs: Instant lookup from cache (99.8% faster)
```

#### folders download-dir
Download entire folder recursively from Rescale

```bash
rescale-int folders download-dir <folder-id> [flags]
```

**Features:**
- Recursive folder structure download
- Concurrent file downloads for improved performance
- Conflict handling for existing local files/folders
- Dry-run mode for previewing downloads
- Checksum verification after download

**Flags:**
- `-o, --outdir string` - Output directory for downloaded files (default: current directory)
- `--max-concurrent int` - Maximum concurrent downloads (default: 5)
- `-S, --skip` - Skip existing files/folders without prompting
- `-w, --overwrite` - Overwrite existing files without prompting
- `-m, --merge` - Merge into existing folders, skip existing files
- `--dry-run` - Preview what would be downloaded without actually downloading
- `--continue-on-error` - Continue downloading other files if one fails
- `--skip-checksum` - Skip checksum verification (not recommended)

**Conflict Handling Modes:**
- **Skip** (`-S`): Skip the entire folder if it already exists locally
- **Overwrite** (`-w`): Download into existing folders, overwrite existing files
- **Merge** (`-m`): Download into existing folders, skip existing files
- **Interactive mode (no flags)**: Prompts for conflict handling mode when folder exists
- **Non-interactive mode**: Requires explicit flag (`--skip`, `--overwrite`, or `--merge`)

**Examples:**
```bash
# Download folder to current directory
rescale-int folders download-dir abc123

# Download to specific directory
rescale-int folders download-dir abc123 -o ./downloads

# Download with merge (skip existing files)
rescale-int folders download-dir abc123 --merge -o ./data

# Download with overwrite (replace existing files)
rescale-int folders download-dir abc123 --overwrite -o ./data

# Preview what would be downloaded
rescale-int folders download-dir abc123 --dry-run --merge -o ./data

# Download with skip (abort if folder exists)
rescale-int folders download-dir abc123 --skip -o ./data

# Download with high concurrency
rescale-int folders download-dir abc123 --max-concurrent 10 --merge -o ./data

# Continue downloading even if some files fail
rescale-int folders download-dir abc123 --continue-on-error --merge
```

#### folders delete
Delete a folder

```bash
rescale-int folders delete <folder-id>
```

**Example:**
```bash
rescale-int folders delete abc123
```

### Job Commands

#### jobs list
List jobs

```bash
rescale-int jobs list [flags]
```

**Flags:**
- `-n, --limit int` - Maximum number of jobs to list (default 10)
- `-s, --status string` - Filter by job status

**Examples:**
```bash
# List recent jobs
rescale-int jobs list

# List more jobs
rescale-int jobs list --limit 50

# Filter by status
rescale-int jobs list --status Completed
rescale-int jobs list --status Running
```

#### jobs get
Get job details

```bash
rescale-int jobs get -j <job-id>
```

**Flags:**
- `-j, --job-id string` - Job ID (required)

**Example:**
```bash
rescale-int jobs get -j WfbQa
```

#### jobs stop
Stop a running job

```bash
rescale-int jobs stop -j <job-id>
```

**Flags:**
- `-j, --job-id string` - Job ID (required)
- `-y, --confirm` - Skip confirmation prompt

**Example:**
```bash
rescale-int jobs stop -j WfbQa
rescale-int jobs stop -j WfbQa -y  # Skip confirmation
```

#### jobs tail
Stream job log output

```bash
rescale-int jobs tail -j <job-id> [flags]
```

**Flags:**
- `-j, --job-id string` - Job ID (required)
- `-i, --interval int` - Polling interval in seconds (default: 10)

**Examples:**
```bash
# View job logs with default 10-second polling
rescale-int jobs tail -j WfbQa

# Monitor job with 5-second polling interval
rescale-int jobs tail -j WfbQa -i 5

# Using long flags
rescale-int jobs tail --job-id WfbQa --interval 30
```

#### jobs listfiles
List files in a job

```bash
rescale-int jobs listfiles -j <job-id>
```

**Flags:**
- `-j, --job-id string` - Job ID (required)

**Example:**
```bash
rescale-int jobs listfiles -j WfbQa
```

#### jobs download
Download job output files

```bash
rescale-int jobs download -j <job-id> [flags]
```

**Modes:**
1. **Batch download** (no `--file-id`): Download all job output files
2. **Single file** (with `--file-id`): Download specific file

**Flags:**
- `-j, --job-id string` - Job ID (required) (alias: `--id`)
- `--file-id string` - Specific file ID to download (optional)
- `-d, --outdir string` - Output directory for batch download
- `-o, --output string` - Output file path (for single file)
- `-m, --max-concurrent int` - Maximum concurrent downloads (default: 5)
- `-w, --overwrite` - Overwrite existing files
- `-S, --skip` - Skip existing files
- `-r, --resume` - Resume interrupted downloads
- `-s, --search string` - Filter files by search term
- `-x, --exclude string` - Exclude files matching pattern

**Examples:**
```bash
# Download all job files to current directory
rescale-int jobs download -j WfbQa

# Download all job files to specific directory
rescale-int jobs download -j WfbQa -d ./results

# Download specific file
rescale-int jobs download -j WfbQa --file-id xyz789 -o result.tar.gz
```

#### jobs delete
Delete jobs

```bash
rescale-int jobs delete -j <job-id> [-j <job-id>...] [-y]
```

**Flags:**
- `-j, --job-id string` - Job ID to delete (can be specified multiple times) (alias: `--id`)
- `-y, --confirm` - Skip confirmation prompt

**Examples:**
```bash
# Delete single job (with confirmation)
rescale-int jobs delete --job-id WfbQa

# Delete multiple jobs (short form)
rescale-int jobs delete -j WfbQa -j XyzBb -j AbcCc

# Delete without confirmation
rescale-int jobs delete --job-id WfbQa --confirm
```

#### jobs submit
Create and/or submit jobs from JSON, SGE script, or existing job ID

```bash
rescale-int jobs submit --job-file <file> [--create]
rescale-int jobs submit --script <file> [--submit]
rescale-int jobs submit --job-id <id>
```

**Flags:**
- `-f, --job-file string` - Path to job specification JSON file
- `-s, --script string` - Path to SGE-style script with `#RESCALE_*` metadata
- `-j, --job-id string` - Existing job ID to submit (use with `--submit` only)
- `--files strings` - Input files to upload (comma-separated, supports glob patterns)
- `--create` - Create job only (don't submit)
- `--submit` - Create and submit job (default behavior)
- `-E, --end-to-end` - Full workflow: upload, create, submit, monitor, download
- `--download` - Auto-download results after job completes (requires `--end-to-end`)
- `--no-tar` - Skip tarball creation for single file uploads
- `-m, --max-concurrent int` - Maximum concurrent file uploads
- `--automation strings` - Automation ID(s) to attach (comma-separated or repeated)

**Examples:**
```bash
# Submit job from JSON spec
rescale-int jobs submit --job-file job_spec.json

# Create job without submitting (create-only mode)
rescale-int jobs submit --job-file job_spec.json --create

# Submit job with automations attached
rescale-int jobs submit --job-file job_spec.json --automation aB1cD2 --automation eF3gH4
```

### Daemon Commands

Background service for automatically downloading completed jobs. Added in v3.4.0.

**Configuration (v4.2.0):** The daemon reads settings from `daemon.conf` by default. CLI flags override config file values. See [daemon config](#daemon-config) commands below.

#### daemon run

Start the daemon to poll for completed jobs and download their output files.

```bash
rescale-int daemon run [flags]
```

**Config File:** `~/.config/rescale/daemon.conf` (Unix) or `%APPDATA%\Rescale\Interlink\daemon.conf` (Windows)

The daemon automatically loads settings from the config file. CLI flags override config file values, allowing you to test different settings without modifying the config file.

**Flags:**
- `-d, --download-dir string` - Directory to download job outputs to (default ".")
- `--poll-interval string` - How often to check for completed jobs (default "5m")
- `--name-prefix string` - Only download jobs with names starting with this prefix
- `--name-contains string` - Only download jobs with names containing this string
- `--exclude stringArray` - Exclude jobs with names starting with these prefixes
- `--max-concurrent int` - Maximum concurrent file downloads per job (default 5)
- `--state-file string` - Path to daemon state file
- `--use-job-id` - Use job ID instead of job name for output directory names
- `--once` - Run once and exit (useful for cron jobs)
- `--log-file string` - Path to log file (empty = stdout)
- `--background` - Run in background mode (Unix only)
- `--ipc` - Enable IPC server for GUI/CLI control

**Examples:**
```bash
# Start daemon using daemon.conf settings
rescale-int daemon run

# Start daemon with IPC for GUI control
rescale-int daemon run --background --ipc

# Override download-dir from config file
rescale-int daemon run --download-dir ./override

# With job name filtering (overrides config)
rescale-int daemon run --name-prefix "MyProject"
rescale-int daemon run --name-contains "simulation"
rescale-int daemon run --exclude "Debug" --exclude "Test"

# Configure poll interval (overrides config)
rescale-int daemon run --poll-interval 2m

# Run once and exit (for cron jobs)
rescale-int daemon run --once
```

#### daemon config

Manage daemon configuration file (`daemon.conf`). Added in v4.2.0.

##### daemon config show

Display current daemon configuration.

```bash
rescale-int daemon config show
```

Shows all settings from the config file with current values.

**Example output:**
```
Daemon Configuration (~/.config/rescale/daemon.conf)
====================================================

[daemon]
enabled = true
download_folder = ~/Downloads/rescale-jobs
poll_interval_minutes = 5
use_job_name_dir = true
max_concurrent = 5
lookback_days = 7

[filters]
name_prefix =
name_contains =
exclude = test,debug

[eligibility]
correctness_tag = isCorrect:true
auto_download_value = Enable
downloaded_tag = autoDownloaded:true

[notifications]
enabled = true
show_download_complete = true
show_download_failed = true
```

##### daemon config path

Show the path to the daemon configuration file.

```bash
rescale-int daemon config path
```

**Example:**
```bash
rescale-int daemon config path
# Output: ~/.config/rescale/daemon.conf
```

##### daemon config edit

Open the daemon configuration file in your default editor.

```bash
rescale-int daemon config edit
```

Uses `$EDITOR` environment variable (falls back to `vi` on Unix, `notepad` on Windows).

##### daemon config set

Set a configuration value.

```bash
rescale-int daemon config set <key> <value>
```

**Available keys:**
- `daemon.enabled` - Enable/disable daemon (true/false)
- `daemon.download_folder` - Download directory path
- `daemon.poll_interval_minutes` - Poll interval in minutes (1-60)
- `daemon.use_job_name_dir` - Use job name for subdirectories (true/false)
- `daemon.max_concurrent` - Max concurrent downloads (1-20)
- `daemon.lookback_days` - How many days back to check for jobs (1-30)
- `filters.name_prefix` - Job name prefix filter
- `filters.name_contains` - Job name contains filter
- `filters.exclude` - Comma-separated exclude patterns
- `eligibility.correctness_tag` - Tag for job correctness
- `eligibility.auto_download_value` - Required value for "Auto Download" field (default: Enable)
- `eligibility.downloaded_tag` - Tag added after download (default: autoDownloaded:true)
- `notifications.enabled` - Enable notifications (true/false)

**Examples:**
```bash
# Set download folder
rescale-int daemon config set daemon.download_folder ~/Downloads/rescale-jobs

# Set poll interval to 10 minutes
rescale-int daemon config set daemon.poll_interval_minutes 10

# Set exclude patterns
rescale-int daemon config set filters.exclude "test,debug,scratch"

# Enable the daemon
rescale-int daemon config set daemon.enabled true
```

##### daemon config init

Interactive daemon configuration setup.

```bash
rescale-int daemon config init [--force]
```

**Flags:**
- `-f, --force` - Overwrite existing configuration

Prompts for common settings and creates a `daemon.conf` file.

**Example:**
```bash
rescale-int daemon config init
# Interactive prompts for download folder, poll interval, etc.
```

##### daemon config validate

**Added in v4.2.1.** Validate that your Rescale workspace is configured for auto-download.

```bash
rescale-int daemon config validate
```

This command checks if the required "Auto Download" custom field exists in your workspace.

**Example output:**
```
Validating auto-download workspace configuration...

Custom Fields Enabled: true
'Auto Download' Field: true
  - Type: select
  - Section: Context
  - Values: [Enable Disable]
'Auto Download Path' Field: false (optional)

✓ Workspace is properly configured for auto-download.
```

**Setting up your workspace for auto-download:**

1. Go to Rescale Platform → Workspace Settings → Custom Fields
2. Create a new Job custom field:
   - **Name**: `Auto Download` (exact spelling required)
   - **Type**: Select (dropdown) or Text
   - **Values** (if Select): `Enable`, `Disable` (or your preferred values)
3. Configure the expected value in `daemon.conf`:
   ```ini
   [eligibility]
   auto_download_value = Enable
   ```

#### daemon status

Show daemon state and statistics.

```bash
rescale-int daemon status [flags]
```

**Flags:**
- `--state-file string` - Path to daemon state file

**Example:**
```bash
rescale-int daemon status
```

Output includes:
- Last poll time
- Number of downloaded jobs
- Number of failed downloads
- Recent download history

#### daemon list

List downloaded or failed jobs.

```bash
rescale-int daemon list [flags]
```

**Flags:**
- `--state-file string` - Path to daemon state file
- `--failed` - Show failed downloads instead of successful ones
- `--limit int` - Limit number of entries shown (0 = all)

**Examples:**
```bash
# List downloaded jobs
rescale-int daemon list

# List failed downloads
rescale-int daemon list --failed

# Limit to 10 most recent
rescale-int daemon list --limit 10
```

#### daemon retry

Mark failed jobs for retry on the next poll cycle.

```bash
rescale-int daemon retry [flags]
```

**Flags:**
- `--state-file string` - Path to daemon state file
- `--all` - Retry all failed jobs
- `-j, --job-id stringArray` - Job ID to retry (can be specified multiple times)

**Examples:**
```bash
# Retry all failed jobs
rescale-int daemon retry --all

# Retry specific job
rescale-int daemon retry --job-id XxYyZz
```

---

### Hardware Commands

Commands for discovering available hardware types (core types) on the Rescale platform.

#### hardware list
List available hardware types (core types). By default, only active hardware types are shown.

```bash
rescale-int hardware list [flags]
```

**Flags:**
- `-s, --search string` - Search for hardware by code or name
- `-J, --json` - Output as JSON
- `-a, --all` - Include inactive/deprecated hardware types

**Examples:**
```bash
# List active hardware types (default)
rescale-int hardware list

# Include inactive/deprecated hardware types
rescale-int hardware list -a

# Search for specific hardware
rescale-int hardware list -s emerald

# Get JSON output
rescale-int hardware list -J
```

**Migration Note (v2.5.0):** The `--active` flag was removed. Active hardware is now shown by default; use `-a/--all` to include inactive types.

### Software Commands

Commands for discovering available software applications (analyses) on the Rescale platform.

#### software list
List available software applications (analyses)

```bash
rescale-int software list [flags]
```

**Flags:**
- `-s, --search string` - Search for software by code or name
- `-J, --json` - Output as JSON
- `-V, --versions` - Show available versions for each software

**Examples:**
```bash
# List all software
rescale-int software list

# Search for specific software
rescale-int software list --search openfoam

# Get JSON output with versions
rescale-int software list --json --versions
```

### PUR (Parallel Upload and Run) Commands

PUR (Parallel Upload and Run) provides batch job submission with pipeline management.

#### pur make-dirs-csv
Generate jobs CSV from directory pattern

```bash
rescale-int pur make-dirs-csv --template TEMPLATE --output OUTPUT --pattern PATTERN [--overwrite]
```

**Flags:**
- `-t, --template string` - Template CSV file (required)
- `-o, --output string` - Output jobs CSV file (required unless `--command-pattern-test`)
- `-p, --pattern string` - Directory pattern, e.g., 'Run_*' (required)
- `--overwrite` - Overwrite existing output file
- `--iterate-command-patterns` - Vary command across runs by iterating numeric patterns (v4.6.4)
- `--command-pattern-test` - Preview pattern detection without generating CSV (v4.6.4)
- `--cwd string` - Working directory (default: current directory) (v4.6.4)
- `--run-subpath string` - Subdirectory path to navigate before finding runs (v4.6.4)
- `--validation-pattern string` - File pattern to validate directories (v4.6.4)
- `--start-index int` - Starting index for job numbering (default: 1) (v4.6.4)
- `--part-dirs strings` - Project directories for multi-part mode (v4.6.5)

**Example:**
```bash
rescale-int pur make-dirs-csv \
  --template template.csv \
  --output jobs.csv \
  --pattern "Run_*"

# Preview how command patterns would vary:
rescale-int pur make-dirs-csv \
  --template template.csv \
  --pattern "Run_*" \
  --command-pattern-test

# Generate with pattern iteration:
rescale-int pur make-dirs-csv \
  --template template.csv \
  --output jobs.csv \
  --pattern "Run_*" \
  --iterate-command-patterns

# Multi-part mode: scan multiple project directories (v4.6.5)
rescale-int pur make-dirs-csv \
  --template template.csv \
  --output jobs.csv \
  --pattern "Run_*" \
  --part-dirs /data/DOE_1 /data/DOE_2 /data/DOE_3 \
  --validation-pattern "*.avg.fnc"
```

#### pur plan
Validate job pipeline without executing

```bash
rescale-int pur plan --jobs-csv FILE [--validate-coretype]
```

**Flags:**
- `-j, --jobs-csv string` - Jobs CSV file (required)
- `--validate-coretype` - Validate core type with Rescale API

**Example:**
```bash
rescale-int pur plan --jobs-csv jobs.csv --validate-coretype
```

#### pur run
Execute complete job pipeline

```bash
rescale-int pur run --jobs-csv FILE [--state FILE] [--multipart]
```

**Pipeline stages:**
1. Create tar archives from run directories
2. Upload files to Rescale
3. Submit jobs to Rescale
4. Save state for resume capability

**Flags:**
- `-j, --jobs-csv string` - Jobs CSV file (required)
- `-s, --state string` - State file for resume capability
- `--multipart` - Enable multi-part mode
- `--extra-input-files string` - Comma-separated local paths and/or `id:<fileId>` to share across all jobs (v4.6.4)
- `--decompress-extras` - Decompress extra input files on cluster (default: false) (v4.6.4)
- `--include-pattern strings` - Only tar files matching glob (repeatable) (v4.6.4)
- `--exclude-pattern strings` - Exclude files matching glob from tar (repeatable) (v4.6.4)
- `--flatten-tar` - Remove subdirectory structure in tarball (v4.6.4)
- `--tar-compression string` - Tar compression: "none" or "gzip" (v4.6.4, v4.7.1: normalized)
- `--tar-workers int` - Parallel tar workers (default from config) (v4.6.4)
- `--upload-workers int` - Parallel upload workers (default from config) (v4.6.4)
- `--job-workers int` - Parallel job creation workers (default from config) (v4.6.4)
- `--rm-tar-on-success` - Delete local tar after successful upload (v4.6.4)
- `--dry-run` - Validate and show plan without executing (v4.6.5)

**Example:**
```bash
rescale-int pur run --jobs-csv jobs.csv --state state.csv

# With shared extra input files:
rescale-int pur run --jobs-csv jobs.csv --state state.csv \
  --extra-input-files "/path/to/shared_script.py,id:AbCdEf123"

# With tar filtering:
rescale-int pur run --jobs-csv jobs.csv --state state.csv \
  --exclude-pattern "*.log" --exclude-pattern "*.tmp"

# Dry-run: validate and preview without executing (v4.6.5)
rescale-int pur run --jobs-csv jobs.csv --dry-run
```

#### pur resume
Resume interrupted pipeline

```bash
rescale-int pur resume --jobs-csv FILE --state FILE [--multipart]
```

**Flags:**
- `-j, --jobs-csv string` - Jobs CSV file (required)
- `-s, --state string` - State file (required)
- `--multipart` - Enable multi-part mode
- `--extra-input-files string` - Comma-separated local paths and/or `id:<fileId>` (v4.6.4)
- `--decompress-extras` - Decompress extra input files on cluster (v4.6.4)
- `--include-pattern strings` - Only tar files matching glob (repeatable) (v4.6.4)
- `--exclude-pattern strings` - Exclude files matching glob from tar (repeatable) (v4.6.4)
- `--flatten-tar` - Remove subdirectory structure in tarball (v4.6.4)
- `--tar-compression string` - Tar compression: "none" or "gzip" (v4.6.4, v4.7.1: normalized)
- `--tar-workers int` - Parallel tar workers (v4.6.4)
- `--upload-workers int` - Parallel upload workers (v4.6.4)
- `--job-workers int` - Parallel job creation workers (v4.6.4)
- `--rm-tar-on-success` - Delete local tar after successful upload (v4.6.4)
- `--dry-run` - Show what would be resumed without executing (v4.6.5)

**Example:**
```bash
rescale-int pur resume --jobs-csv jobs.csv --state state.csv

# Dry-run: analyze state and show remaining work (v4.6.5)
rescale-int pur resume --jobs-csv jobs.csv --state state.csv --dry-run
```

#### pur submit-existing
Submit jobs using existing uploaded file IDs

```bash
rescale-int pur submit-existing --jobs-csv FILE [--state FILE]
rescale-int pur submit-existing --ids JOB1,JOB2,JOB3
```

Skips tar and upload phases. Use when files are already uploaded to Rescale.

**Flags:**
- `--jobs-csv string` - Jobs CSV file with extrainputfileids column
- `--state string` - State file
- `--ids string` - Comma-separated job IDs to submit directly (v4.6.5, mutually exclusive with --jobs-csv)

**Example:**
```bash
# Submit from CSV (existing behavior):
rescale-int pur submit-existing --jobs-csv jobs_with_fileids.csv

# Submit specific job IDs directly (v4.6.5):
rescale-int pur submit-existing --ids "abc123,def456,ghi789"
```

**Example:**
```bash
rescale-int pur submit-existing --jobs-csv jobs_with_fileids.csv --state state.csv
```

### Shortcuts

Convenient aliases for commonly-used commands.

#### upload
Shortcut for `files upload`

```bash
rescale-int upload <file> [file...] [flags]
```

**Example:**
```bash
rescale-int upload input.txt data.csv
```

#### download
Shortcut for `files download`

```bash
rescale-int download <file-id> [file-id...] [flags]
```

**Example:**
```bash
rescale-int download abc123 --output result.tar.gz
```

#### ls
Shortcut for `jobs list`

```bash
rescale-int ls [--limit N] [--status STATUS]
```

**Example:**
```bash
rescale-int ls --limit 20
```

## Shell Completion

Enable shell completion for tab-completion of commands and flags.

### Bash

**Linux:**
```bash
rescale-int completion bash > /etc/bash_completion.d/rescale-int
```

**macOS:**
```bash
rescale-int completion bash > $(brew --prefix)/etc/bash_completion.d/rescale-int
```

**Current session:**
```bash
source <(rescale-int completion bash)
```

### Zsh

```bash
rescale-int completion zsh > "${fpath[1]}/_rescale-int"
```

**Current session:**
```bash
source <(rescale-int completion zsh)
```

### Fish

```bash
rescale-int completion fish > ~/.config/fish/completions/rescale-int.fish
```

### PowerShell

```powershell
rescale-int completion powershell > rescale-int.ps1
```

## Examples

### Basic File Operations

```bash
# Upload files
rescale-int upload model.tar.gz input.dat

# List files
rescale-int files list --limit 50

# Download file
rescale-int download abc123 -o model_output.tar.gz

# Delete old files
rescale-int files delete old_file_id1 old_file_id2
```

### Folder Management

```bash
# Create project folder
rescale-int folders create "CFD Project Q1 2025"

# Upload entire simulation directory (5-10x faster than individual uploads)
rescale-int folders upload-dir ./simulation_cases --parent-id abc123

# List folder contents
rescale-int folders list --folder-id abc123
```

### Job Management

```bash
# List running jobs
rescale-int ls --status Running

# Get job details
rescale-int jobs get -j WfbQa

# Stream job log in real-time
rescale-int jobs tail -j WfbQa

# Download all job outputs
rescale-int jobs download -j WfbQa -d ./results

# Stop job
rescale-int jobs stop -j WfbQa

# Delete old jobs
rescale-int jobs delete -j job1 -j job2 --confirm
```

### Batch Job Pipeline (PUR)

```bash
# 1. Generate jobs CSV from Run_* directories
rescale-int pur make-dirs-csv \
  --template template.csv \
  --output jobs.csv \
  --pattern "Run_*"

# 2. Validate pipeline
rescale-int pur plan \
  --jobs-csv jobs.csv \
  --validate-coretype

# 3. Execute pipeline
rescale-int pur run --jobs-csv jobs.csv --state state.csv

# 4. If interrupted, resume from where it left off
rescale-int pur resume \
  --jobs-csv jobs.csv \
  --state state.csv
```

### Configuration Management

```bash
# Interactive setup
rescale-int config init

# Test connection
rescale-int config test

# View current configuration
rescale-int config show

# Find config file location
rescale-int config path
```

### Using Environment Variables

```bash
# Set API key via environment
export RESCALE_API_KEY="your-api-key-here"
export RESCALE_API_URL="https://platform.rescale.com"

# Now commands work without config file
rescale-int ls
rescale-int upload input.txt
```

### Scripting Examples

**Upload all CSV files in directory:**
```bash
for file in *.csv; do
  rescale-int upload "$file"
done
```

**Download all completed jobs:**
```bash
rescale-int jobs list --status Completed --limit 100 | \
  grep "ID:" | \
  awk '{print $2}' | \
  while read job_id; do
    rescale-int jobs download -j "$job_id" -d "./job_$job_id"
  done
```

**Monitor job until completion:**
```bash
job_id="WfbQa"
while true; do
  status=$(rescale-int jobs get -j "$job_id" | grep "Status:" | awk '{print $2}')
  echo "Job $job_id status: $status"
  if [[ "$status" == "Completed" || "$status" == "Failed" ]]; then
    break
  fi
  sleep 30
done
```

## Performance Tips

### Multi-Threaded Transfers (v2.2.0)

**Automatic (recommended for most users)**:
```bash
# Auto-detects system resources and optimizes transfer speed
rescale-int files upload largefile.tar.gz
rescale-int files download <file-id>
```

**Manual control for specific scenarios**:
```bash
# High-bandwidth connection (>500 Mbps): increase threads
rescale-int files upload bigfile.tar.gz --max-threads 16

# Low-memory system (< 4GB RAM): reduce threads
rescale-int files download <id> --max-threads 4

# Many small files: spread threads across files
rescale-int files upload *.log --max-concurrent 10 --max-threads 10

# Few large files: concentrate threads per file
rescale-int files upload huge1.tar.gz huge2.tar.gz --max-threads 16

# Conservative allocation (disable auto-scaling)
rescale-int files upload file.tar.gz --no-auto-scale
```

**Performance expectations**:
- Small files (<100MB): No change (uses sequential transfer)
- Medium files (100MB-1GB): 1.5-2x speedup
- Large files (1-10GB): 2-4x speedup
- Very large files (>10GB): 3-5x speedup

**Global flags for thread control**:
- `--max-threads N`: Total thread pool size (0=auto, 1-32)
- `--no-auto-scale`: Disable adaptive thread allocation
- Combines with `--max-concurrent` for file-level concurrency

### General Tips

1. **Use folders upload-dir for bulk uploads**: Connection reuse provides 5-10x speedup
2. **Batch operations**: Upload/download multiple files in one command
3. **PUR pipeline**: Efficiently manage dozens or hundreds of jobs
4. **State files**: Resume interrupted operations without starting over
5. **Thread tuning**: Use `--max-threads` for large files on fast connections

## Troubleshooting

### Connection Issues

```bash
# Test your connection
rescale-int config test

# Check configuration
rescale-int config show

# Verify API key is set
echo $RESCALE_API_KEY
```

### File Upload Failures

```bash
# Check file exists and is readable
ls -lh input.txt

# Try with verbose logging
rescale-int upload input.txt --verbose

# Large files use multipart upload automatically (>100MB)
```

### Job Issues

```bash
# Check job status
rescale-int jobs get --job-id WfbQa

# View job logs (polls every 10 seconds by default)
rescale-int jobs tail --job-id WfbQa

# List job files to verify outputs
rescale-int jobs listfiles --job-id WfbQa
```

## Support

For issues and feature requests:
- GitHub Issues: https://github.com/rescale/rescale-int/issues
- Documentation: https://docs.rescale.com

## Version & Release Notes

This guide is for `rescale-int` v4.7.1 (February 21, 2026)

View version:
```bash
rescale-int --version
# Output: rescale-int version v4.7.1 (2026-02-21) [FIPS 140-3]
```

### v4.2.1 Enhanced Eligibility Configuration (January 9, 2026)

Enhanced auto-download eligibility settings and workspace validation:

1. **Configurable eligibility settings** - New config keys in `daemon.conf`:
   - `eligibility.auto_download_value` - Required value for "Auto Download" field (default: "Enable")
   - `eligibility.downloaded_tag` - Tag added after download (default: "autoDownloaded:true")

2. **Workspace validation** - New `daemon config validate` command:
   - Checks if required "Auto Download" custom field exists in workspace
   - Reports field type and available values
   - Provides setup instructions if field is missing

3. **API validation method** - New `ValidateAutoDownloadSetup()` API method:
   - Used by GUI and CLI to validate workspace configuration
   - Returns detailed information about custom field setup

### v4.2.0 Unified Daemon Configuration (January 8, 2026)

Unified daemon configuration with new `daemon.conf` file:

1. **Unified daemon.conf** - Single INI config file for all daemon settings
   - Location: `~/.config/rescale/daemon.conf` (Unix) or `%APPDATA%\Rescale\Interlink\daemon.conf` (Windows)
   - Replaces scattered config settings with one organized file
   - Includes daemon, filters, eligibility, and notification settings

2. **CLI config commands** - New `daemon config` subcommands
   - `daemon config show` - Display current configuration
   - `daemon config path` - Show config file location
   - `daemon config edit` - Open in default editor
   - `daemon config set <key> <value>` - Set individual values
   - `daemon config init` - Interactive setup wizard

3. **Config file + CLI flags** - Flexible configuration model
   - Daemon reads from config file by default
   - CLI flags override config file values
   - Allows testing without modifying config

4. **Windows tray improvements**
   - "Configure..." menu opens GUI to daemon settings
   - "Start Service" reads from daemon.conf

### v3.0.1 Streaming Encryption (November 28, 2025)

Major release with streaming encryption and mandatory FIPS compliance:

1. **Streaming Encryption** - Per-part AES-256-CBC encryption during upload
   - No temporary encrypted file (saves disk space for large files)
   - HKDF-SHA256 key derivation per part (FIPS 140-3 compliant)
   - Format detection on download (backward compatible)

2. **--pre-encrypt Flag** - Legacy encryption mode for compatibility
   - Pre-encrypts entire file before upload
   - Compatible with older Rescale clients and Python client

3. **Mandatory FIPS 140-3** - Non-FIPS binaries refuse to run
   - Exit code 2 if FIPS not enabled
   - Development opt-out: `RESCALE_ALLOW_NON_FIPS=true`

### v2.7.0 FIPS 140-3 Compliance (November 26, 2025)

Security and compliance improvements:

1. **FIPS 140-3 Compliance** - Built with Go 1.24's native FIPS module for FedRAMP Moderate
   - All cryptographic operations use FIPS-validated algorithms
   - Build with `GOFIPS140=latest` (automatic via Makefile)
   - Verify with `--version` (shows `[FIPS 140-3]`)

2. **API Key Precedence** - Clear warnings when multiple API key sources detected
   - Priority: `--api-key` > `RESCALE_API_KEY` env > `--token-file` > default token file
   - Warning message shows which source is being used

3. **Security Fix** - API key display now shows length only, not partial key
   - `config show` displays `<set (40 chars)>` instead of partial key

4. **Dependency Update** - Updated `golang.org/x/crypto` to fix SSH vulnerabilities

### v2.5.0 CLI Usability Improvements (November 22, 2025)

Short flags and improved defaults:

1. **Short Flags** - All commands now support single-letter flags
   - `-s` for `--search`, `-j` for `--job-id`, `-o` for `--outdir`, etc.
   - Aligned with `rescale-cli` conventions where applicable
   - Examples: `hardware list -s emerald -J`, `jobs download -j WfbQa -d ./results -w`

2. **Hardware List Default** - Now shows active hardware only by default
   - Use `-a/--all` to include inactive/deprecated types
   - Before: `--active` flag filtered to active; After: active is default

### v2.4.9 Security Improvements (November 22, 2025)

Security enhancements and bug fixes:

1. **Credential Security** - API keys and proxy passwords no longer stored in config files
   - Use `RESCALE_API_KEY` environment variable or `--token-file` flag
   - Existing config files with credentials are silently ignored (backward compatible)

2. **Token File Support** - New `--token-file` flag for secure API key storage
   - Create file with `chmod 600` permissions
   - Recommended for scripts and automation

3. **Secure Proxy Authentication** - Passwords prompted at runtime
   - Not stored in config files
   - Not echoed to terminal during input

4. **Bug Fixes** - Pipeline resource leak, S3 context leak, PKCS7 verification

### v2.3.0 Bug Fixes (November 17, 2025)

Three critical bug fixes completed:

1. **Resume Logic Fix** - Resume now correctly handles PKCS7 padding (1-16 bytes) when checking encrypted file sizes
   - Prevents unnecessary re-downloads of complete files
   - Enhanced error messages show expected size range on mismatch
   - **Source:** `internal/cli/download_helper.go:163-186`

2. **Decryption Progress Feedback** - Added progress message before large file decryption
   - Prevents confusion during long decryption operations (40+ minutes for 60GB files)
   - Message: "Decrypting filename.dat (this may take several minutes for large files)..."
   - **Source:** `internal/cloud/download/s3_concurrent.go:458`, `azure_concurrent.go:483`

3. **Progress Bar Corruption Fix** - Routed all output through mpb io.Writer
   - Prevents progress bar corruption and "ghost bars"
   - All print statements now use proper output writer
   - **Fixed across 17 files**

See [RELEASE_NOTES.md](RELEASE_NOTES.md) for complete version history and [FEATURE_SUMMARY.md](FEATURE_SUMMARY.md) for comprehensive feature details.
