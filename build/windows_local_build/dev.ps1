# dev.ps1 --- run Rescale Interlink locally with hot reload using the portable
# toolchain.
#
#   powershell -ExecutionPolicy Bypass -File build\windows_local_build\dev.ps1
#   powershell -ExecutionPolicy Bypass -File build\windows_local_build\dev.ps1 -WailsArgs '-debug'
#
# Starts `wails dev`: Vite serves the React frontend with hot-module reload and
# Wails rebuilds/restarts the Go backend on change. It builds a transient dev
# binary of the GUI, not a release artifact. The CLI and tray are not part of
# `wails dev`; build them with dist.ps1 if you need them.
#
# FIPS: the app enforces FIPS 140-3 at startup. Rather than building the dev
# binary with the FIPS toolchain (slower iteration), we set
# RESCALE_ALLOW_NON_FIPS=true so the dev build runs without it. This bypass is
# DEVELOPMENT ONLY - released builds are always FIPS-certified (see dist.ps1).
#
# Run install-deps.ps1 first to provision the toolchain.

[CmdletBinding()]
param(
    # Extra arguments passed through to `wails dev` (e.g. -WailsArgs '-debug').
    [string[]]$WailsArgs = @()
)

. "$PSScriptRoot\_env.ps1"

function Write-Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }
function Write-Ok($msg)   { Write-Host "    $msg"  -ForegroundColor Green }

# Sanity: toolchain present?
if (-not (Test-Path (Join-Path $Script:GoBin 'go.exe'))) {
    throw "Go not found in toolchain. Run: powershell -ExecutionPolicy Bypass -File build\windows_local_build\install-deps.ps1"
}

Use-InterlinkToolchain

$wails    = Join-Path $Script:GoPathBin 'wails.exe'
$frontend = Join-Path $Script:RepoRoot 'frontend'

Push-Location $Script:RepoRoot
try {
    # --- Frontend deps -------------------------------------------------------
    # `wails dev` runs the frontend:dev:watcher (npm run dev) from wails.json,
    # which needs node_modules. Install if missing.
    if (-not (Test-Path (Join-Path $frontend 'node_modules'))) {
        $npm = Join-Path $Script:NodeDir 'npm.cmd'
        Write-Step "Installing frontend deps (npm ci)"
        Push-Location $frontend
        try { & $npm ci; if ($LASTEXITCODE -ne 0) { throw "npm ci failed ($LASTEXITCODE)" } }
        finally { Pop-Location }
    } else {
        Write-Ok "Frontend deps present (node_modules)."
    }

    # --- Free the Wails/Vite dev port ----------------------------------------
    # A previously-killed `wails dev` can leave a vite process holding port
    # 34115, which makes the next run fail with "Port 34115 is already in use".
    $devPort = 34115
    $pids = (Get-NetTCPConnection -LocalPort $devPort -State Listen -ErrorAction SilentlyContinue |
             Select-Object -ExpandProperty OwningProcess -Unique)
    if ($pids) {
        Write-Step "Freeing dev port $devPort (stale process $($pids -join ', '))"
        foreach ($procId in $pids) { Stop-Process -Id $procId -Force -ErrorAction SilentlyContinue }
    }

    # --- Wails dev (hot reload) ----------------------------------------------
    # DEVELOPMENT ONLY: bypass the startup FIPS gate so the non-FIPS dev binary
    # runs. Released builds are always FIPS-certified.
    $env:RESCALE_ALLOW_NON_FIPS = 'true'
    Write-Step "Starting wails dev (hot reload, non-FIPS dev bypass). Ctrl+C to stop."
    & $wails dev @WailsArgs
    if ($LASTEXITCODE -ne 0) { throw "wails dev exited ($LASTEXITCODE)" }
}
finally {
    Pop-Location
}
