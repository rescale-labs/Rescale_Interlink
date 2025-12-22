#!/bin/bash
#
# release-windows-build.sh - Automate Windows binary builds on Rescale
#
# This script uses rescale-int to:
# 1. Generate a PowerShell build script with embedded Rescale metadata
# 2. Submit and monitor the job end-to-end
# 3. Report success/failure
#
# The Rescale job clones the repo via HTTPS (using GITHUB_TOKEN),
# builds the Windows binary with FIPS 140-3 compliance, and saves it
# to the job results. Download the binary manually and upload to GitHub.
#
# Required environment variables:
#   RESCALE_API_KEY - Rescale API token
#   GITHUB_TOKEN - GitHub PAT with repo scope (used for git clone)
#
# Usage:
#   ./release-windows-build.sh
#
# Version: 3.2.4
# Date: 2025-12-11

set -euo pipefail

# =============================================================================
# Parse Arguments
# =============================================================================
for arg in "$@"; do
    case $arg in
        --help|-h)
            echo "Usage: $0"
            echo ""
            echo "Builds Windows binary on Rescale. Download from job results and upload to GitHub manually."
            echo ""
            echo "Required environment variables:"
            echo "  RESCALE_API_KEY  Rescale API token"
            echo "  GITHUB_TOKEN     GitHub PAT with repo scope (for git clone)"
            exit 0
            ;;
    esac
done

# =============================================================================
# Configuration
# =============================================================================
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RESCALE_INT_DIR="$(dirname "$SCRIPT_DIR")"
MAKEFILE="$RESCALE_INT_DIR/Makefile"

# Job configuration - Windows Server 2019
CORE_TYPE="calcitev2"
CORES_PER_SLOT=2
SLOTS=1
WALLTIME_HOURS=1  # 1 hour

# GitHub repository
REPO_OWNER="rescale-labs"
REPO_NAME="Rescale_Interlink"
# BRANCH is set after VERSION is extracted from Makefile (see below)

# =============================================================================
# Helper Functions
# =============================================================================
cleanup() {
    local exit_code=$?
    # Clean up temp scripts
    rm -f "/tmp/interlink-build-windows.ps1" 2>/dev/null || true
    rm -f "/tmp/interlink-build-windows.bat" 2>/dev/null || true
    if [ $exit_code -ne 0 ]; then
        echo ""
        echo "=============================================="
        echo "BUILD FAILED"
        echo "=============================================="
    fi
}
trap cleanup EXIT

error() {
    echo "ERROR: $1" >&2
    exit 1
}

# =============================================================================
# Pre-flight Checks
# =============================================================================
echo "=============================================="
echo "Rescale Interlink Windows Release Builder"
echo "=============================================="
echo ""

# Check required environment variables
[ -z "${RESCALE_API_KEY:-}" ] && error "RESCALE_API_KEY environment variable is not set"
[ -z "${GITHUB_TOKEN:-}" ] && error "GITHUB_TOKEN environment variable is not set"

# Check Makefile exists and extract version
[ ! -f "$MAKEFILE" ] && error "Makefile not found at: $MAKEFILE"

VERSION=$(grep -E '^VERSION\s*:=' "$MAKEFILE" | sed 's/.*:=\s*//' | tr -d '[:space:]')
[ -z "$VERSION" ] && error "Could not extract VERSION from Makefile"

# Set BRANCH based on VERSION
BRANCH="release/${VERSION}"

echo "Version: $VERSION"
echo "Repository: ${REPO_OWNER}/${REPO_NAME}"
echo "Branch: ${BRANCH}"
echo ""

# Find rescale-int binary
RESCALE_INT_BIN="$RESCALE_INT_DIR/bin/v3.2.4/darwin-arm64/rescale-int"
if [ ! -x "$RESCALE_INT_BIN" ]; then
    RESCALE_INT_BIN=$(find "$RESCALE_INT_DIR/bin" -name "rescale-int" -type f 2>/dev/null | head -1)
fi
[ ! -x "$RESCALE_INT_BIN" ] && error "rescale-int binary not found. Please build it first with 'make build'"

echo "Using: $RESCALE_INT_BIN"
echo ""

# =============================================================================
# Generate Windows Build Scripts
# =============================================================================
echo "Generating Windows build scripts..."

JOB_NAME="Interlink Windows Build - ${VERSION}"

# Create wrapper batch file that calls PowerShell
# This is needed because Rescale Windows jobs run .bat files
WRAPPER_SCRIPT="/tmp/interlink-build-windows.bat"
cat > "$WRAPPER_SCRIPT" << 'WRAPPER_EOF'
@echo off
REM Wrapper to execute PowerShell build script
REM Rescale Windows jobs execute .bat files, so we use this to launch PowerShell

echo ============================================
echo Starting PowerShell build script...
echo ============================================

powershell.exe -ExecutionPolicy Bypass -File "%~dp0interlink-build-windows.ps1"

echo ============================================
echo PowerShell script completed with exit code: %ERRORLEVEL%
echo ============================================
exit /b %ERRORLEVEL%
WRAPPER_EOF

# Create PowerShell build script
PS_SCRIPT="/tmp/interlink-build-windows.ps1"
cat > "$PS_SCRIPT" << PSHEADER
# Rescale Interlink Windows Build Script
# Generated by release-windows-build.sh
# Version: ${VERSION}
#
# Environment variables passed from Rescale:
#   GITHUB_TOKEN - GitHub PAT for repo access
#   RELEASE_TAG - Version tag
#   REPO_OWNER - GitHub repo owner
#   REPO_NAME - GitHub repo name

\$ErrorActionPreference = "Stop"

PSHEADER

# Add the main build logic
cat >> "$PS_SCRIPT" << 'PSBODY'
Write-Host "=============================================="
Write-Host "Rescale Interlink Windows Build"
Write-Host "=============================================="
Write-Host "Release Tag: $env:RELEASE_TAG"
Write-Host "Repository: $env:REPO_OWNER/$env:REPO_NAME"
Write-Host "Started: $(Get-Date)"
Write-Host ""

# Set up paths
$WorkDir = $PWD.Path
$BuildDir = "C:\build"
$LogFile = Join-Path $WorkDir "build.log"

# Create build directory
New-Item -ItemType Directory -Force -Path $BuildDir | Out-Null

# Start transcript for logging
Start-Transcript -Path $LogFile -Append

Write-Host "Build log: $LogFile"
Write-Host "Work directory: $WorkDir"
Write-Host "Build directory: $BuildDir"

Set-Location $BuildDir

# Global settings for non-interactive mode
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"  # Prevents console buffer overflow with Write-Progress

# =============================================================================
# Helper Function: Run noisy tools without console buffer issues
# =============================================================================
# PowerShell in non-interactive mode (like Rescale jobs) can crash when
# external commands produce lots of stdout/stderr. This helper uses
# Start-Process with file redirection to completely bypass the console buffer.
function Invoke-ToolQuiet {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [Parameter(Mandatory = $true)][string]$Arguments,
        [Parameter(Mandatory = $true)][string]$LogName
    )

    $stdoutLog = Join-Path $BuildDir "$LogName-stdout.log"
    $stderrLog = Join-Path $BuildDir "$LogName-stderr.log"

    Write-Host "Running: $FilePath $Arguments"
    Write-Host "  stdout -> $stdoutLog"
    Write-Host "  stderr -> $stderrLog"

    # Note: -NoNewWindow removed for better PS7+ compatibility
    $proc = Start-Process -FilePath $FilePath `
                          -ArgumentList $Arguments `
                          -Wait `
                          -PassThru `
                          -RedirectStandardOutput $stdoutLog `
                          -RedirectStandardError $stderrLog

    # Guard against null ExitCode (can happen in some PowerShell versions)
    if ($null -ne $proc.ExitCode -and $proc.ExitCode -ne 0) {
        Write-Host "Command failed with exit code $($proc.ExitCode). Last 50 lines of stderr:"
        if (Test-Path $stderrLog) {
            Get-Content $stderrLog -Tail 50
        }
        throw "Command '$FilePath $Arguments' failed with exit code $($proc.ExitCode). See logs: $stdoutLog, $stderrLog"
    }
}

# =============================================================================
# Helper Function: Ensure Chocolatey is installed
# =============================================================================
function Ensure-Chocolatey {
    if (Get-Command choco.exe -ErrorAction SilentlyContinue) {
        Write-Host "Chocolatey already installed"
        return
    }

    Write-Host "Chocolatey not found, installing..."
    Set-ExecutionPolicy Bypass -Scope Process -Force
    [System.Net.ServicePointManager]::SecurityProtocol = [System.Net.ServicePointManager]::SecurityProtocol -bor 3072
    Invoke-Expression ((New-Object System.Net.WebClient).DownloadString('https://community.chocolatey.org/install.ps1'))

    if (-not (Get-Command choco.exe -ErrorAction SilentlyContinue)) {
        throw "Chocolatey installation failed - choco.exe not on PATH"
    }
    Write-Host "Chocolatey installed successfully"
}

# =============================================================================
# Helper Function: Find MinGW bin path dynamically
# =============================================================================
function Get-MinGWBinPath {
    # Check common locations first
    $candidates = @(
        "C:\ProgramData\chocolatey\lib\mingw\tools\install\mingw64\bin",
        "C:\ProgramData\mingw64\mingw64\bin",
        "C:\mingw64\bin"
    )

    foreach ($p in $candidates) {
        if (Test-Path (Join-Path $p "gcc.exe")) {
            return $p
        }
    }

    # Fallback: search under Chocolatey mingw lib
    $root = "C:\ProgramData\chocolatey\lib\mingw"
    if (Test-Path $root) {
        $gcc = Get-ChildItem -Path $root -Filter "gcc.exe" -Recurse -ErrorAction SilentlyContinue |
               Select-Object -First 1
        if ($gcc) { return $gcc.DirectoryName }
    }

    return $null
}

# =============================================================================
# Step 1: Install Go 1.24.2 for Windows
# =============================================================================
Write-Host ""
Write-Host "[1/4] Installing Go 1.24.2..."

$GoVersion = "1.24.2"
$GoZip = "go${GoVersion}.windows-amd64.zip"
$GoUrl = "https://go.dev/dl/$GoZip"
$GoInstallDir = "C:\Go"

Write-Host "Downloading Go from: $GoUrl"
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
$ProgressPreference = 'SilentlyContinue'  # Disable progress bar (fixes non-interactive console bug)
(New-Object System.Net.WebClient).DownloadFile($GoUrl, (Join-Path $BuildDir $GoZip))

Write-Host "Extracting Go..."
$GoZipPath = Join-Path $BuildDir $GoZip
if (Test-Path $GoInstallDir) {
    Remove-Item -Recurse -Force $GoInstallDir
}
Expand-Archive -Path $GoZipPath -DestinationPath "C:\" -Force
Remove-Item $GoZipPath

# Update PATH for this session
$env:PATH = "$GoInstallDir\bin;$env:PATH"
$env:GOPATH = "$env:USERPROFILE\go"
$env:PATH = "$env:GOPATH\bin;$env:PATH"

Write-Host "Go version:"
$goResult = cmd /c "go version 2>&1"
Write-Host $goResult
if (-not $goResult -match "go version") { throw "Go installation failed" }

# =============================================================================
# Step 2: Install Git (if not present) and Clone Repository
# =============================================================================
Write-Host ""
Write-Host "[2/4] Setting up Git and cloning repository..."

# Check if Git is available
$GitPath = Get-Command git -ErrorAction SilentlyContinue
if (-not $GitPath) {
    Write-Host "Git not found, installing via winget..."
    # Try winget first (Windows Server 2019 may not have it)
    # Use Invoke-ToolQuiet to avoid console buffer overflow in non-interactive mode
    try {
        Invoke-ToolQuiet -FilePath "winget" `
                         -Arguments "install --id Git.Git -e --source winget --accept-package-agreements --accept-source-agreements --silent" `
                         -LogName "winget-git"

        # Refresh PATH after install
        $env:PATH = [System.Environment]::GetEnvironmentVariable("PATH", "Machine") + ";" +
                    [System.Environment]::GetEnvironmentVariable("PATH", "User")
    }
    catch {
        Write-Host "winget failed, trying Chocolatey..."

        # Ensure Chocolatey is installed
        Ensure-Chocolatey

        Invoke-ToolQuiet -FilePath "choco" `
                         -Arguments "install git -y --no-progress --limit-output" `
                         -LogName "choco-git"

        # Refresh PATH from persisted machine+user env (more robust than hard-coding)
        $env:PATH = [System.Environment]::GetEnvironmentVariable("PATH", "Machine") + ";" +
                    [System.Environment]::GetEnvironmentVariable("PATH", "User")
    }
}

# Verify Git
Write-Host "Git version:"
$gitResult = cmd /c "git --version 2>&1"
Write-Host $gitResult
if (-not $gitResult -match "git version") { throw "Git installation failed" }

# Clone repository using HTTPS with token
$RepoUrl = "https://x-access-token:$($env:GITHUB_TOKEN)@github.com/$($env:REPO_OWNER)/$($env:REPO_NAME).git"
$BranchName = "release/$($env:RELEASE_TAG)"

Write-Host "Cloning repository (branch: $BranchName)..."
Invoke-ToolQuiet -FilePath "git" `
                 -Arguments "clone --depth 1 --branch $BranchName $RepoUrl" `
                 -LogName "git-clone"

if (-not (Test-Path $env:REPO_NAME)) { throw "Git clone failed" }

Write-Host "Repository cloned successfully"

# =============================================================================
# Step 3: Install MinGW-w64 (C compiler for CGO)
# =============================================================================
Write-Host ""
Write-Host "[3/4] Installing MinGW-w64 (C compiler for CGO)..."

# Ensure Chocolatey is available (may not be if Git was pre-installed)
Ensure-Chocolatey

# Install MinGW-w64 for CGO support (required for Fyne GUI)
Invoke-ToolQuiet -FilePath "choco" `
                 -Arguments "install mingw -y --no-progress --limit-output" `
                 -LogName "choco-mingw"

# Find MinGW bin path dynamically (handles different install locations)
$MinGWBin = Get-MinGWBinPath
if (-not $MinGWBin) {
    throw "MinGW installed but gcc.exe not found under known locations"
}
Write-Host "Found MinGW at: $MinGWBin"

# Add MinGW to PATH and set CC for CGO
$env:PATH = "$MinGWBin;$env:PATH"
$env:CC = Join-Path $MinGWBin "gcc.exe"

# Verify GCC is available
Write-Host "GCC version:"
$gccResult = & $env:CC --version 2>&1
Write-Host $gccResult

# =============================================================================
# Step 4: Download Mesa DLLs for software rendering
# =============================================================================
Write-Host ""
Write-Host "[4/5] Downloading Mesa DLLs for embedded software rendering..."

Set-Location $env:REPO_NAME

# Create the mesa/dlls directory if it doesn't exist
$MesaDllDir = "internal\mesa\dlls"
New-Item -ItemType Directory -Force -Path $MesaDllDir | Out-Null

# Download Mesa release (using direct DLL URLs from GitHub release)
$MesaVersion = "24.2.7"
$MesaBaseUrl = "https://github.com/pal1000/mesa-dist-win/releases/download/$MesaVersion"
$Mesa7zFile = "mesa3d-$MesaVersion-release-msvc.7z"

Write-Host "Downloading Mesa $MesaVersion..."
$ProgressPreference = 'SilentlyContinue'
(New-Object System.Net.WebClient).DownloadFile("$MesaBaseUrl/$Mesa7zFile", (Join-Path $BuildDir $Mesa7zFile))

# Install 7-Zip to extract the archive
Write-Host "Installing 7-Zip for extraction..."
Invoke-ToolQuiet -FilePath "choco" `
                 -Arguments "install 7zip -y --no-progress --limit-output" `
                 -LogName "choco-7zip"

# Refresh PATH to find 7z
$env:PATH = [System.Environment]::GetEnvironmentVariable("PATH", "Machine") + ";" +
            [System.Environment]::GetEnvironmentVariable("PATH", "User") + ";" +
            "C:\Program Files\7-Zip"

# Extract only the needed DLLs
$7zExe = "C:\Program Files\7-Zip\7z.exe"
$MesaArchive = Join-Path $BuildDir $Mesa7zFile
Write-Host "Extracting Mesa DLLs..."
& $7zExe e -y $MesaArchive "x64/opengl32.dll" "x64/libgallium_wgl.dll" "x64/libglapi.dll" "-o$MesaDllDir" | Out-Null

# Verify DLLs were extracted
$requiredDlls = @("opengl32.dll", "libgallium_wgl.dll", "libglapi.dll")
foreach ($dll in $requiredDlls) {
    if (-not (Test-Path (Join-Path $MesaDllDir $dll))) {
        throw "Failed to extract Mesa DLL: $dll"
    }
}
Write-Host "Mesa DLLs extracted to $MesaDllDir"
Get-ChildItem $MesaDllDir | Format-Table Name, Length

# =============================================================================
Write-Host ""
Write-Host "[5/5] Building Windows AMD64 binary with FIPS 140-3..."

$BuildTime = Get-Date -Format "yyyy-MM-dd"
$LdFlags = "-s -w -X main.Version=$($env:RELEASE_TAG) -X main.BuildTime=$BuildTime"

Write-Host "Build flags: GOFIPS140=latest GOOS=windows GOARCH=amd64 CGO_ENABLED=1 -tags mesa"
Write-Host "LDFLAGS: $LdFlags"

# Set environment variables for FIPS build with CGO enabled
$env:GOFIPS140 = "latest"
$env:GOOS = "windows"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "1"  # Enable CGO for GUI support (requires MinGW)

$buildArgs = "build -tags mesa -ldflags `"$LdFlags`" -o rescale-int.exe ./cmd/rescale-int"
$GoExe = "$GoInstallDir\bin\go.exe"  # Use full path - Start-Process needs it with redirects
Write-Host "Running: $GoExe $buildArgs"
Invoke-ToolQuiet -FilePath $GoExe `
                 -Arguments $buildArgs `
                 -LogName "go-build"

if (-not (Test-Path "rescale-int.exe")) { throw "Go build failed - binary not created" }

Write-Host ""
Write-Host "Binary built successfully. Version info:"
$versionOutput = cmd /c ".\rescale-int.exe --version 2>&1"
Write-Host $versionOutput

# =============================================================================
# Bundle Mesa DLLs alongside EXE for app-local deployment
# =============================================================================
# Windows loads DLLs from the EXE directory before System32 (when not in KnownDLLs).
# By placing Mesa DLLs next to the EXE, users get software rendering automatically.

Write-Host ""
Write-Host "[5b/5] Bundling Mesa DLLs for app-local deployment..."

# Copy Mesa DLLs to output directory (same location as EXE)
Copy-Item "$MesaDllDir\opengl32.dll" -Destination "."
Copy-Item "$MesaDllDir\libgallium_wgl.dll" -Destination "."
Copy-Item "$MesaDllDir\libglapi.dll" -Destination "."

# Create Mesa license file for compliance
$MesaLicense = @"
Mesa 3D Graphics Library
========================
Copyright (C) 1999-2024 Mesa Authors

This distribution includes Mesa3D software rendering DLLs (llvmpipe driver)
for systems without GPU/OpenGL support.

Mesa is licensed under the MIT license.
See: https://mesa3d.org/license.html
"@
$MesaLicense | Out-File -FilePath "MESA_LICENSE.txt" -Encoding UTF8

Write-Host "Mesa DLLs bundled:"
Get-ChildItem -Name "*.dll"

# Package ALL files together (EXE + DLLs + license)
$BinaryName = "rescale-int-$($env:RELEASE_TAG)-windows-amd64-mesa.zip"
Compress-Archive -Path @(
    "rescale-int.exe",
    "opengl32.dll",
    "libgallium_wgl.dll",
    "libglapi.dll",
    "MESA_LICENSE.txt"
) -DestinationPath $BinaryName -Force

Write-Host "Packaged as: $BinaryName"
Get-Item $BinaryName | Format-List Name, Length

# Copy to work directory for Rescale to save
Copy-Item $BinaryName -Destination $WorkDir
Write-Host "Binary copied to: $WorkDir\$BinaryName"

# Also copy the raw files for convenience
Copy-Item "rescale-int.exe" -Destination $WorkDir
Copy-Item "opengl32.dll" -Destination $WorkDir
Copy-Item "libgallium_wgl.dll" -Destination $WorkDir
Copy-Item "libglapi.dll" -Destination $WorkDir
Write-Host "All files copied to: $WorkDir"

# =============================================================================
# Done - Binary is ready in work directory for download
# =============================================================================
Write-Host ""
Write-Host "=============================================="
Write-Host "SUCCESS: Build complete!"
Write-Host "=============================================="
Write-Host "Release: $($env:RELEASE_TAG)"
Write-Host "Binary: $BinaryName"
Write-Host "Location: $WorkDir\$BinaryName"
Write-Host ""
Write-Host "Download the binary from the job results and upload to GitHub manually."
Write-Host "Completed: $(Get-Date)"

Stop-Transcript
PSBODY

chmod +x "$WRAPPER_SCRIPT"
echo "  Created: $WRAPPER_SCRIPT"
echo "  Created: $PS_SCRIPT"
echo ""

# =============================================================================
# Submit Job using JSON specification
# =============================================================================
echo "Submitting Windows build job to Rescale..."
echo ""

# First, upload the input files and get their IDs
echo "Uploading build scripts..."
WRAPPER_OUTPUT=$("$RESCALE_INT_BIN" upload "$WRAPPER_SCRIPT" 2>&1) || true
WRAPPER_FILE_ID=$(echo "$WRAPPER_OUTPUT" | grep -o 'FileID: [A-Za-z0-9]*' | head -1 | cut -d' ' -f2)
echo "$WRAPPER_OUTPUT"

PS_OUTPUT=$("$RESCALE_INT_BIN" upload "$PS_SCRIPT" 2>&1) || true
PS_FILE_ID=$(echo "$PS_OUTPUT" | grep -o 'FileID: [A-Za-z0-9]*' | head -1 | cut -d' ' -f2)
echo "$PS_OUTPUT"

if [ -z "$WRAPPER_FILE_ID" ] || [ -z "$PS_FILE_ID" ]; then
    # Fallback: upload without JSON parsing
    echo "Uploading wrapper script..."
    "$RESCALE_INT_BIN" upload "$WRAPPER_SCRIPT"
    echo "Uploading PowerShell script..."
    "$RESCALE_INT_BIN" upload "$PS_SCRIPT"
    echo ""
    echo "Files uploaded. Please create the job manually in the Rescale UI with:"
    echo "  - Analysis: Bring Your Own Windows Software (user_included_win:1)"
    echo "  - Hardware: ${CORE_TYPE}, ${CORES_PER_SLOT} cores, ${SLOTS} slot"
    echo "  - Command: interlink-build-windows.bat"
    echo "  - Environment variables:"
    echo "      GITHUB_TOKEN=${GITHUB_TOKEN}"
    echo "      RELEASE_TAG=${VERSION}"
    echo "      REPO_OWNER=${REPO_OWNER}"
    echo "      REPO_NAME=${REPO_NAME}"
    exit 0
fi

echo "  Wrapper script ID: $WRAPPER_FILE_ID"
echo "  PowerShell script ID: $PS_FILE_ID"
echo ""

# Create JSON job specification
JOB_JSON="/tmp/interlink-windows-job.json"
cat > "$JOB_JSON" << EOF
{
  "name": "${JOB_NAME}",
  "jobanalyses": [
    {
      "analysis": {
        "code": "user_included_win",
        "version": "1"
      },
      "hardware": {
        "coreType": {"code": "${CORE_TYPE}"},
        "coresPerSlot": ${CORES_PER_SLOT},
        "slots": ${SLOTS},
        "walltime": ${WALLTIME_HOURS}
      },
      "command": "interlink-build-windows.bat",
      "envVars": {
        "GITHUB_TOKEN": "${GITHUB_TOKEN}",
        "RELEASE_TAG": "${VERSION}",
        "REPO_OWNER": "${REPO_OWNER}",
        "REPO_NAME": "${REPO_NAME}"
      },
      "inputFiles": [
        {"id": "${WRAPPER_FILE_ID}"},
        {"id": "${PS_FILE_ID}"}
      ]
    }
  ]
}
EOF

echo "Job specification created: $JOB_JSON"
echo ""

# Submit using job file
"$RESCALE_INT_BIN" jobs submit --job-file "$JOB_JSON" --end-to-end

# =============================================================================
# Done
# =============================================================================
echo ""
echo "=============================================="
echo "BUILD COMPLETE"
echo "=============================================="
echo ""
echo "The Windows binary is available in the job results."
echo "Download it and upload to GitHub manually:"
echo "  https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/tag/${VERSION}"
echo ""
