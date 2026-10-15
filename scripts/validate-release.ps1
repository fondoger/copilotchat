#requires -Version 7.0
param([string]$ExpectedTag = '')
. (Join-Path $PSScriptRoot 'release-common.ps1')
Push-Location (Split-Path -Parent $PSScriptRoot)
try {
    $config = Get-ReleaseConfig -ExpectedTag $ExpectedTag
    Write-Output "Validated Copilot Chat $($config.version)."
} finally { Pop-Location }
