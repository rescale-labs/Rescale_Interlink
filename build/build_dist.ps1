param (
    [string]$ReleaseTag = $env:RELEASE_TAG
)

# Fail fast on any error, including the early setup steps below.
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

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

# Record the Node.js this script inherited, before the PATH refreshes below
# replace $env:PATH with the Machine and User registry values. Those refreshes
# drop process-only PATH entries, and in CI actions/setup-node provides the
# pinned Node through exactly such an entry (the hosted tool cache), so step 3.5
# has to learn about it here or it will only ever see the machine-wide Node.
$InheritedNodeDir = $null
$InheritedNodeVersion = ""
$inheritedNodeCmd = Get-Command node -CommandType Application -ErrorAction SilentlyContinue |
                    Select-Object -First 1
if ($inheritedNodeCmd) {
    $InheritedNodeDir = Split-Path $inheritedNodeCmd.Source
    $InheritedNodeVersion = ((cmd /c "node --version 2>&1") -join "`n").Trim()
    Write-Host "Inherited Node.js: $InheritedNodeVersion ($InheritedNodeDir)"
} else {
    Write-Host "Inherited Node.js: none on PATH"
}

Set-Location $BuildDir

# =============================================================================
# Step 1: Install Go 1.26.7
# =============================================================================
Write-Host ""
Write-Host "[1/7] Installing Go 1.26.7..."

$GoVersion = "1.26.7"
$GoZip = "go${GoVersion}.windows-amd64.zip"
$GoUrl = "https://go.dev/dl/$GoZip"
# Official sha256 from https://go.dev/dl/?mode=json&include=all
$GoZipSha256 = "f4f534a486e4bc3387fa18f08208f2f854b7aaea8a08f2a2d829a914a05abb11"
$GoInstallDir = "C:\Go"

Write-Host "Downloading Go from: $GoUrl"
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
(New-Object System.Net.WebClient).DownloadFile($GoUrl, (Join-Path $BuildDir $GoZip))

Write-Host "Verifying Go archive checksum..."
$GoZipActualSha256 = (Get-FileHash -Path (Join-Path $BuildDir $GoZip) -Algorithm SHA256).Hash
if ($GoZipActualSha256 -ne $GoZipSha256.ToUpper()) {
    throw "Go archive checksum mismatch for ${GoZip}: expected $GoZipSha256, got $GoZipActualSha256"
}
Write-Host "Checksum OK: $GoZipActualSha256"

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

# Refresh PATH. Our Go ($GoInstallDir\bin) MUST come first: the GitHub
# windows-latest runner ships a preinstalled Go in hostedtoolcache that lacks
# GOFIPS140=certified, and if it wins PATH precedence the FIPS build fails with
# "unknown GOFIPS140 version".
$env:PATH = "$GoInstallDir\bin;$env:GOPATH\bin;" +
            [System.Environment]::GetEnvironmentVariable("PATH", "Machine") + ";" +
            [System.Environment]::GetEnvironmentVariable("PATH", "User")

Write-Host ".NET version:"
$dotnetResult = cmd /c "dotnet --version 2>&1"
Write-Host $dotnetResult

# =============================================================================
# Step 3: Install WiX Toolset v4
# =============================================================================
Write-Host ""
Write-Host "[3/7] Installing WiX Toolset v4..."

# Temporarily allow errors since dotnet outputs info to stderr
$prevErrorAction = $ErrorActionPreference
$ErrorActionPreference = "Continue"
dotnet tool install --global wix --version 6.0.2 2>&1 | Out-Host
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
$wixExtResult = cmd /c "wix extension add WixToolset.UI.wixext/6.0.2 -g 2>&1"
Write-Host $wixExtResult
# Note: wix extension add may return non-zero if already installed - that's OK
# Pin to 6.0.x to match WiX v6 — unpinned resolves to v7.0.0-rc.1 which is incompatible

Write-Host "WiX version:"
# Use cmd /c wrapper to avoid PowerShell transcript console buffer conflicts
$wixVersionResult = cmd /c "wix --version 2>&1"
Write-Host $wixVersionResult

# =============================================================================
# Step 3.5: Install Node.js and Wails CLI (required for GUI build)
# =============================================================================
Write-Host ""
Write-Host "[3.5/7] Installing Node.js and Wails CLI..."

# Install Node.js via Chocolatey. The version is pinned because the unpinned
# nodejs-lts package tracks whatever the current LTS is, and the frontend must
# be built with the same Node the other release paths use.
$NodeVersion = "24.21.0"
$PinnedNodeDir = $null

if ($InheritedNodeVersion -eq "v$NodeVersion") {
    # Nothing to install. This is the CI path: actions/setup-node has already put
    # the pinned Node on PATH, and running Chocolatey anyway fails with msiexec
    # 1603 whenever the runner image ships a newer Node from the same MSI upgrade
    # family, because Windows Installer refuses to downgrade it.
    Write-Host "Node.js $NodeVersion already present at ${InheritedNodeDir} - skipping Chocolatey install."
    $PinnedNodeDir = $InheritedNodeDir
} else {
    Write-Host "Installing Node.js $NodeVersion via Chocolatey (inherited Node.js: '$InheritedNodeVersion')..."
    $prevErrorAction = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    choco install nodejs-lts --version=$NodeVersion -y --no-progress --limit-output 2>&1 | Out-Host
    $nodeExitCode = $LASTEXITCODE
    $ErrorActionPreference = $prevErrorAction

    if ($nodeExitCode -ne 0) {
        $nodeFailure = "Node.js $NodeVersion installation failed with exit code: $nodeExitCode. " +
                       "Inherited Node.js was '$InheritedNodeVersion'. Exit code 1603 comes from " +
                       "msiexec and usually means a different Node.js from the same MSI upgrade " +
                       "family is already installed, which Windows Installer will not downgrade. " +
                       "Uninstall that Node.js, or install $NodeVersion yourself so this script " +
                       "skips the install, then re-run."
        throw $nodeFailure
    }
}

# Refresh PATH. Keep our Go ($GoInstallDir\bin) ahead of the runner's
# preinstalled Go so the FIPS-certified toolchain is the one Wails invokes.
# $PinnedNodeDir is set only when the pinned Node was already on PATH, and it has
# to be re-added by hand: this refresh reads the registry, where a process-only
# directory such as the hosted tool cache never appears.
$PathPrefix = "$GoInstallDir\bin;$env:GOPATH\bin;$env:USERPROFILE\.dotnet\tools"
if ($PinnedNodeDir) {
    $PathPrefix = "$PathPrefix;$PinnedNodeDir"
}
$env:PATH = "$PathPrefix;" +
            [System.Environment]::GetEnvironmentVariable("PATH", "Machine") + ";" +
            [System.Environment]::GetEnvironmentVariable("PATH", "User")

Write-Host "Node.js version:"
$nodeResult = cmd /c "node --version 2>&1"
Write-Host $nodeResult

# Pinning the package is not enough on its own: the runner ships a preinstalled
# Node, and whichever copy wins the PATH refresh above is the one that builds the
# frontend. Fail loudly rather than shipping a build made with the wrong Node.
if (($nodeResult -join "`n").Trim() -ne "v$NodeVersion") {
    throw "Expected Node.js $NodeVersion on PATH, got: $nodeResult"
}

# Install Wails CLI
Write-Host "Installing Wails CLI..."
$wailsInstallCmd = "go install github.com/wailsapp/wails/v2/cmd/wails@v2.12.0"
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
Write-Host "[4/7] Building Wails application with FIPS 140-3..."

# On GitHub Actions, the repo is already checked out to the workspace root.
# On Rescale HPC, the script clones into $env:REPO_NAME and must cd into it.
if ($env:REPO_NAME -and (Test-Path $env:REPO_NAME)) {
    Set-Location $env:REPO_NAME
}

$BuildTime = Get-Date -Format "yyyy-MM-dd"
$LdFlags = "-s -w -X github.com/rescale/rescale-int/internal/version.Version=$($ReleaseTag) -X github.com/rescale/rescale-int/internal/version.BuildTime=$BuildTime"

Write-Host "Build flags: GOFIPS140=certified"
Write-Host "LDFLAGS: $LdFlags"

# Install frontend dependencies from the lockfile. `npm ci` is required (not
# `npm install`) so release builds resolve to exactly the audited dependency
# versions in package-lock.json.
Write-Host "Installing frontend dependencies..."
Set-Location (Join-Path $WorkDir "frontend")
# Temporarily allow errors since npm writes progress to stderr.
$prevErrorAction = $ErrorActionPreference
$ErrorActionPreference = "Continue"
cmd /c "npm ci 2>&1" | Out-Host
$npmExitCode = $LASTEXITCODE
$ErrorActionPreference = $prevErrorAction
Set-Location $WorkDir

if ($npmExitCode -ne 0) {
    throw "npm ci failed with exit code: $npmExitCode"
}

# Build the CLI and tray while the checkout is still clean. wails build
# regenerates frontend\wailsjs and writes build\windows\info.json, and a binary
# built after it is stamped vcs.modified=true.
Write-Host "Building rescale-int.exe (standalone CLI)..."
$GoExe = "C:\Go\bin\go.exe"
$cliBuildCmd = "set `"GOFIPS140=certified`"&& `"$GoExe`" build -trimpath -tags fips -ldflags `"$LdFlags`" -o `"$BinDir\rescale-int.exe`" .\cmd\rescale-int"
$prevErrorAction = $ErrorActionPreference
$ErrorActionPreference = "Continue"
cmd /c $cliBuildCmd 2>&1 | Out-Host
$cliExitCode = $LASTEXITCODE
$ErrorActionPreference = $prevErrorAction
if ($cliExitCode -ne 0 -or -not (Test-Path "$BinDir\rescale-int.exe")) { throw "CLI build failed (exit code: $cliExitCode)" }
Write-Host "CLI binary built: rescale-int.exe"

# Build tray companion (windowsgui subsystem) - this is a separate simple Go app
Write-Host "Building rescale-int-tray.exe..."
$trayCmd = "set `"GOFIPS140=certified`"&& set `"GOOS=windows`"&& set `"GOARCH=amd64`"&& `"$GoExe`" build -trimpath -tags fips -ldflags `"$LdFlags -H=windowsgui`" -o `"$BinDir\rescale-int-tray.exe`" .\cmd\rescale-int-tray"
$prevErrorAction = $ErrorActionPreference
$ErrorActionPreference = "Continue"
cmd /c $trayCmd 2>&1 | Out-Host
$trayExitCode = $LASTEXITCODE
$ErrorActionPreference = $prevErrorAction
if ($trayExitCode -ne 0 -or -not (Test-Path "$BinDir\rescale-int-tray.exe")) { throw "Failed to build rescale-int-tray.exe (exit code: $trayExitCode)" }

# Build GUI binary using Wails (required for embedded frontend assets)
# NOTE: Must use wails build, not go build, because the app embeds frontend assets
Write-Host "Building rescale-int-gui.exe with Wails..."
$WailsExe = "$env:GOPATH\bin\wails.exe"
# Wails shells out to `go`; force our FIPS-certified toolchain by putting it
# first on PATH and pinning GOROOT, so the runner's preinstalled Go can't win.
$wailsBuildCmd = "set `"GOFIPS140=certified`"&& set `"GOROOT=$GoInstallDir`"&& set `"PATH=$GoInstallDir\bin;%PATH%`"&& `"$WailsExe`" build -trimpath -tags fips -platform windows/amd64 -ldflags `"$LdFlags`""
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

Write-Host "Binaries built successfully"
Get-ChildItem $BinDir

# =============================================================================
# Step 4.5: Download and Bundle WebView2 Fixed Version Runtime
# =============================================================================
Write-Host ""
Write-Host "[4.5/7] Bundling WebView2 Fixed Version Runtime..."

# Pinned WebView2.Runtime.X64 package. Change both lines together: the SHA-256
# is of the .nupkg that api.nuget.org serves for this version.
$WebView2Version = "152.0.4191.62"
$WebView2Sha256 = "f6db2fa2038d7e7398cf33ca3113c86187b76cb6906c6a3abdb4aee8a042b376"

$WebView2Dir = Join-Path $BinDir "webview2"
$RuntimeExtract = Join-Path $BuildDir "webview2-runtime-extract"
# build_installer.ps1 requires this marker, which is written last. Clearing it and
# any earlier extraction or copy first keeps stale or partial runtimes out of the MSI.
$WebView2Marker = Join-Path $BuildDir "webview2-bundled.txt"
Get-Item $WebView2Marker, $RuntimeExtract, $WebView2Dir -Force -ErrorAction Ignore | Remove-Item -Recurse -Force
New-Item -ItemType Directory -Force -Path $WebView2Dir | Out-Null

# Download WebView2 Fixed Version Runtime from NuGet
# IMPORTANT: Use WebView2.Runtime.X64 package (contains actual runtime files)
# NOT Microsoft.Web.WebView2 (which is just the SDK with WebView2Loader.dll)
# See: https://github.com/ProKn1fe/WebView2.Runtime
$RuntimeNuGetUrl = "https://api.nuget.org/v3-flatcontainer/webview2.runtime.x64/$WebView2Version/webview2.runtime.x64.$WebView2Version.nupkg"
$RuntimePkg = Join-Path $BuildDir "webview2-runtime.zip"

Write-Host "Downloading WebView2 Fixed Version Runtime (WebView2.Runtime.X64 $WebView2Version)..."

# Any failure below stops the build. Without the bundled runtime, the GUI offers to
# download an Evergreen runtime where none is installed, and cannot start without one.
try {
    (New-Object System.Net.WebClient).DownloadFile($RuntimeNuGetUrl, $RuntimePkg)
    Write-Host "WebView2.Runtime.X64 package downloaded successfully"
    $pkgSize = (Get-Item $RuntimePkg).Length / 1MB
    Write-Host "Package size: $([math]::Round($pkgSize, 1)) MB"

    $RuntimePkgSha256 = (Get-FileHash -Path $RuntimePkg -Algorithm SHA256).Hash
    if ($RuntimePkgSha256 -ne $WebView2Sha256.ToUpper()) {
        throw "checksum mismatch for ${RuntimeNuGetUrl}: expected $WebView2Sha256, got $RuntimePkgSha256"
    }
    Write-Host "Checksum OK: $RuntimePkgSha256"

    # Extract the NuGet package
    Expand-Archive -Path $RuntimePkg -DestinationPath $RuntimeExtract -Force

    Write-Host "Searching for runtime files..."

    # Find msedgewebview2.exe in the extracted package
    $runtimeExe = Get-ChildItem -Path $RuntimeExtract -Recurse -Filter "msedgewebview2.exe" | Select-Object -First 1

    if ($runtimeExe) {
        $RuntimeSourceDir = $runtimeExe.DirectoryName
        Write-Host "Found runtime at: $RuntimeSourceDir"

        # Copy all runtime files
        Copy-Item -Path "$RuntimeSourceDir\*" -Destination $WebView2Dir -Recurse -Force

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
            Write-Host "SUCCESS: WebView2 $WebView2Version bundled for MSI (msedgewebview2.exe $((Get-Item $copiedExe).VersionInfo.FileVersion))"
            $fileCount = (Get-ChildItem -Path $WebView2Dir -Recurse).Count
            $totalSize = (Get-ChildItem -Path $WebView2Dir -Recurse | Measure-Object -Property Length -Sum).Sum / 1MB
            Write-Host "WebView2 runtime: $fileCount files, $([math]::Round($totalSize, 1)) MB total"
        } else {
            throw "Failed to copy msedgewebview2.exe"
        }
    } else {
        Get-ChildItem -Path $RuntimeExtract -Recurse | Where-Object { $_.Name -like "*.exe" } | Select-Object FullName | Out-Host
        throw "msedgewebview2.exe not found in WebView2.Runtime.X64 package"
    }

    Set-Content -Path $WebView2Marker -Value $WebView2Version

    # Cleanup
    Remove-Item $RuntimePkg -Force -ErrorAction SilentlyContinue
    Remove-Item $RuntimeExtract -Recurse -Force -ErrorAction SilentlyContinue

} catch {
    throw "Could not bundle WebView2 runtime ${WebView2Version}: $_"
}

# =============================================================================
# Step 5: Create Support Files
# =============================================================================
Write-Host ""
Write-Host "[5/7] Creating support files..."

$ReadmeContent = @"
Rescale Interlink $($ReleaseTag)
============================

Unified CLI and GUI for Rescale HPC platform.

Installation Directory: %LOCALAPPDATA%\Rescale\Interlink\

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

Documentation: https://docs.rescale.com
Source/issues: https://github.com/rescale-labs/Rescale_Interlink
Support:       support@rescale.com

Copyright (c) 2026 Rescale, Inc.
"@

$ReadmeContent | Out-File -FilePath "$BinDir\README.txt" -Encoding UTF8

$LicenseContent = @"
MIT License

Copyright (c) 2026 Rescale, Inc.

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
