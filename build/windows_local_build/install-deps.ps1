# install-deps.ps1 --- install the portable Windows build toolchain (no admin).
#
#   powershell -ExecutionPolicy Bypass -File build\windows_local_build\install-deps.ps1
#
# Downloads portable (zip) distributions of Go, Node.js, and the Wails CLI
# into build\windows_local_build\.toolchain\ and sets process-local paths.
# Nothing is installed system-wide, no registry/PATH changes persist, no
# administrator rights are needed. Re-running is idempotent: present tools are
# skipped (use -Force to re-download).

[CmdletBinding()]
param(
    [switch]$Force
)

. "$PSScriptRoot\_env.ps1"

function Write-Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }
function Write-Ok($msg)   { Write-Host "    $msg"  -ForegroundColor Green }
function Write-Warn2($m)  { Write-Host "    $m"    -ForegroundColor Yellow }

# TLS 1.2 for older PowerShell on Windows.
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

# Detect arch (Go/Node zip names differ for arm64).
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$nodeArch = if ($arch -eq 'arm64') { 'arm64' } else { 'x64' }

New-Item -ItemType Directory -Force -Path $Script:ToolchainDir | Out-Null

# Download to a temp file then extract into a destination dir, flattening the
# single top-level folder the archive usually contains.
function Install-FromZip {
    param(
        [string]$Name,
        [string]$Url,
        [string]$DestDir,
        [string]$TopLevelFolder  # the folder inside the zip to flatten away ('' = none)
    )
    if ((Test-Path $DestDir) -and -not $Force) {
        Write-Ok "$Name already present ($DestDir) --- skipping. (-Force to re-download)"
        return
    }
    if (Test-Path $DestDir) { Remove-Item -Recurse -Force $DestDir }

    $tmp = Join-Path $env:TEMP ("interlink-" + [IO.Path]::GetRandomFileName() + ".zip")
    Write-Step "Downloading $Name"
    Write-Ok   $Url
    Invoke-WebRequest -Uri $Url -OutFile $tmp -UseBasicParsing

    Write-Step "Extracting $Name"
    $stage = Join-Path $env:TEMP ("interlink-stage-" + [IO.Path]::GetRandomFileName())
    Expand-Archive -Path $tmp -DestinationPath $stage -Force

    if ($TopLevelFolder) {
        # Archive contains one wrapper folder (e.g. go\, node-vXX-win-x64\).
        $inner = Join-Path $stage $TopLevelFolder
        if (-not (Test-Path $inner)) {
            # Fall back to the single child dir if the name wasn't exact.
            $inner = (Get-ChildItem $stage -Directory | Select-Object -First 1).FullName
        }
        Move-Item $inner $DestDir
    } else {
        New-Item -ItemType Directory -Force -Path $DestDir | Out-Null
        Move-Item (Join-Path $stage '*') $DestDir
    }

    Remove-Item -Force $tmp
    Remove-Item -Recurse -Force $stage -ErrorAction SilentlyContinue
    Write-Ok "$Name -> $DestDir"
}

# --- Go ----------------------------------------------------------------------
$goZipUrl = "https://go.dev/dl/go$($Script:GoVersion).windows-$arch.zip"
Install-FromZip -Name "Go $($Script:GoVersion)" -Url $goZipUrl -DestDir $Script:GoRoot -TopLevelFolder 'go'

# --- Node.js -----------------------------------------------------------------
$nodeFolder = "node-v$($Script:NodeVersion)-win-$nodeArch"
$nodeZipUrl = "https://nodejs.org/dist/v$($Script:NodeVersion)/$nodeFolder.zip"
Install-FromZip -Name "Node $($Script:NodeVersion)" -Url $nodeZipUrl -DestDir $Script:NodeDir -TopLevelFolder $nodeFolder

# Activate paths now so `go install` and version checks use the portable tools.
Use-InterlinkToolchain

# --- Wails CLI ---------------------------------------------------------------
$wailsExe = Join-Path $Script:GoPathBin 'wails.exe'
if ((Test-Path $wailsExe) -and -not $Force) {
    Write-Ok "Wails CLI already present ($wailsExe) --- skipping."
} else {
    Write-Step "Installing Wails CLI $($Script:WailsVersion) (go install)"
    & (Join-Path $Script:GoBin 'go.exe') install "github.com/wailsapp/wails/v2/cmd/wails@$($Script:WailsVersion)"
    if ($LASTEXITCODE -ne 0) { throw "wails install failed (exit $LASTEXITCODE)" }
    Write-Ok "Wails CLI -> $wailsExe"
}

# --- .NET SDK (portable, no admin) -------------------------------------------
# WiX v4+ is distributed as a .NET global tool, so we need the .NET SDK. We
# download the pinned SDK zip and extract it with System32 tar.exe (bsdtar).
# NOTE: do NOT use Expand-Archive / .NET ZipFile here -- the SDK contains paths
# that exceed MAX_PATH (260 chars) under this deep toolchain dir, which those
# APIs cannot create. bsdtar handles long paths.
$dotnetExe = Join-Path $Script:DotnetDir 'dotnet.exe'
if ((Test-Path $dotnetExe) -and -not $Force) {
    Write-Ok ".NET SDK already present ($Script:DotnetDir) --- skipping. (-Force to re-download)"
} else {
    Write-Step "Installing .NET SDK $($Script:DotnetVersion)"
    if (Test-Path $Script:DotnetDir) { Remove-Item -Recurse -Force $Script:DotnetDir }
    $dotnetArch = if ($arch -eq 'arm64') { 'arm64' } else { 'x64' }
    $dotnetZipUrl = "https://builds.dotnet.microsoft.com/dotnet/Sdk/$($Script:DotnetVersion)/dotnet-sdk-$($Script:DotnetVersion)-win-$dotnetArch.zip"
    $dotnetZip = Join-Path $env:TEMP ("dotnet-sdk-" + [IO.Path]::GetRandomFileName() + ".zip")
    Write-Ok $dotnetZipUrl
    Invoke-WebRequest -Uri $dotnetZipUrl -OutFile $dotnetZip -UseBasicParsing

    New-Item -ItemType Directory -Force -Path $Script:DotnetDir | Out-Null
    # System32 tar (bsdtar) extracts zip and tolerates long paths. Use the
    # absolute path so we don't pick up a Git-bundled GNU tar that may differ.
    $sysTar = Join-Path $env:WINDIR 'System32\tar.exe'
    & $sysTar -xf $dotnetZip -C $Script:DotnetDir
    if ($LASTEXITCODE -ne 0) { throw "tar extraction of .NET SDK failed (exit $LASTEXITCODE)" }
    Remove-Item -Force $dotnetZip -ErrorAction SilentlyContinue
    if (-not (Test-Path $dotnetExe)) { throw ".NET SDK extracted but dotnet.exe is missing." }
    Write-Ok ".NET SDK -> $Script:DotnetDir"
}

# Activate .NET on PATH (and refresh Go/Node/Wails ordering) now that it exists.
Use-InterlinkToolchain

# --- WiX Toolset + extensions (portable, no admin) ---------------------------
# Installed as a .NET tool into the toolchain dir via --tool-path, so nothing
# lands in the user profile. Extensions are added to the global (-g) store,
# which dotnet keeps under the user profile by default; that's fine -- they're
# small and idempotent.
$wixExe = Join-Path $Script:DotnetTools 'wix.exe'
if ((Test-Path $wixExe) -and -not $Force) {
    Write-Ok "WiX already present ($wixExe) --- skipping. (-Force to re-download)"
} else {
    Write-Step "Installing WiX $($Script:WixVersion) (.NET tool)"
    if (Test-Path $wixExe) {
        & $dotnetExe tool uninstall wix --tool-path $Script:DotnetTools 2>&1 | Out-Null
    }
    & $dotnetExe tool install wix --version $Script:WixVersion --tool-path $Script:DotnetTools
    if ($LASTEXITCODE -ne 0) { throw "WiX install failed (exit $LASTEXITCODE)" }
    Write-Ok "WiX -> $wixExe"
}

Write-Step "Ensuring WiX extensions (UI, Util)"
& $wixExe extension add WixToolset.UI.wixext/$($Script:WixVersion) -g 2>&1   | Out-Host
& $wixExe extension add WixToolset.Util.wixext/$($Script:WixVersion) -g 2>&1 | Out-Host

# --- Report ------------------------------------------------------------------
Write-Step "Toolchain ready. Versions:"
& (Join-Path $Script:GoBin   'go.exe')   version
& (Join-Path $Script:NodeDir 'node.exe') --version
& (Join-Path $Script:NodeDir 'npm.cmd')  --version
& $wailsExe version
& $dotnetExe --version
& $wixExe --version
Write-Host ""
Write-Ok "All dependencies live under: $($Script:ToolchainDir)"
Write-Ok "Next:"
Write-Ok "  dev.ps1        -- run the GUI with hot reload"
Write-Ok "  check.ps1      -- compile + test the Go code"
Write-Ok "  dist.ps1       -- build GUI + CLI + tray binaries"
Write-Ok "  installer.ps1  -- build the MSI (uses the bundled .NET SDK + WiX)"
