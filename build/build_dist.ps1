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
# Pin to 6.0.x to match WiX v6 — unpinned, it can resolve to v7, which is incompatible

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

$BuildTime = Get-Date -Format "yyyy-MM-dd"
$LdFlags = "-s -w -X github.com/rescale/rescale-int/internal/version.Version=$($ReleaseTag) -X github.com/rescale/rescale-int/internal/version.BuildTime=$BuildTime"

Write-Host "Build flags: GOFIPS140=certified"
Write-Host "LDFLAGS: $LdFlags"

# The builds below take paths relative to the checkout.
Set-Location $WorkDir

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
# wails.json's frontend:install is 'npm ci', so the frontend gets exactly what
# package-lock.json pins.
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

# Pinned Fixed Version runtime, the .cab Microsoft publishes for it on the
# WebView2 download page. Change the three lines together: the SHA-256 is of
# the .cab at this URL. The community NuGet repackage used before is not
# complete from 152 on: it moved msedge.dll into a second package and leaves
# out the shader compilers.
$WebView2Version = "154.0.4258.62"
$WebView2CabUrl = "https://msedge.sf.dl.delivery.mp.microsoft.com/filestreamingservice/files/b92cd7d9-6976-4f34-9708-47e80937c287/Microsoft.WebView2.FixedVersionRuntime.154.0.4258.62.x64.cab"
$WebView2Sha256 = "e8f55a4bde27c7f82512402b56a58539b5ec8928be4e500e077b6f66c9ef4668"

$WebView2Dir = Join-Path $BinDir "webview2"
$RuntimeExtract = Join-Path $BuildDir "webview2-runtime-extract"
# build_installer.ps1 requires this marker, which is written last. Clearing it and
# any earlier extraction or copy first keeps stale or partial runtimes out of the MSI.
$WebView2Marker = Join-Path $BuildDir "webview2-bundled.txt"
Get-Item $WebView2Marker, $RuntimeExtract, $WebView2Dir -Force -ErrorAction Ignore | Remove-Item -Recurse -Force
New-Item -ItemType Directory -Force -Path $WebView2Dir, $RuntimeExtract | Out-Null
$RuntimeCab = Join-Path $BuildDir "webview2-runtime.cab"

Write-Host "Downloading WebView2 Fixed Version Runtime $WebView2Version..."

# Any failure below stops the build. Without a complete bundled runtime the GUI
# cannot start where no system runtime is installed.
try {
    (New-Object System.Net.WebClient).DownloadFile($WebView2CabUrl, $RuntimeCab)
    Write-Host "Package size: $([math]::Round((Get-Item $RuntimeCab).Length / 1MB, 1)) MB"

    $RuntimeCabSha256 = (Get-FileHash -Path $RuntimeCab -Algorithm SHA256).Hash
    if ($RuntimeCabSha256 -ne $WebView2Sha256.ToUpper()) {
        throw "checksum mismatch for ${WebView2CabUrl}: expected $WebView2Sha256, got $RuntimeCabSha256"
    }
    Write-Host "Checksum OK: $RuntimeCabSha256"

    # Microsoft's documented way to unpack the Fixed Version .cab
    & expand.exe $RuntimeCab -F:* $RuntimeExtract | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "expand.exe exited $LASTEXITCODE" }

    $runtimeExe = Get-ChildItem -Path $RuntimeExtract -Recurse -Filter "msedgewebview2.exe" | Select-Object -First 1
    if (-not $runtimeExe) { throw "msedgewebview2.exe not found in the Fixed Version runtime" }
    Write-Host "Found runtime at: $($runtimeExe.DirectoryName)"
    Copy-Item -Path "$($runtimeExe.DirectoryName)\*" -Destination $WebView2Dir -Recurse -Force

    # Strip what Interlink does not use: DRM for video, the 32-bit host, Edge's
    # Copilot programs, and every UI language but US English (the browser falls
    # back to it).
    $strip = @(@("WidevineCdm", "EBWebView\x86", "copilotapp.exe", "mscopilot.exe", "Installer\copilot_setup.exe") |
        ForEach-Object { Join-Path $WebView2Dir $_ } | Where-Object { Test-Path $_ })
    $strip += @(Get-ChildItem -Path (Join-Path $WebView2Dir "Locales") -File | Where-Object { $_.Name -ne "en-US.pak" } |
        ForEach-Object { $_.FullName })
    $strippedSize = ($strip | ForEach-Object { Get-ChildItem -Path $_ -Recurse -File -Force } | Measure-Object -Property Length -Sum).Sum
    $strip | ForEach-Object { Remove-Item -Path $_ -Recurse -Force }
    Write-Host "Stripped $($strip.Count) unused entries ($([math]::Round($strippedSize / 1MB, 1)) MB)"

    # A runtime missing any of these does not start, and nothing but launching the
    # GUI on Windows would show it. A signature file without its binary means a
    # file was lost on the way.
    foreach ($required in @("msedgewebview2.exe", "msedge.dll", "EBWebView\x64\EmbeddedBrowserWebView.dll",
                            "resources.pak", "icudtl.dat", "Locales\en-US.pak")) {
        if (-not (Test-Path (Join-Path $WebView2Dir $required) -PathType Leaf)) { throw "the runtime lacks $required" }
    }
    $unpaired = @(Get-ChildItem -Path $WebView2Dir -Recurse -File -Filter "*.sig" |
        Where-Object { -not (Test-Path ($_.FullName -replace '\.sig$', '') -PathType Leaf) })
    if ($unpaired.Count -gt 0) { throw "signature files without their binary: $($unpaired.Name -join ', ')" }
    $engineSize = (Get-Item (Join-Path $WebView2Dir "msedge.dll")).Length
    if ($engineSize -lt 100MB) { throw "msedge.dll is $engineSize bytes, too small to be the browser engine" }
    $exeVersion = (Get-Item (Join-Path $WebView2Dir "msedgewebview2.exe")).VersionInfo.FileVersion
    if ($exeVersion -ne $WebView2Version) { throw "msedgewebview2.exe is version $exeVersion, expected $WebView2Version" }

    $files = Get-ChildItem -Path $WebView2Dir -Recurse -File
    Write-Host "SUCCESS: WebView2 $WebView2Version bundled: $($files.Count) files, $([math]::Round(($files | Measure-Object -Property Length -Sum).Sum / 1MB, 1)) MB"

    Set-Content -Path $WebView2Marker -Value $WebView2Version

    Remove-Item $RuntimeCab -Force -ErrorAction SilentlyContinue
    Remove-Item $RuntimeExtract -Recurse -Force -ErrorAction SilentlyContinue

} catch {
    throw "Could not bundle WebView2 runtime ${WebView2Version}: $_"
}

# =============================================================================
# Step 5: Create Support Files
# =============================================================================
Write-Host ""
Write-Host "[5/7] Creating support files..."

& (Join-Path $PSScriptRoot "write_support_files.ps1") -BinDir $BinDir -Version $ReleaseTag

Write-Host "Support files created"
