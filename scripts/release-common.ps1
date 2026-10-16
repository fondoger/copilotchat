#requires -Version 7.0
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Invoke-CheckedCommand {
    param([Parameter(Mandatory)][string]$Command, [string[]]$Arguments = @())
    & $Command @Arguments
    if ($LASTEXITCODE -ne 0) { throw "$Command failed with exit code $LASTEXITCODE." }
}

function Resolve-ReleaseTagCommit {
    param([Parameter(Mandatory)][string]$Tag)
    if ($Tag -cnotmatch '^v\d+\.\d+\.\d+$') { throw 'Release tag must have the form vMAJOR.MINOR.PATCH.' }
    $commit = & git rev-parse --verify "refs/tags/${Tag}^{commit}"
    if ($LASTEXITCODE -ne 0) { throw "Tag $Tag must already exist." }
    return $commit
}

function Get-ReleaseSource {
    param([Parameter(Mandatory)][string]$Path, [string]$Commit = '')
    if (-not $Commit) { return Get-Content -LiteralPath $Path -Raw }
    $content = & git show "${Commit}:$Path"
    if ($LASTEXITCODE -ne 0) { throw "Cannot read $Path at release commit $Commit." }
    return $content -join "`n"
}

function Get-ReleaseConfig {
    param([string]$ExpectedTag = '', [switch]$FromTag)
    if ($FromTag -and -not $ExpectedTag) { throw '-FromTag requires -ExpectedTag.' }
    $tagCommit = if ($ExpectedTag) { Resolve-ReleaseTagCommit $ExpectedTag } else { '' }
    $sourceCommit = if ($FromTag) { $tagCommit } else { '' }
    $config = Get-ReleaseSource 'mygo.json' $sourceCommit | ConvertFrom-Json
    if ($config.version -notmatch '^\d+\.\d+\.\d+$') {
        throw 'mygo.json version must be a stable major.minor.patch release.'
    }
    $appSource = Get-ReleaseSource 'internal/desktop/app.go' $sourceCommit
    $appVersion = [regex]::Match($appSource, 'const version = "([^"]+)"').Groups[1].Value
    $clientSource = Get-ReleaseSource 'internal/copilot/client.go' $sourceCommit
    $clientVersion = [regex]::Match($clientSource, 'CopilotChatNative/([^"\s]+)').Groups[1].Value
    if ($appVersion -ne $config.version -or $clientVersion -ne $config.version) {
        throw "Version mismatch: mygo.json=$($config.version), desktop=$appVersion, user agent=$clientVersion."
    }
    if ($ExpectedTag -and $ExpectedTag -cne "v$($config.version)") {
        throw "Tag $ExpectedTag does not match mygo.json version $($config.version)."
    }
    if ($ExpectedTag -and -not $FromTag) {
        $headCommit = & git rev-parse HEAD
        if ($LASTEXITCODE -ne 0 -or $tagCommit -cne $headCommit) {
            throw "Tag $ExpectedTag does not point at the checked-out commit."
        }
    }
    return $config
}

function Get-GitHubRelease {
    param([Parameter(Mandatory)][string]$Repository, [Parameter(Mandatory)][string]$Tag)
    # Drafts appear in this authenticated list, but the REST by-tag endpoint
    # returns 404 for them. Keep the ID for subsequent draft metadata requests.
    $json = & gh api --paginate --slurp "repos/$Repository/releases?per_page=100"
    if ($LASTEXITCODE -ne 0) { throw 'Could not query existing releases.' }
    # --slurp returns an array of page arrays. Preserve that shape explicitly:
    # neither singleton pages nor pages with many releases should change how
    # PowerShell enumerates the individual release objects.
    $pages = ConvertFrom-Json -InputObject ($json -join "`n") -NoEnumerate
    $matches = @(foreach ($page in $pages) {
        foreach ($release in $page) {
            if ($release.tag_name -ceq $Tag) { $release }
        }
    })
    if ($matches.Count -gt 1) { throw "More than one release exists for $Tag." }
    if ($matches.Count -eq 1) { return $matches[0] }
    return $null
}

function Get-ReleaseAssetNames {
    param([Parameter(Mandatory)][string]$Version, [object]$Config = $null)
    foreach ($architecture in @('x64', 'arm64')) {
        "copilotchat-$Version-windows-$architecture.zip"
        "copilotchat-$Version-windows-$architecture-setup.exe"
        "copilotchat-$Version-macos-$architecture.zip"
        "copilotchat-$Version-macos-$architecture.dmg"
    }
    if ($null -ne $Config) {
        foreach ($asset in Get-ReleaseUpdateAssets $Config) {
            $asset.Archive
            $asset.Manifest
        }
    }
}

function Get-ReleaseUpdateAssets {
    param([Parameter(Mandatory)][object]$Config, [string[]]$Targets = @('windows-amd64', 'windows-arm64', 'darwin-amd64', 'darwin-arm64'))
    if (-not $Config.PSObject.Properties['updates'] -or $null -eq $Config.updates) { return }
    $updates = $Config.updates
    if (-not $updates.PSObject.Properties['deltas'] -or $updates.deltas -ne 0) {
        throw 'This release pipeline requires updates.deltas=0; delta publishing is not configured.'
    }
    if (-not $updates.PSObject.Properties['github'] -or $updates.github -notmatch '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$') {
        throw 'Signed updates require updates.github in owner/repository form.'
    }
    if ($updates.PSObject.Properties['tagPrefix'] -and $updates.tagPrefix -and $updates.tagPrefix -cne 'v') {
        throw 'This release pipeline requires the v tag prefix.'
    }
    if (-not $updates.PSObject.Properties['publicKey'] -or -not $updates.publicKey) {
        throw 'Signed updates require updates.publicKey.'
    }
    $slug = [regex]::Replace($Config.name.ToLowerInvariant(), '[^a-z0-9]+', '-').Trim('-')
    if (-not $slug) { $slug = 'app' }
    foreach ($target in $Targets) {
        if ($target -cnotmatch '^(windows|darwin)-(amd64|arm64)$') { throw "Unsupported update target: $target" }
        $archive = "$slug-$($Config.version)-$target.tar.gz"
        [pscustomobject]@{
            Target = $target
            Archive = $archive
            Manifest = "update-$target.json"
            URL = "https://github.com/$($updates.github)/releases/download/v$($Config.version)/$archive"
        }
    }
}

function Assert-ReleaseUpdateArtifacts {
    param([Parameter(Mandatory)][object]$Config, [Parameter(Mandatory)][string]$Directory, [string[]]$Targets = @('windows-amd64', 'windows-arm64', 'darwin-amd64', 'darwin-arm64'))
    foreach ($asset in Get-ReleaseUpdateAssets -Config $Config -Targets $Targets) {
        $archive = Join-Path $Directory $asset.Archive
        $manifest = Join-Path $Directory $asset.Manifest
        foreach ($path in @($archive, $manifest)) {
            if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Missing signed update asset: $path" }
        }
        Invoke-CheckedCommand -Command go -Arguments @('run', (Join-Path $PSScriptRoot 'verify-update/main.go'),
            '-archive', $archive, '-manifest', $manifest, '-version', $Config.version, '-url', $asset.URL,
            '-public-key', $Config.updates.publicKey, '-app-name', $Config.name, '-target', $asset.Target)
    }
}
