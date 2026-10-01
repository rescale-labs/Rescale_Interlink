# write_support_files.ps1 --- write README.txt and LICENSE.txt into a Windows
# package's bin folder, with Windows line endings whatever the checkout has.
# build_dist.ps1 (release) and windows_local_build\dist.ps1 (local) both call
# it, so the two packages carry the same text.
param(
    [string]$BinDir,
    [string]$Version
)

$readme = @"
Rescale Interlink $Version
============================

Unified CLI and GUI for Rescale HPC platform.

Installation Directory: %LOCALAPPDATA%\Rescale\Interlink\

Components:
- rescale-int-gui.exe  : GUI application (double-click to run)
- rescale-int.exe      : CLI tool (run from command prompt)
- rescale-int-tray.exe : System tray companion

Usage:
GUI Mode:
  Double-click rescale-int-gui.exe, or run from Start Menu

CLI Mode:
  rescale-int --help
  rescale-int jobs list
  rescale-int upload file.txt

Documentation: https://docs.rescale.com
Source/issues: https://github.com/rescale-labs/Rescale_Interlink
Support:       support@rescale.com

Copyright (c) 2026 Rescale, Inc.
"@
$license = Get-Content -Raw -Encoding UTF8 (Join-Path $PSScriptRoot '..\LICENSE')

("$readme`n" -replace "`r?`n", "`r`n") | Out-File -FilePath (Join-Path $BinDir 'README.txt') -Encoding UTF8 -NoNewline
($license -replace "`r?`n", "`r`n") | Out-File -FilePath (Join-Path $BinDir 'LICENSE.txt') -Encoding UTF8 -NoNewline
