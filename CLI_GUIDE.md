# Rescale Interlink CLI Guide

Complete command-line interface reference for `rescale-int` v2.4.8.

**Version:** 2.4.8
**Build Date:** November 20, 2025
**Status:** Production Ready

For a comprehensive list of all features with source code references, see [FEATURE_SUMMARY.md](FEATURE_SUMMARY.md).

## Table of Contents

- [Installation](#installation)
- [Configuration](#configuration)
- [Global Flags](#global-flags)
- [Quick Start](#quick-start)
- [Command Reference](#command-reference)
  - [Config Commands](#config-commands)
  - [File Commands](#file-commands)
  - [Folder Commands](#folder-commands)
  - [Job Commands](#job-commands)
  - [PUR Commands](#pur-commands)
  - [Shortcuts](#shortcuts)
- [Shell Completion](#shell-completion)
- [Examples](#examples)

## Installation

Download the appropriate binary for your platform from the releases page:

- **macOS ARM64**: `rescale-int-darwin-arm64`
- **macOS Intel**: `rescale-int-darwin-amd64`
- **Linux**: `rescale-int-linux-amd64`
- **Windows**: `rescale-int-windows.exe`

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

Configuration is saved to `~/.config/rescale/pur_config.csv`

### Manual Configuration

Create a CSV file with key-value pairs:

```csv
key,value
api_key,your-api-key-here
api_base_url,https://platform.rescale.com
tar_workers,4
upload_workers,4
job_workers,4
proxy_mode,no-proxy
```

### Environment Variables

You can also use environment variables:

```bash
export RESCALE_API_KEY="your-api-key"
export RESCALE_API_URL="https://platform.rescale.com"
```

### Priority Order

Configuration values are merged with this priority:
1. Command-line flags (highest)
2. Environment variables
3. Configuration file
4. Default values (lowest)

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

**`--parts-per-file N`** - Parts per file for concurrent uploads/downloads (0 = auto, range: 1-10)
```bash
rescale-int files upload large_file.dat --parts-per-file 5
```

### Configuration Overrides

**`--config, -c PATH`** - Use specific configuration file
```bash
rescale-int pur run --config myconfig.csv --jobs jobs.csv --state state.csv
```

**`--api-key KEY`** - Override API key from configuration
```bash
rescale-int files list --api-key your-api-key-here
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
rescale-int run jobs.csv --state state.csv
```

## Command Reference

### Config Commands

#### config init
Initialize configuration interactively

```bash
rescale-int config init [--force]
```

**Flags:**
- `--force` - Overwrite existing configuration

**Example:**
```bash
rescale-int config init
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
- Multi-part upload for files >100MB (16MB chunks)
- Automatic resume on interruption (state saved to `.rescale-upload-state`)
- Progress bars with transfer speed and ETA
- Support for both S3 and Azure storage backends

**Flags:**
- `--folder-id string` - Target folder ID
- `--description string` - File description

**Examples:**
```bash
# Upload single file (automatically encrypted)
rescale-int files upload input.txt

# Upload multiple files
rescale-int files upload data1.csv data2.csv results.tar.gz

# Upload to specific folder
rescale-int files upload model.tar.gz --folder-id abc123

# Upload with description
rescale-int files upload simulation.dat --description "CFD simulation input"

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
- Chunked download for large files (>100MB, 16MB chunks)
- Progress bars during download and decryption
- Resume capability for interrupted downloads (state saved to `.rescale-download-state`)
- **v2.3.0 Fix:** Correct PKCS7 padding handling (1-16 bytes) in resume logic
- **v2.3.0 Enhancement:** Progress message before large file decryption
- Streaming decryption using 16KB chunks (prevents memory exhaustion)
- Concurrent chunk downloads for large files

**Flags:**
- `-d, --outdir string` - Output directory for multiple files
- `-o, --output string` - Output file path (for single file)

**Examples:**
```bash
# Download single file (automatically decrypted)
rescale-int files download abc123 -o result.tar.gz

# Download multiple files
rescale-int files download abc123 def456 ghi789 --outdir ./downloads

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

#### folders upload
Upload files to a folder

```bash
rescale-int folders upload <file> [file...] --folder-id ID
```

**Flags:**
- `--folder-id string` - Target folder ID (required)

**Performance Note:** This command reuses the same API connection for all files, providing **5-10x speedup** compared to uploading files individually.

**New in v2.0.2**: Professional mpb-based multi-progress bars show individual progress for concurrent uploads with real-time EWMA speed and ETA calculations.

**Example:**
```bash
rescale-int folders upload data1.csv data2.csv results.tar.gz --folder-id abc123

# Example output (TTY mode with progress bars):
# [1/3] file1.tar.gz (700 MB) → my_folder  35% | 245 MB / 700 MB | 15.2 MB/s | ETA: 30s
# [2/3] file2.tar.gz (700 MB) → my_folder  50% | 350 MB / 700 MB | 18.5 MB/s | ETA: 19s
# [3/3] file3.tar.gz (700 MB) → my_folder  15% | 105 MB / 700 MB | 12.1 MB/s | ETA: 49s
#
# Example output (non-TTY/pipe mode):
# Uploading [1/3]: file1.tar.gz (700.0 MB) → my_folder
# ✓ file1.tar.gz → my_folder (FileID: abc123, 700.0 MB, 46s, 15.2 MB/s)
```

#### folders upload-dir
Upload entire directory to a folder

```bash
rescale-int folders upload-dir --folder-id ID --dir PATH [-r]
```

**Flags:**
- `--folder-id string` - Target folder ID (required)
- `--dir string` - Directory to upload (required)
- `-r, --recursive` - Upload directory recursively

**Performance Note:** Concurrent uploads (max 3 simultaneous) with connection reuse for maximum throughput.

**New in v2.0.1**:
- Folder caching reduces API calls by 99.8% for repeated operations
- Disk space pre-checking prevents mid-operation failures
- Multi-progress bars for concurrent uploads

**Examples:**
```bash
# Upload directory (files only)
rescale-int folders upload-dir --folder-id abc123 --dir ./simulation_data

# Upload directory recursively
rescale-int folders upload-dir --folder-id abc123 --dir ./project -r

# Example: Folder caching in action
# First run: 1 API call to resolve folder
# Subsequent runs: Instant lookup from cache (99.8% faster)
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
rescale-int jobs get --id <job-id>
```

**Flags:**
- `--id, --job-id string` - Job ID (required)

**Example:**
```bash
rescale-int jobs get --id WfbQa
```

#### jobs stop
Stop a running job

```bash
rescale-int jobs stop --id <job-id>
```

**Flags:**
- `--id, --job-id string` - Job ID (required)

**Example:**
```bash
rescale-int jobs stop --id WfbQa
```

#### jobs tail
Stream job log output

```bash
rescale-int jobs tail --id <job-id> [flags]
```

**Flags:**
- `--id, --job-id string` - Job ID (required)
- `-f, --follow` - Follow log output (like `tail -f`)
- `-n, --lines int` - Number of lines to show (default 50)

**Examples:**
```bash
# View last 50 lines
rescale-int jobs tail --id WfbQa

# Follow log output in real-time
rescale-int jobs tail --id WfbQa --follow

# View last 100 lines
rescale-int jobs tail --id WfbQa -n 100
```

#### jobs listfiles
List files in a job

```bash
rescale-int jobs listfiles --id <job-id>
```

**Flags:**
- `--id, --job-id string` - Job ID (required)

**Example:**
```bash
rescale-int jobs listfiles --id WfbQa
```

#### jobs download
Download job output files

```bash
rescale-int jobs download --id <job-id> [flags]
```

**Modes:**
1. **Batch download** (no `--file-id`): Download all job output files
2. **Single file** (with `--file-id`): Download specific file

**Flags:**
- `--id, --job-id string` - Job ID (required)
- `--file-id string` - Specific file ID to download (optional)
- `-d, --outdir string` - Output directory for batch download
- `-o, --output string` - Output file path (for single file)

**Examples:**
```bash
# Download all job files to current directory
rescale-int jobs download --id WfbQa

# Download all job files to specific directory
rescale-int jobs download --id WfbQa --outdir ./results

# Download specific file
rescale-int jobs download --id WfbQa --file-id xyz789 -o result.tar.gz
```

#### jobs delete
Delete jobs

```bash
rescale-int jobs delete --id <job-id> [--id <job-id>...] [--confirm]
```

**Flags:**
- `--id, --job-id string` - Job ID to delete (can be specified multiple times)
- `--confirm` - Skip confirmation prompt

**Examples:**
```bash
# Delete single job (with confirmation)
rescale-int jobs delete --id WfbQa

# Delete multiple jobs
rescale-int jobs delete --id WfbQa --id XyzBb --id AbcCc

# Delete without confirmation
rescale-int jobs delete --id WfbQa --confirm
```

#### jobs submit
Create and submit a job from JSON specification

```bash
rescale-int jobs submit --job-file <file> [--no-submit]
```

**Flags:**
- `-f, --job-file string` - Path to job specification JSON file (required)
- `--no-submit` - Create job but don't submit it

**Example:**
```bash
rescale-int jobs submit --job-file job_spec.json
```

### PUR Commands

PUR (Parallel Uploader and Runner) provides batch job submission with pipeline management.

#### pur make-dirs-csv
Generate jobs CSV from directory pattern

```bash
rescale-int pur make-dirs-csv --template TEMPLATE --output OUTPUT --pattern PATTERN [--overwrite]
```

**Flags:**
- `-t, --template string` - Template CSV file (required)
- `-o, --output string` - Output jobs CSV file (required)
- `-p, --pattern string` - Directory pattern, e.g., 'Run_*' (required)
- `--overwrite` - Overwrite existing output file

**Example:**
```bash
rescale-int pur make-dirs-csv \
  --template template.csv \
  --output jobs.csv \
  --pattern "Run_*"
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

**Example:**
```bash
rescale-int pur run --jobs-csv jobs.csv --state state.csv
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

**Example:**
```bash
rescale-int pur resume --jobs-csv jobs.csv --state state.csv
```

#### pur submit-existing
Submit jobs using existing uploaded file IDs

```bash
rescale-int pur submit-existing --jobs-csv FILE [--state FILE]
```

Skips tar and upload phases. Use when files are already uploaded to Rescale.

**Flags:**
- `--jobs-csv string` - Jobs CSV file with extrainputfileids column
- `--state string` - State file

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

#### run
Shortcut for `pur run`

```bash
rescale-int run <jobs-csv> [--state FILE] [--multipart]
```

**Example:**
```bash
rescale-int run jobs.csv --state state.csv
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
rescale-int folders upload-dir \
  --folder-id abc123 \
  --dir ./simulation_cases \
  --recursive

# List folder contents
rescale-int folders list --folder-id abc123
```

### Job Management

```bash
# List running jobs
rescale-int ls --status Running

# Get job details
rescale-int jobs get --id WfbQa

# Stream job log in real-time
rescale-int jobs tail --id WfbQa --follow

# Download all job outputs
rescale-int jobs download --id WfbQa --outdir ./results

# Stop job
rescale-int jobs stop --id WfbQa

# Delete old jobs
rescale-int jobs delete --id job1 --id job2 --confirm
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
rescale-int run jobs.csv --state state.csv

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
    rescale-int jobs download --id "$job_id" --outdir "./job_$job_id"
  done
```

**Monitor job until completion:**
```bash
job_id="WfbQa"
while true; do
  status=$(rescale-int jobs get --id "$job_id" | grep "Status:" | awk '{print $2}')
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

# For large files, use multipart mode
rescale-int run jobs.csv --multipart
```

### Job Issues

```bash
# Check job status
rescale-int jobs get --id WfbQa

# View job logs
rescale-int jobs tail --id WfbQa -n 200

# List job files to verify outputs
rescale-int jobs listfiles --id WfbQa
```

## Support

For issues and feature requests:
- GitHub Issues: https://github.com/anthropics/rescale-int/issues
- Documentation: https://docs.rescale.com

## Version & Release Notes

This guide is for `rescale-int` v2.4.8 (November 20, 2025)

View version:
```bash
rescale-int --version
```

### v2.3.0 Bug Fixes (November 17, 2025)

Three critical bug fixes completed:

1. **Resume Logic Fix** - Resume now correctly handles PKCS7 padding (1-16 bytes) when checking encrypted file sizes
   - Prevents unnecessary re-downloads of complete files
   - Enhanced error messages show expected size range on mismatch
   - **Source:** `internal/cli/download_helper.go:163-186`

2. **Decryption Progress Feedback** - Added progress message before large file decryption
   - Prevents confusion during long decryption operations (40+ minutes for 60GB files)
   - Message: "Decrypting filename.dat (this may take several minutes for large files)..."
   - **Source:** `internal/pur/download/s3_concurrent.go:458`, `azure_concurrent.go:483`

3. **Progress Bar Corruption Fix** - Routed all output through mpb io.Writer
   - Prevents progress bar corruption and "ghost bars"
   - All print statements now use proper output writer
   - **Fixed across 17 files**

See [RELEASE_NOTES.md](RELEASE_NOTES.md) for complete version history and [FEATURE_SUMMARY.md](FEATURE_SUMMARY.md) for comprehensive feature details.
