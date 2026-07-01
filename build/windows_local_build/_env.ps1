# _env.ps1 --- shared toolchain layout + PATH setup for the portable Windows build.
#
# Dot-source this from install-deps.ps1 and build.ps1:
#     . "$PSScriptRoot\_env.ps1"
#
# Everything lives under a SINGLE directory (build\windows_local_build\.toolchain)
# so the whole toolchain can be wiped by deleting that one folder. No admin
# rights, no system PATH changes, no registry writes --- paths are set for the
# current process only.

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# --- Pinned versions (mirror .github/workflows/release.yml) ------------------
# Go 1.26.3 (go.mod requires 1.26.3), Node 20, Wails CLI v2.12.0, WiX 6.0.2.
# .NET SDK is needed only for the WiX MSI tool; channel 8.0 (LTS) is fine.
$Script:GoVersion     = $env:INTERLINK_GO_VERSION;     if (-not $Script:GoVersion)     { $Script:GoVersion     = '1.26.3' }
$Script:NodeVersion   = $env:INTERLINK_NODE_VERSION;   if (-not $Script:NodeVersion)   { $Script:NodeVersion   = '20.19.0' }
$Script:WailsVersion  = $env:INTERLINK_WAILS_VERSION;  if (-not $Script:WailsVersion)  { $Script:WailsVersion  = 'v2.12.0' }
$Script:DotnetVersion = $env:INTERLINK_DOTNET_VERSION; if (-not $Script:DotnetVersion) { $Script:DotnetVersion = '8.0.404' }
$Script:WixVersion    = $env:INTERLINK_WIX_VERSION;    if (-not $Script:WixVersion)    { $Script:WixVersion    = '6.0.2' }

# --- Directory layout --------------------------------------------------------
# build\windows_local_build\.toolchain\
#   go\            (GOROOT --- go.exe in go\bin)
#   node\          (node.exe + npm in this dir)
#   gopath\        (GOPATH --- wails.exe lands in gopath\bin)
#   dotnet\        (DOTNET_ROOT --- dotnet.exe here; WiX tool in dotnet\tools)
# RepoRoot is two levels up (build\windows_local_build -> build -> repo root).
$Script:RepoRoot      = Split-Path (Split-Path $PSScriptRoot -Parent) -Parent
$Script:ToolchainDir  = Join-Path $PSScriptRoot '.toolchain'
$Script:GoRoot        = Join-Path $Script:ToolchainDir 'go'
$Script:NodeDir       = Join-Path $Script:ToolchainDir 'node'
$Script:GoPath        = Join-Path $Script:ToolchainDir 'gopath'
$Script:GoBin         = Join-Path $Script:GoRoot 'bin'
$Script:GoPathBin     = Join-Path $Script:GoPath 'bin'
$Script:DotnetDir     = Join-Path $Script:ToolchainDir 'dotnet'
$Script:DotnetTools   = Join-Path $Script:DotnetDir 'tools'

# Apply the toolchain to the CURRENT process environment only.
function Use-InterlinkToolchain {
    $env:GOROOT = $Script:GoRoot
    $env:GOPATH = $Script:GoPath
    # GOBIN so `go install` drops wails.exe into a path we control.
    $env:GOBIN  = $Script:GoPathBin
    # Keep Go's module/build cache inside the toolchain dir too, so nothing
    # leaks into the user profile and the whole thing stays self-contained.
    $env:GOCACHE    = Join-Path $Script:ToolchainDir 'gocache'
    $env:GOMODCACHE = Join-Path $Script:GoPath 'pkg\mod'

    # .NET: keep the SDK self-contained under the toolchain dir. DOTNET_ROOT
    # tells dotnet where the runtime lives; the per-tool install dir is set via
    # `dotnet tool install --tool-path` in install-deps.ps1, so WiX lands in
    # $Script:DotnetTools rather than the user profile.
    if (Test-Path $Script:DotnetDir) {
        $env:DOTNET_ROOT = $Script:DotnetDir
        $env:DOTNET_CLI_TELEMETRY_OPTOUT = '1'
        $env:DOTNET_SKIP_FIRST_TIME_EXPERIENCE = '1'
    }

    # Prepend our bins so they win over any system Go/Node/.NET/WiX.
    $prepend = @($Script:GoBin, $Script:NodeDir, $Script:GoPathBin, $Script:DotnetDir, $Script:DotnetTools) -join ';'
    $env:PATH = "$prepend;$env:PATH"
}
