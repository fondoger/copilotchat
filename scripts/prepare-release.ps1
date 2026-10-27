#requires -Version 7.0
param(
    [ValidateSet('stable', 'dev')][string]$Channel = 'dev',
    [string]$Tag = '',
    [long]$RunNumber = 0,
    [string]$MetadataPath = ''
)
. (Join-Path $PSScriptRoot 'release-common.ps1')
Push-Location (Split-Path -Parent $PSScriptRoot)
try {
    $workflowCommit = & git rev-parse HEAD
    if ($LASTEXITCODE -ne 0) { throw 'Cannot resolve the workflow commit.' }
    if ($MetadataPath -and (Test-Path -LiteralPath $MetadataPath)) {
        $info = Get-Content -LiteralPath $MetadataPath -Raw | ConvertFrom-Json
        if ($info.workflowCommit -cne $workflowCommit -or ($Tag -and $Tag -cne $info.tag)) {
            throw 'Saved release metadata belongs to a different source or tag.'
        }
        if ((Get-ReleaseVersion $info.tag) -cne $info.version -or $info.source -cnotmatch '^[0-9a-f]{40}$') {
            throw 'Saved release metadata is invalid.'
        }
    } elseif ($Tag) {
        $version = Get-ReleaseVersion $Tag
        $info = [ordered]@{
            tag = $Tag; version = $version
            channel = $(if ($version -match '-dev\.') { 'dev' } else { 'stable' })
            source = (Resolve-ReleaseTagCommit $Tag)
            workflowCommit = $workflowCommit; createTag = $false
        }
    } else {
        $tags = & git tag --list 'v*'
        if ($LASTEXITCODE -ne 0) { throw 'Cannot list release tags.' }
        $stable = $tags | Where-Object { $_ -cmatch '^v\d+\.\d+\.\d+$' } |
            ForEach-Object { [version]$_.Substring(1) } | Sort-Object -Descending | Select-Object -First 1
        if ($null -ne $stable) {
            $version = '{0}.{1}.{2}' -f $stable.Major, $stable.Minor, ($stable.Build + 1)
        } else {
            $version = (Get-ReleaseConfig).version
        }
        if ($Channel -eq 'dev') {
            if ($RunNumber -le 0) { throw 'A dev release needs a positive workflow run number.' }
            $version += "-dev.$RunNumber"
        }
        $info = [ordered]@{
            tag = "v$version"; version = $version; channel = $Channel
            source = $workflowCommit; workflowCommit = $workflowCommit; createTag = $true
        }
    }
    $info = [pscustomobject]$info
    if ($MetadataPath) {
        [IO.File]::WriteAllText($MetadataPath, ($info | ConvertTo-Json), [Text.UTF8Encoding]::new($false))
    }
    if ($env:GITHUB_OUTPUT) {
        foreach ($name in @('tag', 'version', 'channel', 'source')) {
            "$name=$($info.$name)" >> $env:GITHUB_OUTPUT
        }
        "create-tag=$($info.createTag.ToString().ToLowerInvariant())" >> $env:GITHUB_OUTPUT
    }
    $info | ConvertTo-Json
} finally { Pop-Location }
