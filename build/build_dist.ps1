param (
    [string]$ReleaseTag = $env:RELEASE_TAG
)

# Set up paths
$WorkDir = $PWD.Path
$BuildDir = Join-Path $WorkDir "_build"
$BinDir = Join-Path $BuildDir "bin"
$LogFile = Join-Path $WorkDir "build.log"

New-Item -ItemType Directory -Force -Path $BuildDir | Out-Null
New-Item -ItemType Directory -Force -Path $BinDir | Out-Null

Start-Transcript -Path $LogFile -Append

Write-Host "Build log: $LogFile"
Write-Host "Work directory: $WorkDir"
Write-Host "Build directory: $BuildDir"
Write-Host "Bin directory: $BinDir"

Set-Location $BuildDir

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

# =============================================================================
# Step 1: Install Go 1.24.2
# =============================================================================
Write-Host ""
Write-Host "[1/7] Installing Go 1.24.2..."

$GoVersion = "1.24.2"
$GoZip = "go${GoVersion}.windows-amd64.zip"
$GoUrl = "https://go.dev/dl/$GoZip"
$GoInstallDir = "C:\Go"

Write-Host "Downloading Go from: $GoUrl"
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
(New-Object System.Net.WebClient).DownloadFile($GoUrl, (Join-Path $BuildDir $GoZip))

Write-Host "Extracting Go..."
if (Test-Path $GoInstallDir) {
    Remove-Item -Recurse -Force $GoInstallDir
}
Expand-Archive -Path (Join-Path $BuildDir $GoZip) -DestinationPath "C:\" -Force
Remove-Item (Join-Path $BuildDir $GoZip)

$env:PATH = "$GoInstallDir\bin;$env:PATH"
$env:GOPATH = "$env:USERPROFILE\go"
$env:PATH = "$env:GOPATH\bin;$env:PATH"

Write-Host "Go version:"
$goResult = cmd /c "go version 2>&1"
Write-Host $goResult

# =============================================================================
# Step 2: Install .NET SDK (for WiX v4)
# =============================================================================
Write-Host ""
Write-Host "[2/7] Installing .NET SDK..."

# Install .NET SDK using Chocolatey
# Temporarily allow errors since choco/dotnet output progress to stderr
$prevErrorAction = $ErrorActionPreference
$ErrorActionPreference = "Continue"
choco install dotnet-sdk -y --no-progress --limit-output 2>&1 | Out-Host
$chocoExitCode = $LASTEXITCODE
$ErrorActionPreference = $prevErrorAction

if ($chocoExitCode -ne 0) {
    throw ".NET SDK installation failed with exit code: $chocoExitCode"
}

# Refresh PATH
$env:PATH = [System.Environment]::GetEnvironmentVariable("PATH", "Machine") + ";" +
            [System.Environment]::GetEnvironmentVariable("PATH", "User") + ";" +
            "$GoInstallDir\bin;$env:GOPATH\bin"

Write-Host ".NET version:"
$dotnetResult = cmd /c "dotnet --version 2>&1"
Write-Host $dotnetResult

# =============================================================================
# Step 3: Install WiX Toolset v4
# =============================================================================
Write-Host ""
Write-Host "[4/7] Installing WiX Toolset v4..."

# Temporarily allow errors since dotnet outputs info to stderr
$prevErrorAction = $ErrorActionPreference
$ErrorActionPreference = "Continue"
dotnet tool install --global wix 2>&1 | Out-Host
$wixInstallExitCode = $LASTEXITCODE
$ErrorActionPreference = $prevErrorAction

# Exit code 0 = success, exit code 1 may just mean "already installed" - check if wix is available
# Refresh PATH to include dotnet tools before checking
$env:PATH = "$env:USERPROFILE\.dotnet\tools;$env:PATH"

# Verify wix is available
$wixPath = Get-Command wix -ErrorAction SilentlyContinue
if (-not $wixPath) {
    throw "WiX Toolset installation failed - wix command not found"
}

# Install WiX UI extension (use cmd /c to avoid transcript console buffer conflicts)
Write-Host "Installing WiX UI extension..."
$wixExtResult = cmd /c "wix extension add WixToolset.UI.wixext -g 2>&1"
Write-Host $wixExtResult
# Note: wix extension add may return non-zero if already installed - that's OK

# v4.0.8: Install WiX Util extension for WixShellExec (LaunchTray custom action)
Write-Host "Installing WiX Util extension..."
$wixUtilExtResult = cmd /c "wix extension add WixToolset.Util.wixext -g 2>&1"
Write-Host $wixUtilExtResult

Write-Host "WiX version:"
# Use cmd /c wrapper to avoid PowerShell transcript console buffer conflicts
$wixVersionResult = cmd /c "wix --version 2>&1"
Write-Host $wixVersionResult

# =============================================================================
# Step 3.5: Install Node.js and Wails CLI (required for GUI build)
# =============================================================================
Write-Host ""
Write-Host "[4.5/7] Installing Node.js and Wails CLI..."

# Install Node.js via Chocolatey
$prevErrorAction = $ErrorActionPreference
$ErrorActionPreference = "Continue"
choco install nodejs-lts -y --no-progress --limit-output 2>&1 | Out-Host
$nodeExitCode = $LASTEXITCODE
$ErrorActionPreference = $prevErrorAction

if ($nodeExitCode -ne 0) {
    throw "Node.js installation failed with exit code: $nodeExitCode"
}

# Refresh PATH
$env:PATH = [System.Environment]::GetEnvironmentVariable("PATH", "Machine") + ";" +
            [System.Environment]::GetEnvironmentVariable("PATH", "User") + ";" +
            "$GoInstallDir\bin;$env:GOPATH\bin;$env:USERPROFILE\.dotnet\tools"

Write-Host "Node.js version:"
$nodeResult = cmd /c "node --version 2>&1"
Write-Host $nodeResult

# Install Wails CLI
Write-Host "Installing Wails CLI..."
$wailsInstallCmd = "go install github.com/wailsapp/wails/v2/cmd/wails@latest"
$prevErrorAction = $ErrorActionPreference
$ErrorActionPreference = "Continue"
cmd /c $wailsInstallCmd 2>&1 | Out-Host
$wailsInstallExitCode = $LASTEXITCODE
$ErrorActionPreference = $prevErrorAction

if ($wailsInstallExitCode -ne 0) {
    throw "Wails CLI installation failed with exit code: $wailsInstallExitCode"
}

Write-Host "Wails version:"
$wailsResult = cmd /c "$env:GOPATH\bin\wails.exe version 2>&1"
Write-Host $wailsResult

# =============================================================================
# Step 4: Build Wails Application
# =============================================================================
Write-Host ""
Write-Host "[5/7] Building Wails application with FIPS 140-3..."

Set-Location $env:REPO_NAME

$BuildTime = Get-Date -Format "yyyy-MM-dd"
$LdFlags = "-s -w -X main.Version=$($ReleaseTag) -X main.BuildTime=$BuildTime"

Write-Host "Build flags: GOFIPS140=latest"
Write-Host "LDFLAGS: $LdFlags"

# Install frontend dependencies
Write-Host "Installing frontend dependencies..."
Set-Location (Join-Path $WorkDir "frontend")
$prevErrorAction = $ErrorActionPreference
$ErrorActionPreference = "Continue"
cmd /c "npm install 2>&1" | Out-Host
$npmExitCode = $LASTEXITCODE
$ErrorActionPreference = $prevErrorAction
Set-Location ..

if ($npmExitCode -ne 0) {
    Write-Host "Warning: npm install returned exit code $npmExitCode (may be non-fatal warnings)"
}

# Build GUI binary using Wails (required for embedded frontend assets)
# NOTE: Must use wails build, not go build, because the app embeds frontend assets
Write-Host "Building rescale-int-gui.exe with Wails..."
$WailsExe = "$env:GOPATH\bin\wails.exe"
$wailsBuildCmd = "set `"GOFIPS140=latest`"&& `"$WailsExe`" build -platform windows/amd64 -ldflags `"$LdFlags`""
Write-Host "Running: $wailsBuildCmd"
$prevErrorAction = $ErrorActionPreference
$ErrorActionPreference = "Continue"
cmd /c $wailsBuildCmd 2>&1 | Out-Host
$wailsBuildExitCode = $LASTEXITCODE
$ErrorActionPreference = $prevErrorAction

# Wails now outputs rescale-int-gui.exe directly (configured in wails.json)
$WailsOutputExe = "build\bin\rescale-int-gui.exe"
if ($wailsBuildExitCode -ne 0 -or -not (Test-Path $WailsOutputExe)) {
    throw "Wails build failed (exit code: $wailsBuildExitCode)"
}

Copy-Item $WailsOutputExe -Destination "$BinDir\rescale-int-gui.exe"
Write-Host "GUI binary built: rescale-int-gui.exe"

# v4.0.2: Build standalone CLI binary
Write-Host "Building rescale-int.exe (standalone CLI)..."
$GoExe = "C:\Go\bin\go.exe"
$cliBuildCmd = "set `"GOFIPS140=latest`"&& `"$GoExe`" build -ldflags `"$LdFlags`" -o `"$BinDir\rescale-int.exe`" .\cmd\rescale-int"
$prevErrorAction = $ErrorActionPreference
$ErrorActionPreference = "Continue"
cmd /c $cliBuildCmd 2>&1 | Out-Host
$cliExitCode = $LASTEXITCODE
$ErrorActionPreference = $prevErrorAction
if ($cliExitCode -ne 0 -or -not (Test-Path "$BinDir\rescale-int.exe")) { throw "CLI build failed (exit code: $cliExitCode)" }
Write-Host "CLI binary built: rescale-int.exe"

# Build tray companion (windowsgui subsystem) - this is a separate simple Go app
Write-Host "Building rescale-int-tray.exe..."
$trayCmd = "set `"GOFIPS140=latest`"&& set `"GOOS=windows`"&& set `"GOARCH=amd64`"&& go build -ldflags `"$LdFlags -H=windowsgui`" -o `"$BinDir\rescale-int-tray.exe`" .\cmd\rescale-int-tray"
$prevErrorAction = $ErrorActionPreference
$ErrorActionPreference = "Continue"
cmd /c $trayCmd 2>&1 | Out-Host
$trayExitCode = $LASTEXITCODE
$ErrorActionPreference = $prevErrorAction
if ($trayExitCode -ne 0 -or -not (Test-Path "$BinDir\rescale-int-tray.exe")) { throw "Failed to build rescale-int-tray.exe (exit code: $trayExitCode)" }

Write-Host "Binaries built successfully"
Get-ChildItem $BinDir

# =============================================================================
# Step 4.5: Download and Bundle WebView2 Fixed Version Runtime
# =============================================================================
Write-Host ""
Write-Host "[5.5/7] Bundling WebView2 Fixed Version Runtime..."

$WebView2Dir = Join-Path $BinDir "webview2"
New-Item -ItemType Directory -Force -Path $WebView2Dir | Out-Null

# Download WebView2 Fixed Version Runtime from NuGet
# IMPORTANT: Use WebView2.Runtime.X64 package (contains actual runtime files)
# NOT Microsoft.Web.WebView2 (which is just the SDK with WebView2Loader.dll)
# See: https://github.com/ProKn1fe/WebView2.Runtime
$RuntimeNuGetUrl = "https://www.nuget.org/api/v2/package/WebView2.Runtime.X64"
$RuntimePkg = Join-Path $BuildDir "webview2-runtime.zip"

Write-Host "Downloading WebView2 Fixed Version Runtime (WebView2.Runtime.X64)..."
$hasWebView2 = $false

try {
    (New-Object System.Net.WebClient).DownloadFile($RuntimeNuGetUrl, $RuntimePkg)
    Write-Host "WebView2.Runtime.X64 package downloaded successfully"
    $pkgSize = (Get-Item $RuntimePkg).Length / 1MB
    Write-Host "Package size: $([math]::Round($pkgSize, 1)) MB"

    # Extract the NuGet package
    $RuntimeExtract = Join-Path $BuildDir "webview2-runtime-extract"
    Expand-Archive -Path $RuntimePkg -DestinationPath $RuntimeExtract -Force

    Write-Host "Searching for runtime files..."

    # Find msedgewebview2.exe in the extracted package
    $runtimeExe = Get-ChildItem -Path $RuntimeExtract -Recurse -Filter "msedgewebview2.exe" | Select-Object -First 1

    if ($runtimeExe) {
        $RuntimeSourceDir = $runtimeExe.DirectoryName
        Write-Host "Found runtime at: $RuntimeSourceDir"

        # Copy all runtime files
        Copy-Item -Path "$RuntimeSourceDir\*" -Destination $WebView2Dir -Recurse -Force
        $hasWebView2 = $true

        # v4.0.1: Strip unnecessary components to avoid path length issues and reduce size
        # - WidevineCdm: DRM for video playback - not needed for Interlink
        # - EBWebView/x86: 32-bit components - Interlink is 64-bit only
        Write-Host "Stripping unnecessary WebView2 components..."
        $strippedSize = 0

        $widevinePath = Join-Path $WebView2Dir "WidevineCdm"
        if (Test-Path $widevinePath) {
            $wvSize = (Get-ChildItem -Path $widevinePath -Recurse | Measure-Object -Property Length -Sum).Sum / 1MB
            Remove-Item -Recurse $widevinePath -Force -ErrorAction SilentlyContinue
            Write-Host "  Removed WidevineCdm/ ($([math]::Round($wvSize, 1)) MB)"
            $strippedSize += $wvSize
        }

        $x86Path = Join-Path $WebView2Dir "EBWebView\x86"
        if (Test-Path $x86Path) {
            $x86Size = (Get-ChildItem -Path $x86Path -Recurse | Measure-Object -Property Length -Sum).Sum / 1MB
            Remove-Item -Recurse $x86Path -Force -ErrorAction SilentlyContinue
            Write-Host "  Removed EBWebView/x86/ ($([math]::Round($x86Size, 1)) MB)"
            $strippedSize += $x86Size
        }

        if ($strippedSize -gt 0) {
            Write-Host "  Total stripped: $([math]::Round($strippedSize, 1)) MB"
        }

        # Verify
        $copiedExe = Join-Path $WebView2Dir "msedgewebview2.exe"
        if (Test-Path $copiedExe) {
            Write-Host "SUCCESS: msedgewebview2.exe bundled for MSI"
            $fileCount = (Get-ChildItem -Path $WebView2Dir -Recurse).Count
            $totalSize = (Get-ChildItem -Path $WebView2Dir -Recurse | Measure-Object -Property Length -Sum).Sum / 1MB
            Write-Host "WebView2 runtime: $fileCount files, $([math]::Round($totalSize, 1)) MB total"
        } else {
            Write-Host "ERROR: Failed to copy msedgewebview2.exe"
            $hasWebView2 = $false
        }
    } else {
        Write-Host "ERROR: msedgewebview2.exe not found in WebView2.Runtime.X64 package"
        Get-ChildItem -Path $RuntimeExtract -Recurse | Where-Object { $_.Name -like "*.exe" } | Select-Object FullName
        $hasWebView2 = $false
    }

    # Cleanup
    Remove-Item $RuntimePkg -Force -ErrorAction SilentlyContinue
    Remove-Item $RuntimeExtract -Recurse -Force -ErrorAction SilentlyContinue

} catch {
    Write-Host "ERROR: Could not download/extract WebView2 runtime: $_"
    Write-Host "MSI will require WebView2 Evergreen runtime on target system"
    $hasWebView2 = $false
}

if ($hasWebView2) {
    Write-Host ""
    Write-Host "WebView2 Fixed Version Runtime BUNDLED in MSI"
    Write-Host "MSI will work on Windows Server 2019 without any pre-installation"
} else {
    Write-Host ""
    Write-Host "WARNING: WebView2 runtime NOT bundled in MSI"
    Write-Host "MSI requires WebView2 Evergreen runtime (pre-installed on Win10+)"
}

# =============================================================================
# Step 5: Create Support Files
# =============================================================================
Write-Host ""
Write-Host "[6/7] Creating support files..."

$ReadmeContent = @"
Rescale Interlink $($ReleaseTag)
============================

Unified CLI and GUI for Rescale HPC platform.

Installation Directory: C:\Program Files\Rescale\Interlink\

Components:
- rescale-int-gui.exe  : GUI application (double-click to run)
- rescale-int.exe      : CLI tool (run from command prompt)
- rescale-int-tray.exe : System tray companion

Usage:
GUI Mode:
  Double-click rescale-int-gui.exe, or run from Start Menu

CLI Mode:
  rescale-int --help
  rescale-int jobs list
  rescale-int upload file.txt

Documentation:
  https://docs.rescale.com/

Support:
  support@rescale.com

Copyright (c) 2026 Rescale, Inc.
"@

$ReadmeContent | Out-File -FilePath "$BinDir\README.txt" -Encoding UTF8

$LicenseContent = @"
MIT License

Copyright (c) 2025 Rescale, Inc.

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
"@

$LicenseContent | Out-File -FilePath "$BinDir\LICENSE.txt" -Encoding UTF8

Write-Host "Support files created"
