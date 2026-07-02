# installer.ps1 --- build the Rescale Interlink MSI locally using the portable
# toolchain for the binaries and WiX (dotnet tool) for the package.
#
#   powershell -ExecutionPolicy Bypass -File build\windows_local_build\installer.ps1
#   powershell -ExecutionPolicy Bypass -File build\windows_local_build\installer.ps1 -Version 4.9.9
#   powershell -ExecutionPolicy Bypass -File build\windows_local_build\installer.ps1 -SkipBuild
#
# Mirrors the release MSI job (build\build_installer.ps1): it compiles
# installer\rescale-interlink.wxs with the WiX UI + Util extensions, pointing
# the binary source at the dist\bin produced by dist.ps1.
#
# WiX (and the .NET SDK it needs) are provisioned by install-deps.ps1 into the
# portable toolchain, so this script just uses them. If they're missing, run
# install-deps.ps1 first.
#
# Output: build\windows_local_build\dist\RescaleInterlink-<Version>.msi (+ .sha256)

[CmdletBinding()]
param(
    # Version stamped into the binaries and MSI. Defaults to a -dev marker.
    [string]$Version = '4.9.8-dev-local',
    # Reuse an existing dist\bin instead of rebuilding the binaries.
    [switch]$SkipBuild
)

. "$PSScriptRoot\_env.ps1"

function Write-Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }
function Write-Ok($msg)   { Write-Host "    $msg"  -ForegroundColor Green }

Use-InterlinkToolchain

$distDir      = Join-Path $PSScriptRoot 'dist'
$binDir       = Join-Path $distDir 'bin'
$installerDir = Join-Path $Script:RepoRoot 'installer'
$wxsFile      = Join-Path $installerDir 'rescale-interlink.wxs'
$wixExe       = Join-Path $Script:DotnetTools 'wix.exe'

# --- 1. Ensure WiX is provisioned --------------------------------------------
if (-not (Test-Path $wixExe)) {
    throw "WiX not found at $wixExe. Run install-deps.ps1 first to provision the .NET SDK + WiX."
}

# --- 2. Build the binaries (unless reusing) ----------------------------------
if ($SkipBuild) {
    if (-not (Test-Path (Join-Path $binDir 'rescale-int-gui.exe'))) {
        throw "-SkipBuild set but $binDir\rescale-int-gui.exe is missing. Run dist.ps1 first."
    }
    Write-Ok "Skipping build (using existing $binDir)."
} else {
    Write-Step "Building distribution (dist.ps1, version=$Version)"
    & (Join-Path $PSScriptRoot 'dist.ps1') -Version $Version
    if ($LASTEXITCODE -ne 0) { throw "dist.ps1 failed ($LASTEXITCODE)" }
}

# --- 3. Ensure WiX extensions (idempotent) -----------------------------------
Write-Step "Ensuring WiX extensions (UI, Util)"
& $wixExe extension add WixToolset.UI.wixext/$($Script:WixVersion) -g 2>&1   | Out-Host
& $wixExe extension add WixToolset.Util.wixext/$($Script:WixVersion) -g 2>&1 | Out-Host
Write-Ok ("WiX: " + (& $wixExe --version))

# --- 4. Build the MSI --------------------------------------------------------
# MSI ProductVersion must be numeric major.minor.build (each in range), with no
# pre-release labels. Strip a leading 'v' and any '-dev'/'-suffix', keeping just
# the leading X.Y.Z so a dev tag like '4.9.8-dev-local' yields '4.9.8'.
$versionNum = $Version -replace '^v', ''
$msiVersion = if ($versionNum -match '^\d+\.\d+\.\d+') { $Matches[0] } else { '0.0.0' }
$msiPath    = Join-Path $distDir "RescaleInterlink-$versionNum.msi"

# WiX harvests bin\webview2 (bundled only in release dist). Create an empty
# placeholder so the local MSI build doesn't warn about a missing directory.
$webview2Dir = Join-Path $binDir 'webview2'
if (-not (Test-Path $webview2Dir)) {
    New-Item -ItemType Directory -Force -Path $webview2Dir | Out-Null
    Set-Content -Path (Join-Path $webview2Dir '.gitkeep') -Value '' -NoNewline
}

Write-Step "Building MSI -> $msiPath (ProductVersion $msiVersion)"
# -bindpath points WiX at installer\ so it resolves License.rtf and the icon.
& $wixExe build $wxsFile `
    -d BuildDir="$binDir" `
    -d SourceDir="$binDir" `
    -d Version="$msiVersion" `
    -ext WixToolset.UI.wixext `
    -ext WixToolset.Util.wixext `
    -bindpath "$installerDir" `
    -o "$msiPath"
if ($LASTEXITCODE -ne 0) { throw "wix build failed ($LASTEXITCODE)" }
if (-not (Test-Path $msiPath)) { throw "wix build reported success but $msiPath is missing." }

# --- 4. Checksum -------------------------------------------------------------
$hash = (Get-FileHash -Path $msiPath -Algorithm SHA256).Hash.ToLower()
$msiName = Split-Path $msiPath -Leaf
"$hash  $msiName" | Out-File -FilePath "$msiPath.sha256" -Encoding ASCII

Write-Step "MSI built"
Write-Ok $msiPath
Write-Ok ("Size:   {0:N2} MB" -f ((Get-Item $msiPath).Length / 1MB))
Write-Ok "SHA256: $hash"
