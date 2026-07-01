# dist.ps1 --- build the full Rescale Interlink distribution locally using the
# portable toolchain.
#
#   powershell -ExecutionPolicy Bypass -File build\windows_local_build\dist.ps1
#   powershell -ExecutionPolicy Bypass -File build\windows_local_build\dist.ps1 -Version 4.9.9
#   powershell -ExecutionPolicy Bypass -File build\windows_local_build\dist.ps1 -SkipNpmInstall
#
# Mirrors the `release (windows)` dist job (build\build_dist.ps1): FIPS 140-3
# is built in (GOFIPS140=certified, -tags fips) because the app enforces FIPS
# at startup and refuses to run otherwise. The only differences from release
# are: no code signing, and it uses the portable toolchain from _env.ps1
# instead of system Go/Node.
#
# Produces three binaries + support files into the bin dir consumed by
# installer.ps1:
#   build\windows_local_build\dist\bin\
#     rescale-int-gui.exe   (Wails GUI, embeds frontend)
#     rescale-int.exe       (standalone CLI)
#     rescale-int-tray.exe  (system tray companion, windowsgui subsystem)
#     README.txt, LICENSE.txt
#
# WebView2: this local bundle does NOT download the fixed-version WebView2
# runtime (the release dist does). Win10/11 ship the Evergreen runtime, so the
# GUI runs without it on a normal dev machine.
#
# Run install-deps.ps1 first to provision the toolchain.

[CmdletBinding()]
param(
    # Version stamped into the binaries. Defaults to a -dev marker.
    [string]$Version = '4.9.8-dev-local',
    # Skip `npm ci` if frontend/node_modules already exists.
    [switch]$SkipNpmInstall
)

. "$PSScriptRoot\_env.ps1"

function Write-Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }
function Write-Ok($msg)   { Write-Host "    $msg"  -ForegroundColor Green }

# Sanity: toolchain present?
if (-not (Test-Path (Join-Path $Script:GoBin 'go.exe'))) {
    throw "Go not found in toolchain. Run: powershell -ExecutionPolicy Bypass -File build\windows_local_build\install-deps.ps1"
}

Use-InterlinkToolchain

$go       = Join-Path $Script:GoBin     'go.exe'
$npm      = Join-Path $Script:NodeDir   'npm.cmd'
$wails    = Join-Path $Script:GoPathBin 'wails.exe'
$frontend = Join-Path $Script:RepoRoot  'frontend'
$distDir  = Join-Path $PSScriptRoot     'dist'
$binDir   = Join-Path $distDir          'bin'

$buildTime = Get-Date -Format 'yyyy-MM-dd'
$ldflags   = "-s -w -X github.com/rescale/rescale-int/internal/version.Version=$Version -X github.com/rescale/rescale-int/internal/version.BuildTime=$buildTime"

New-Item -ItemType Directory -Force -Path $binDir | Out-Null

Push-Location $Script:RepoRoot
try {
    # --- Frontend deps (release.yml: npm ci) ---------------------------------
    if ($SkipNpmInstall -and (Test-Path (Join-Path $frontend 'node_modules'))) {
        Write-Ok "Skipping npm ci (node_modules present)."
    } else {
        Write-Step "Installing frontend deps (npm ci)"
        # Package postinstall scripts (e.g. esbuild's `node install.js`) invoke
        # bare `node`, which fails with "'node' is not recognized" unless the
        # toolchain node dir is on PATH for npm's grandchild cmd.exe. Setting
        # $env:PATH from PowerShell does NOT reliably propagate that far, and
        # npm's old scripts-prepend-node-path setting was removed in npm 7+.
        # Use the same proven pattern as the wails build below: run npm inside a
        # single cmd /c that sets PATH first, so every child process (npm ->
        # cmd -> node install.js) inherits the toolchain node.
        Push-Location $frontend
        try {
            $npmCmd = "set `"PATH=$($Script:NodeDir);%PATH%`"&& `"$npm`" ci"
            cmd /c $npmCmd
            if ($LASTEXITCODE -ne 0) { throw "npm ci failed ($LASTEXITCODE)" }
        }
        finally { Pop-Location }
    }

    # --- Build GUI with Wails (embeds frontend assets) -----------------------
    # FIPS 140-3 is required: the app exits at startup (including during Wails
    # binding generation) unless built with GOFIPS140=certified -tags fips.
    # Wails shells out to `go` (bindings) AND `npm` (frontend compile), so PATH
    # must include BOTH the Go bin and the node dir; otherwise the frontend
    # compile step fails with `exec: "npm": executable file not found`.
    Write-Step "Building rescale-int-gui.exe (wails build, FIPS, version=$Version)"
    $wailsBuildCmd = "set `"GOFIPS140=certified`"&& set `"GOROOT=$($Script:GoRoot)`"&& set `"PATH=$($Script:GoBin);$($Script:NodeDir);%PATH%`"&& `"$wails`" build -tags fips -platform windows/amd64 -ldflags `"$ldflags`""
    cmd /c $wailsBuildCmd
    if ($LASTEXITCODE -ne 0) { throw "wails build failed ($LASTEXITCODE)" }

    $wailsOut = Join-Path $Script:RepoRoot 'build\bin\rescale-int-gui.exe'
    if (-not (Test-Path $wailsOut)) { throw "wails build reported success but $wailsOut is missing." }
    Copy-Item $wailsOut -Destination (Join-Path $binDir 'rescale-int-gui.exe') -Force
    Write-Ok "GUI binary built."

    # --- Build standalone CLI ------------------------------------------------
    Write-Step "Building rescale-int.exe (CLI, FIPS)"
    $env:GOOS = 'windows'; $env:GOARCH = 'amd64'; $env:GOFIPS140 = 'certified'
    & $go build -tags fips -ldflags $ldflags -o (Join-Path $binDir 'rescale-int.exe') .\cmd\rescale-int
    if ($LASTEXITCODE -ne 0) { throw "CLI build failed ($LASTEXITCODE)" }
    Write-Ok "CLI binary built."

    # --- Build tray companion (windowsgui subsystem) -------------------------
    Write-Step "Building rescale-int-tray.exe (tray, FIPS)"
    & $go build -tags fips -ldflags "$ldflags -H=windowsgui" -o (Join-Path $binDir 'rescale-int-tray.exe') .\cmd\rescale-int-tray
    if ($LASTEXITCODE -ne 0) { throw "tray build failed ($LASTEXITCODE)" }
    Write-Ok "Tray binary built."

    # --- Support files -------------------------------------------------------
    Write-Step "Writing support files"
    $readme = @"
Rescale Interlink $Version
============================

Unified CLI and GUI for Rescale HPC platform.

Installation Directory: %LOCALAPPDATA%\Rescale\Interlink\

Components:
- rescale-int-gui.exe  : GUI application (double-click to run)
- rescale-int.exe      : CLI tool (run from command prompt)
- rescale-int-tray.exe : System tray companion (auto-download)

Documentation: https://docs.rescale.com/
Support:       support@rescale.com

Copyright (c) 2026 Rescale, Inc.
"@
    $readme | Out-File -FilePath (Join-Path $binDir 'README.txt') -Encoding UTF8

    $license = @"
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
    $license | Out-File -FilePath (Join-Path $binDir 'LICENSE.txt') -Encoding UTF8

    Write-Step "Distribution ready"
    Write-Ok $binDir
    Get-ChildItem $binDir | Select-Object Name, Length | Format-Table -AutoSize
    Write-Ok "Next: powershell -ExecutionPolicy Bypass -File build\windows_local_build\installer.ps1 -Version $Version"
}
finally {
    Remove-Item Env:\GOOS -ErrorAction SilentlyContinue
    Remove-Item Env:\GOARCH -ErrorAction SilentlyContinue
    Remove-Item Env:\GOFIPS140 -ErrorAction SilentlyContinue
    Pop-Location
}
