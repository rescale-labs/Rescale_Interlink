# check.ps1 --- vet + test the Go code using the portable toolchain.
#
#   powershell -ExecutionPolicy Bypass -File build\windows_local_build\check.ps1
#   powershell -ExecutionPolicy Bypass -File build\windows_local_build\check.ps1 -Test
#
# Mirrors the portable toolchain layout from _env.ps1. Checks the packages CI
# tests (.github/workflows/test.yml): all but the root package, which embeds
# the frontend build that dist.ps1 makes. `go vet` compiles them and their
# tests (`go build` refuses a package that has only tests), and -Test runs
# `go test`, both in FIPS 140-3 mode (GOFIPS140=certified, -tags fips) as CI
# does. Run install-deps.ps1 first to provision the toolchain.

[CmdletBinding()]
param(
    # Also run `go test`.
    [switch]$Test,
    # Restrict vet/test to these packages (default: the packages CI tests).
    [string]$Packages
)

. "$PSScriptRoot\_env.ps1"

if (-not (Test-Path (Join-Path $Script:GoBin 'go.exe'))) {
    throw "Go not found in toolchain. Run: powershell -ExecutionPolicy Bypass -File build\windows_local_build\install-deps.ps1"
}

Use-InterlinkToolchain
$go = Join-Path $Script:GoBin 'go.exe'

Push-Location $Script:RepoRoot
try {
    $env:GOFIPS140 = 'certified'
    $pkgs = $Packages
    if (-not $pkgs) {
        $pkgs = & $go list -e ./... | Where-Object { $_ -ne 'github.com/rescale/rescale-int' }
        if ($LASTEXITCODE -ne 0) { throw "go list failed ($LASTEXITCODE)" }
    }
    Write-Step "go vet"
    & $go vet -tags fips $pkgs
    if ($LASTEXITCODE -ne 0) { throw "go vet failed ($LASTEXITCODE)" }

    if ($Test) {
        Write-Step "go test"
        & $go test -tags fips $pkgs
        if ($LASTEXITCODE -ne 0) { throw "go test failed ($LASTEXITCODE)" }
    }
    Write-Step "OK"
}
finally {
    Remove-Item Env:\GOFIPS140 -ErrorAction SilentlyContinue
    Pop-Location
}
