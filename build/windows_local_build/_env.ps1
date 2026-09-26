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
# Go 1.26.7 (go.mod requires 1.26.7), Node 24.21.0, Wails CLI v2.12.0, WiX 6.0.2.
# .NET SDK is needed only for the WiX MSI tool; channel 8.0 (LTS) is fine.
$Script:GoVersion     = $env:INTERLINK_GO_VERSION;     if (-not $Script:GoVersion)     { $Script:GoVersion     = '1.26.7' }
$Script:NodeVersion   = $env:INTERLINK_NODE_VERSION;   if (-not $Script:NodeVersion)   { $Script:NodeVersion   = '24.21.0' }
$Script:WailsVersion  = $env:INTERLINK_WAILS_VERSION;  if (-not $Script:WailsVersion)  { $Script:WailsVersion  = 'v2.12.0' }
$Script:DotnetVersion = $env:INTERLINK_DOTNET_VERSION; if (-not $Script:DotnetVersion) { $Script:DotnetVersion = '8.0.404' }
$Script:WixVersion    = $env:INTERLINK_WIX_VERSION;    if (-not $Script:WixVersion)    { $Script:WixVersion    = '6.0.2' }

# SHA-256 of each archive install-deps.ps1 downloads, by file name: from
# go.dev/dl, nodejs.org's SHASUMS256.txt, and (for .NET, which publishes
# SHA-512) the zip checked against Microsoft's release metadata. A version
# without an entry here is refused, so a new version needs its sums added.
$Script:Sha256 = @{
    'go1.26.7.windows-amd64.zip'       = 'f4f534a486e4bc3387fa18f08208f2f854b7aaea8a08f2a2d829a914a05abb11'
    'go1.26.7.windows-arm64.zip'       = '6f1b08de9e2dd94f69c52e524ab6834737275253291e8fd7f1c12ed4eceeda89'
    'node-v24.21.0-win-x64.zip'        = '158f7685b44de51f6c0df1d153526cbcd3e1bc739a8dfc607721cef75de9e541'
    'node-v24.21.0-win-arm64.zip'      = '8779b1bde1d39f8d420e3b57aa657b39891af434d3de44a919044cec06785921'
    'dotnet-sdk-8.0.404-win-x64.zip'   = '783417b46c6d411576b0dbaf6dbdd07b5728f5cdc5a74b026c2e262435625723'
    'dotnet-sdk-8.0.404-win-arm64.zip' = 'e1fc4ef9ec1fb8b08af0b57c4c571b8f32fc150f1cd1c991976ab32c39751314'
}

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
