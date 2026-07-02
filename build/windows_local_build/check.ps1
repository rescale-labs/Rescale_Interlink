# check.ps1 --- compile + test the Go code using the portable toolchain.
#
#   powershell -ExecutionPolicy Bypass -File build\windows_local_build\check.ps1
#   powershell -ExecutionPolicy Bypass -File build\windows_local_build\check.ps1 -Test
#   powershell -ExecutionPolicy Bypass -File build\windows_local_build\check.ps1 -Goos linux
#
# Mirrors the portable toolchain layout from _env.ps1. Builds the whole module
# for the requested GOOS (default: windows), and optionally runs `go test`.
# Run install-deps.ps1 first to provision the toolchain.

[CmdletBinding()]
param(
    # Target GOOS for the build (windows | linux | darwin).
    [string]$Goos = 'windows',
    # Also run `go test ./...`.
    [switch]$Test,
    # Restrict build/test to these packages (default: ./...).
    [string]$Packages = './...'
)

. "$PSScriptRoot\_env.ps1"

function Write-Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }

if (-not (Test-Path (Join-Path $Script:GoBin 'go.exe'))) {
    throw "Go not found in toolchain. Run: powershell -ExecutionPolicy Bypass -File build\windows_local_build\install-deps.ps1"
}

Use-InterlinkToolchain
$go = Join-Path $Script:GoBin 'go.exe'

Push-Location $Script:RepoRoot
try {
    $env:GOOS = $Goos
    # Tests must run on the host GOOS; clear cross-compile target for -Test.
    Write-Step "go build ($Goos) $Packages"
    & $go build $Packages
    if ($LASTEXITCODE -ne 0) { throw "go build failed ($LASTEXITCODE)" }

    if ($Test) {
        Remove-Item Env:\GOOS -ErrorAction SilentlyContinue
        Write-Step "go test $Packages"
        & $go test $Packages
        if ($LASTEXITCODE -ne 0) { throw "go test failed ($LASTEXITCODE)" }
    }
    Write-Step "OK"
}
finally {
    Remove-Item Env:\GOOS -ErrorAction SilentlyContinue
    Pop-Location
}
