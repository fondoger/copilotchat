#requires -Version 7.0
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Invoke-CheckedCommand {
    param([Parameter(Mandatory)][string]$Command, [string[]]$Arguments = @())
    & $Command @Arguments
    if ($LASTEXITCODE -ne 0) { throw "$Command failed with exit code $LASTEXITCODE." }
}

function Get-ReleaseConfig {
    param([string]$ExpectedTag = '')
    $config = Get-Content -LiteralPath 'mygo.json' -Raw | ConvertFrom-Json
    if ($config.version -notmatch '^\d+\.\d+\.\d+$') {
        throw 'mygo.json version must be a stable major.minor.patch release.'
    }
    $appSource = Get-Content -LiteralPath 'internal/desktop/app.go' -Raw
    $appVersion = [regex]::Match($appSource, 'const version = "([^"]+)"').Groups[1].Value
    $clientSource = Get-Content -LiteralPath 'internal/copilot/client.go' -Raw
    $clientVersion = [regex]::Match($clientSource, 'CopilotChatNative/([^"\s]+)').Groups[1].Value
    if ($appVersion -ne $config.version -or $clientVersion -ne $config.version) {
        throw "Version mismatch: mygo.json=$($config.version), desktop=$appVersion, user agent=$clientVersion."
    }
    if ($ExpectedTag -and $ExpectedTag -cne "v$($config.version)") {
        throw "Tag $ExpectedTag does not match mygo.json version $($config.version)."
    }
    if ($ExpectedTag) {
        $tagCommit = & git rev-parse "${ExpectedTag}^{commit}"
        if ($LASTEXITCODE -ne 0) { throw "Tag $ExpectedTag must already exist." }
        $headCommit = & git rev-parse HEAD
        if ($LASTEXITCODE -ne 0 -or $tagCommit -cne $headCommit) {
            throw "Tag $ExpectedTag does not point at the checked-out commit."
        }
    }
    return $config
}

function Get-ReleaseAssetNames {
    param([Parameter(Mandatory)][string]$Version)
    foreach ($architecture in @('x64', 'arm64')) {
        "copilotchat-$Version-windows-$architecture.zip"
        "copilotchat-$Version-windows-$architecture-setup.exe"
        "copilotchat-$Version-macos-$architecture.zip"
        "copilotchat-$Version-macos-$architecture.dmg"
    }
}
