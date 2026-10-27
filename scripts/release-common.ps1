#requires -Version 7.0
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Invoke-CheckedCommand {
    param([Parameter(Mandatory)][string]$Command, [string[]]$Arguments = @())
    & $Command @Arguments
    if ($LASTEXITCODE -ne 0) { throw "$Command failed with exit code $LASTEXITCODE." }
}

function Get-ReleaseVersion {
    param([Parameter(Mandatory)][string]$Tag)
    if ($Tag -cnotmatch '^v(\d+\.\d+\.\d+(?:-dev\.\d+)?)$') {
        throw 'Release tag must have the form vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-dev.BUILD.'
    }
    return $Matches[1]
}

function Get-ReleaseSlug {
    param([Parameter(Mandatory)][object]$Config)
    if ($Config.PSObject.Properties['slug'] -and $Config.slug) { return $Config.slug }
    $slug = [regex]::Replace($Config.name.ToLowerInvariant(), '[^a-z0-9]+', '-').Trim('-')
    if ($slug) { return $slug }
    return 'app'
}

function Initialize-ReleaseBuildConfig {
    param([Parameter(Mandatory)][object]$Config)
    if ($Config.PSObject.Properties['updates'] -and $Config.updates) {
        $subject = & git log -1 --format=%s
        if ($LASTEXITCODE -ne 0) { throw 'Cannot read the release source commit.' }
        New-Item -ItemType Directory -Force -Path dist | Out-Null
        $changelog = 'dist/release-changelog.md'
        [IO.File]::WriteAllText((Join-Path (Get-Location) $changelog), "## $($Config.version)`n`n- $subject`n", [Text.UTF8Encoding]::new($false))
        $Config.updates | Add-Member -NotePropertyName changelog -NotePropertyValue $changelog -Force
    }
    [IO.File]::WriteAllText((Join-Path (Get-Location) 'mygo.json'), ($Config | ConvertTo-Json -Depth 20), [Text.UTF8Encoding]::new($false))
}

function Get-ReleasePlatforms {
    param([string]$Version = '')
    $platforms = @(
        [pscustomobject]@{ name = 'Windows x64'; runner = 'windows-2022'; platform = 'windows/amd64'; id = 'windows-x64' }
        [pscustomobject]@{ name = 'Windows ARM64'; runner = 'windows-11-arm'; platform = 'windows/arm64'; id = 'windows-arm64' }
        [pscustomobject]@{ name = 'macOS Intel'; runner = 'macos-15-intel'; platform = 'darwin/amd64'; id = 'macos-x64' }
        [pscustomobject]@{ name = 'macOS Apple silicon'; runner = 'macos-15'; platform = 'darwin/arm64'; id = 'macos-arm64' }
    )
    $platforms | Where-Object { $Version -notmatch '-dev\.' -or $_.platform -ne 'darwin/amd64' }
}

function Resolve-ReleaseTagCommit {
    param([Parameter(Mandatory)][string]$Tag)
    $null = Get-ReleaseVersion $Tag
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
    if ($config.version -notmatch '^\d+\.\d+\.\d+(?:-dev\.\d+)?$') {
        throw 'mygo.json version must be major.minor.patch with an optional -dev.BUILD suffix.'
    }
    if ($ExpectedTag) { $config.version = Get-ReleaseVersion $ExpectedTag }
    if ($ExpectedTag -and -not $FromTag) {
        $headCommit = & git rev-parse HEAD
        if ($LASTEXITCODE -ne 0 -or $tagCommit -cne $headCommit) {
            throw "Tag $ExpectedTag does not point at the checked-out commit."
        }
    }
    return $config
}

function Ensure-RemoteReleaseTag {
    param([string]$Repository, [string]$Tag)
    $object = & git rev-parse "refs/tags/$Tag"
    if ($LASTEXITCODE -ne 0) { throw 'Cannot resolve the local release tag.' }
    $json = & gh api "repos/$Repository/git/matching-refs/tags/$Tag"
    if ($LASTEXITCODE -ne 0) { throw 'Cannot inspect remote release tags.' }
    $refs = @(ConvertFrom-Json -InputObject ($json -join "`n"))
    $existing = @($refs | Where-Object { $_.ref -ceq "refs/tags/$Tag" })
    if ($existing.Count -gt 0) {
        if ($existing.Count -ne 1 -or $existing[0].object.sha -cne $object) {
            throw 'The remote release tag points at a different object.'
        }
        return
    }
    Invoke-CheckedCommand -Command gh -Arguments @('api', '--method', 'POST', "repos/$Repository/git/refs", '-f', "ref=refs/tags/$Tag", '-f', "sha=$object", '--silent')
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
    foreach ($platform in Get-ReleasePlatforms $Version) {
        "copilotchat-$Version-$($platform.id).zip"
        if ($platform.platform.StartsWith('windows/')) {
            "copilotchat-$Version-$($platform.id)-setup.exe"
        } else {
            "copilotchat-$Version-$($platform.id).dmg"
        }
    }
    if ($null -ne $Config) {
        foreach ($asset in Get-ReleaseUpdateAssets $Config) {
            $asset.Archive
            $asset.Manifest
        }
    }
}

function Get-ReleaseUpdateAssets {
    param([Parameter(Mandatory)][object]$Config, [string[]]$Targets = @())
    if (-not $Config.PSObject.Properties['updates'] -or $null -eq $Config.updates) { return }
    $updates = $Config.updates
    if ($Targets.Count -eq 0) {
        $Targets = @(Get-ReleasePlatforms $Config.version | ForEach-Object { $_.platform.Replace('/', '-') })
    }
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
    $slug = Get-ReleaseSlug $Config
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
    param([Parameter(Mandatory)][object]$Config, [Parameter(Mandatory)][string]$Directory, [string[]]$Targets = @())
    foreach ($asset in Get-ReleaseUpdateAssets -Config $Config -Targets $Targets) {
        $archive = Join-Path $Directory $asset.Archive
        $manifest = Join-Path $Directory $asset.Manifest
        foreach ($path in @($archive, $manifest)) {
            if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Missing signed update asset: $path" }
        }
        $verifyArguments = @('run', (Join-Path $PSScriptRoot 'verify-update/main.go'),
            '-archive', $archive, '-manifest', $manifest, '-version', $Config.version, '-url', $asset.URL,
            '-public-key', $Config.updates.publicKey, '-app-name', $Config.name, '-target', $asset.Target)
        Invoke-CheckedCommand -Command go -Arguments $verifyArguments
    }
}
