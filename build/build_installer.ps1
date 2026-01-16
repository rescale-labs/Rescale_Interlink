param (
    [string]$ReleaseTag = $env:RELEASE_TAG,
    [string]$MsiName = $env:MSI_NAME
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
# Step 6: Build MSI Installer
# =============================================================================
Write-Host ""
Write-Host "[7/7] Building MSI installer..."

$InstallerDir = Join-Path $WorkDir "installer"
$MsiPath = Join-Path $BuildDir $MsiName

Write-Host "Building MSI: $MsiPath"

# Build the wix command string and execute via cmd /c to avoid transcript console buffer conflicts
$VersionNum = $ReleaseTag -replace '^v', ''
$WxsFile = "$InstallerDir\rescale-interlink.wxs"
# v4.0.8: Add -bindpath to tell WiX where to find License.rtf
# v4.0.8: Add Util extension for WixShellExec (LaunchTray custom action)
$wixBuildCmd = "wix build `"$WxsFile`" -d BuildDir=`"$BinDir`" -d SourceDir=`"$BinDir`" -d Version=`"$VersionNum`" -ext WixToolset.UI.wixext -ext WixToolset.Util.wixext -bindpath `"$InstallerDir`" -o `"$MsiPath`""

Write-Host "Running: $wixBuildCmd"
$wixBuildResult = cmd /c "$wixBuildCmd 2>&1"
$wixBuildExitCode = $LASTEXITCODE
Write-Host $wixBuildResult

if ($wixBuildExitCode -ne 0) {
    throw "WiX build failed with exit code: $wixBuildExitCode"
}

if (-not (Test-Path $MsiPath)) { throw "Failed to build MSI - file not created" }

Write-Host "MSI built successfully"
Get-Item $MsiPath | Format-List Name, Length

# Generate checksum
$hash = (Get-FileHash -Path $MsiPath -Algorithm SHA256).Hash.ToLower()
$checksumFile = "$MsiName.sha256"
"$hash  $MsiName" | Out-File -FilePath (Join-Path $WorkDir $checksumFile) -Encoding ASCII
Write-Host "Checksum: $hash"