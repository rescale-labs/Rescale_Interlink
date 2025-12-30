# Rescale Interlink Windows Installer Build Script
# Requires: WiX Toolset v4+, Go 1.24+
#
# Usage:
#   .\installer\build-installer.ps1
#   .\installer\build-installer.ps1 -Version "4.0.1"
#   .\installer\build-installer.ps1 -Sign -CertThumbprint "ABC123..."

param(
    [string]$Version = "4.0.0",
    [switch]$Sign,
    [string]$CertThumbprint = "",
    [string]$OutputDir = "bin\installer"
)

$ErrorActionPreference = "Stop"

Write-Host "========================================" -ForegroundColor Cyan
Write-Host "Rescale Interlink MSI Builder v$Version" -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan

# Paths
$ProjectRoot = Split-Path -Parent $PSScriptRoot
$BuildDir = Join-Path $ProjectRoot "bin\windows-amd64"
$InstallerDir = Join-Path $ProjectRoot "installer"
$OutputPath = Join-Path $ProjectRoot $OutputDir

# Ensure output directory exists
New-Item -ItemType Directory -Force -Path $OutputPath | Out-Null

# Step 1: Build Go binaries
Write-Host "`n[1/5] Building Go binaries..." -ForegroundColor Yellow

# Build main executable
Write-Host "  Building rescale-int.exe..."
$env:GOOS = "windows"
$env:GOARCH = "amd64"
$env:GOFIPS140 = "latest"
$env:CGO_ENABLED = "1"

Push-Location $ProjectRoot
try {
    # Build main CLI/GUI
    go build -ldflags "-s -w" -o "$BuildDir\rescale-int.exe" .\cmd\rescale-int
    if ($LASTEXITCODE -ne 0) { throw "Failed to build rescale-int.exe" }

    # Build tray companion
    Write-Host "  Building rescale-int-tray.exe..."
    go build -ldflags "-s -w -H=windowsgui" -o "$BuildDir\rescale-int-tray.exe" .\cmd\rescale-int-tray
    if ($LASTEXITCODE -ne 0) { throw "Failed to build rescale-int-tray.exe" }
}
finally {
    Pop-Location
}

Write-Host "  Binaries built successfully" -ForegroundColor Green

# Step 2: Create support files
Write-Host "`n[2/5] Creating support files..." -ForegroundColor Yellow

$ReadmeContent = @"
Rescale Interlink v$Version
============================

Unified CLI and GUI for Rescale HPC platform.

Installation Directory: C:\Program Files\Rescale\Interlink\

Components:
- rescale-int.exe      : Main application (CLI + GUI)
- rescale-int-tray.exe : System tray companion

Usage:
  rescale-int --gui            : Launch GUI
  rescale-int --help           : Show CLI help
  rescale-int service status   : Check service status

Documentation:
  https://docs.rescale.com/

Support:
  support@rescale.com

Copyright (c) 2025 Rescale, Inc.
"@

$ReadmeContent | Out-File -FilePath "$BuildDir\README.txt" -Encoding UTF8

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

$LicenseContent | Out-File -FilePath "$BuildDir\LICENSE.txt" -Encoding UTF8

Write-Host "  Support files created" -ForegroundColor Green

# Step 3: Check for WiX Toolset
Write-Host "`n[3/5] Checking WiX Toolset..." -ForegroundColor Yellow

$wixPath = Get-Command wix -ErrorAction SilentlyContinue
if (-not $wixPath) {
    Write-Host "  WiX Toolset not found. Installing..." -ForegroundColor Yellow
    dotnet tool install --global wix
    if ($LASTEXITCODE -ne 0) { throw "Failed to install WiX Toolset" }
}

# Install WiX UI extension if needed
wix extension add WixToolset.UI.wixext -g 2>$null

Write-Host "  WiX Toolset ready" -ForegroundColor Green

# Step 4: Build MSI
Write-Host "`n[4/5] Building MSI installer..." -ForegroundColor Yellow

$MsiPath = Join-Path $OutputPath "RescaleInterlink-$Version.msi"

wix build `
    "$InstallerDir\rescale-interlink.wxs" `
    -d BuildDir="$BuildDir" `
    -d SourceDir="$BuildDir" `
    -d Version="$Version" `
    -ext WixToolset.UI.wixext `
    -o $MsiPath

if ($LASTEXITCODE -ne 0) { throw "Failed to build MSI" }

Write-Host "  MSI built: $MsiPath" -ForegroundColor Green

# Step 5: Sign MSI (optional)
if ($Sign) {
    Write-Host "`n[5/5] Signing MSI..." -ForegroundColor Yellow

    if (-not $CertThumbprint) {
        Write-Host "  Warning: -Sign specified but no -CertThumbprint provided" -ForegroundColor Yellow
        Write-Host "  Skipping code signing" -ForegroundColor Yellow
    }
    else {
        signtool sign /sha1 $CertThumbprint /fd SHA256 /tr http://timestamp.digicert.com /td SHA256 $MsiPath
        if ($LASTEXITCODE -ne 0) { throw "Failed to sign MSI" }
        Write-Host "  MSI signed successfully" -ForegroundColor Green
    }
}
else {
    Write-Host "`n[5/5] Skipping code signing (use -Sign to enable)" -ForegroundColor Yellow
}

# Summary
Write-Host "`n========================================" -ForegroundColor Cyan
Write-Host "Build Complete!" -ForegroundColor Green
Write-Host "========================================" -ForegroundColor Cyan
Write-Host "Output: $MsiPath"
Write-Host "Size: $([math]::Round((Get-Item $MsiPath).Length / 1MB, 2)) MB"

# Generate SHA256 checksum
$hash = Get-FileHash -Path $MsiPath -Algorithm SHA256
Write-Host "SHA256: $($hash.Hash)"

# Save checksum
$hash.Hash | Out-File -FilePath "$OutputPath\RescaleInterlink-$Version.msi.sha256" -Encoding ASCII
Write-Host "Checksum saved to: $OutputPath\RescaleInterlink-$Version.msi.sha256"
