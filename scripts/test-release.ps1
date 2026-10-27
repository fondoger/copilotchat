#requires -Version 7.0
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# These mocks are inherited by the copied publisher script. Every unrecognized
# command fails; this suite never invokes Git, gh, or a live release API.
$global:copilotChatReleaseTest = $null
function git {
    $arguments = @($args | ForEach-Object { [string]$_ })
    $global:LASTEXITCODE = 0
    $tag = 'v' + $global:copilotChatReleaseTest.Config.version
    switch ($arguments -join ' ') {
        "rev-parse --verify refs/tags/${tag}^{commit}" { return $global:copilotChatReleaseTest.TagCommit }
        "rev-parse refs/tags/$tag" { return $global:copilotChatReleaseTest.TagCommit }
        'tag --list v*' { return $global:copilotChatReleaseTest.TagList }
        'log -1 --format=%s' { return 'Fixture release source' }
        'rev-parse HEAD' { return $global:copilotChatReleaseTest.WorkflowCommit }
        "show $($global:copilotChatReleaseTest.TagCommit):mygo.json" { return $global:copilotChatReleaseTest.Config | ConvertTo-Json -Depth 5 -Compress }
        default { throw "Unexpected mocked git command: $($arguments -join ' ')" }
    }
}

function gh {
    $arguments = @($args | ForEach-Object { [string]$_ })
    $global:copilotChatReleaseTest.Calls.Add($arguments) | Out-Null
    $global:LASTEXITCODE = 0
    $command = $arguments -join ' '
    $tag = 'v' + $global:copilotChatReleaseTest.Config.version
    if ($command -eq 'api --paginate --slurp repos/test-owner/test-repo/releases?per_page=100') {
        $global:copilotChatReleaseTest.ListCalls++
        if ($global:copilotChatReleaseTest.ListFails) { $global:LASTEXITCODE = 1; return '' }
        if ($global:copilotChatReleaseTest.ListResponse) { return $global:copilotChatReleaseTest.ListResponse }
        $older = '{"id":100,"tag_name":"v0.2.0","draft":false}'
        if ($null -eq $global:copilotChatReleaseTest.Release -or
            ($global:copilotChatReleaseTest.HideCreated -and $global:copilotChatReleaseTest.Creates -gt 0)) { return "[[$older]]" }
        return "[[$older," + ($global:copilotChatReleaseTest.Release | ConvertTo-Json -Depth 10 -Compress) + ']]'
    }
    if ($command -eq "api repos/test-owner/test-repo/git/ref/tags/$tag --silent") {
        if ($global:copilotChatReleaseTest.RemoteTagMissing) { $global:LASTEXITCODE = 1 }
        return
    }
    if ($command -eq "api repos/test-owner/test-repo/git/matching-refs/tags/$tag") {
        if ($global:copilotChatReleaseTest.RemoteTagMissing) { return '[]' }
        return @(@{ ref = "refs/tags/$tag"; object = @{ sha = $global:copilotChatReleaseTest.TagCommit } }) | ConvertTo-Json -Depth 5 -AsArray -Compress
    }
    if ($command.StartsWith('api --method POST repos/test-owner/test-repo/git/refs ')) {
        $global:copilotChatReleaseTest.RemoteTagMissing = $false
        $global:copilotChatReleaseTest.TagsCreated++
        return
    }
    if ($command.StartsWith('api --method POST repos/test-owner/test-repo/releases --input ')) {
        $requestPath = $arguments[[Array]::IndexOf($arguments, '--input') + 1]
        $request = Get-Content -LiteralPath $requestPath -Raw | ConvertFrom-Json
        if ($request.target_commitish -cne $global:copilotChatReleaseTest.TagCommit -or $request.tag_name -cne $tag -or
            -not $request.draft -or -not $request.generate_release_notes -or $request.name -cne "Copilot Chat $tag" -or $request.prerelease -ne ($global:copilotChatReleaseTest.Config.version -match '-dev\.')) {
            throw 'Create must make the expected draft for the existing tag commit.'
        }
        $global:copilotChatReleaseTest.Creates++
        $global:copilotChatReleaseTest.ReleaseNotes = $request.body
        $global:copilotChatReleaseTest.Release = New-TestRelease
        if ($global:copilotChatReleaseTest.InvalidCreatedID) { $global:copilotChatReleaseTest.Release.id = 'RE_graphql_id' }
        return $global:copilotChatReleaseTest.Release | ConvertTo-Json -Depth 10 -Compress
    }
    if ($command.StartsWith("release upload $tag ")) {
        $global:copilotChatReleaseTest.Release.assets = @(Get-ChildItem -LiteralPath release -File | ForEach-Object {
            $digest = 'sha256:' + (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
            if ($global:copilotChatReleaseTest.BadDigest) { $digest = 'sha256:' + ('0' * 64) }
            $asset = [ordered]@{ name = $_.Name; state = 'uploaded'; size = $_.Length }
            if (-not $global:copilotChatReleaseTest.OmitDigest) { $asset.digest = $digest }
            [pscustomobject]$asset
        })
        return
    }
    if ($command -eq 'api repos/test-owner/test-repo/releases/414008935') {
        return $global:copilotChatReleaseTest.Release | ConvertTo-Json -Depth 10 -Compress
    }
    if ($command.Contains('/releases/tags/')) { throw 'Draft by-tag lookup returns 404; use its numeric release ID.' }
    if ($command.StartsWith("release download $tag ")) {
        $name = $arguments[[Array]::IndexOf($arguments, '--pattern') + 1]
        $destination = $arguments[[Array]::IndexOf($arguments, '--dir') + 1]
        Copy-Item -LiteralPath (Join-Path release $name) -Destination (Join-Path $destination $name) -Force
        return
    }
    if ($command -eq "release edit $tag --repo test-owner/test-repo --draft=false --latest" -or
        $command -eq "release edit $tag --repo test-owner/test-repo --draft=false --prerelease --latest=false") {
        $global:copilotChatReleaseTest.Release.draft = $false
        $global:copilotChatReleaseTest.Latest = -not $command.Contains('--latest=false')
        return
    }
    throw "Unexpected mocked gh command: $command"
}

function Assert-True {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw "Assertion failed: $Message" }
}

function Assert-Throws {
    param([scriptblock]$Action, [string]$MessagePattern)
    $caught = $false
    try { & $Action | Out-Null } catch {
        $caught = $true
        Assert-True ($_.Exception.Message -like $MessagePattern) "Unexpected failure: $($_.Exception.Message)"
    }
    Assert-True $caught "Expected failure matching $MessagePattern"
}

function New-TestRelease {
    return [pscustomobject]@{
        id = 414008935
        draft = $true
        tag_name = 'v' + $global:copilotChatReleaseTest.Config.version
        prerelease = ($global:copilotChatReleaseTest.Config.version -match '-dev\.')
        target_commitish = $global:copilotChatReleaseTest.TagCommit
        assets = @()
    }
}

function Reset-TestState {
    $global:copilotChatReleaseTest = @{
        TagCommit = '1111111111111111111111111111111111111111'
        WorkflowCommit = '2222222222222222222222222222222222222222'
        Calls = [System.Collections.Generic.List[object]]::new()
        Release = $null
        BadDigest = $false
        OmitDigest = $false
        ListFails = $false
        ListCalls = 0
        ListResponse = ''
        Creates = 0
        HideCreated = $false
        RemoteTagMissing = $false
        InvalidCreatedID = $false
        ReleaseNotes = ''
        Latest = $true
        TagsCreated = 0
        TagList = @('v0.2.0')
        Config = [pscustomobject]@{ name = 'Copilot Chat'; version = '0.3.0' }
    }
    $env:GITHUB_REPOSITORY = 'test-owner/test-repo'
    $env:GITHUB_SHA = $global:copilotChatReleaseTest.WorkflowCommit
}

function Assert-NoPublicMutation {
    $mutations = @($global:copilotChatReleaseTest.Calls | Where-Object { ($_ -join ' ') -match '^(release (create|upload|edit) |api --method POST )' })
    Assert-True ($mutations.Count -eq 0) 'Rejected release must not be created, uploaded, or published.'
}

$fixtureRoot = Join-Path ([IO.Path]::GetTempPath()) ('copilotchat-release-tests-' + [guid]::NewGuid().ToString('N'))
$savedRepository = $env:GITHUB_REPOSITORY
$savedSHA = $env:GITHUB_SHA
$savedOutput = $env:GITHUB_OUTPUT
$savedExitCode = Get-Variable LASTEXITCODE -Scope Global -ErrorAction SilentlyContinue
try {
    New-Item -ItemType Directory -Path (Join-Path $fixtureRoot 'scripts'), (Join-Path $fixtureRoot 'release') -Force | Out-Null
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'release-common.ps1'), (Join-Path $PSScriptRoot 'publish-release.ps1'), (Join-Path $PSScriptRoot 'prepare-release.ps1') -Destination (Join-Path $fixtureRoot 'scripts')
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'verify-update') -Destination (Join-Path $fixtureRoot 'scripts/verify-update') -Recurse
    . (Join-Path $fixtureRoot 'scripts/release-common.ps1')
    $publisher = Join-Path $fixtureRoot 'scripts/publish-release.ps1'
    # Current workflow metadata deliberately differs from the release tag.
    Set-Content -LiteralPath (Join-Path $fixtureRoot 'mygo.json') -Value '{"version":"9.9.9"}'
    foreach ($name in Get-ReleaseAssetNames '0.3.0') {
        Set-Content -LiteralPath (Join-Path $fixtureRoot "release/$name") -Value "fixture for $name"
    }

    Reset-TestState
    $targetJSON = New-TestRelease | ConvertTo-Json -Depth 10 -Compress
    $olderJSON = '{"id":100,"tag_name":"v0.2.0","draft":false}'
    $newerJSON = '{"id":200,"tag_name":"v0.4.0","draft":true}'
    foreach ($pageJSON in @("[[$olderJSON,$targetJSON,$newerJSON]]", "[[$olderJSON],[],[$newerJSON,$targetJSON]]")) {
        $global:copilotChatReleaseTest.ListResponse = $pageJSON
        $listed = Get-GitHubRelease test-owner/test-repo v0.3.0
        Assert-True ($listed.id -eq 414008935) 'The exact draft must be found among multiple releases and pages.'
    }
    $global:copilotChatReleaseTest.ListResponse = "[[$targetJSON],[$targetJSON]]"
    Assert-Throws { Get-GitHubRelease test-owner/test-repo v0.3.0 } '*More than one release exists*'
    Write-Host 'PASS: multi-release pages, empty pages, and duplicate-tag detection'

    Reset-TestState
    & $publisher -Tag v0.3.0
    Assert-True (-not $global:copilotChatReleaseTest.Release.draft) 'A complete new draft must publish.'
    Assert-True ($global:copilotChatReleaseTest.Release.assets.Count -eq 9) 'Eight packages and the checksum must upload.'
    $metadataCalls = @($global:copilotChatReleaseTest.Calls | Where-Object { ($_ -join ' ') -eq 'api repos/test-owner/test-repo/releases/414008935' })
    Assert-True ($metadataCalls.Count -eq 1) 'A new draft must be verified using its numeric REST ID.'
    Write-Host 'PASS: create draft, read tagged metadata, verify all assets by release ID, publish'

    Reset-TestState
    $global:copilotChatReleaseTest.HideCreated = $true
    & $publisher -Tag v0.3.0
    Assert-True (-not $global:copilotChatReleaseTest.Release.draft) 'The POST response must allow publication while a draft remains invisible in lists.'
    Assert-True ($global:copilotChatReleaseTest.Creates -eq 1 -and $global:copilotChatReleaseTest.ListCalls -eq 1) 'Create must happen once and must not depend on a second list request.'
    Write-Host 'PASS: delayed list visibility uses the create response without recreating the draft'

    Reset-TestState
    $global:copilotChatReleaseTest.RemoteTagMissing = $true
    Assert-Throws { & $publisher -Tag v0.3.0 } '*gh failed with exit code*'
    Assert-NoPublicMutation
    Write-Host 'PASS: a missing remote tag cannot be created by the release publisher'

    Reset-TestState
    $global:copilotChatReleaseTest.InvalidCreatedID = $true
    Assert-Throws { & $publisher -Tag v0.3.0 } '*valid numeric REST release ID*'
    Assert-True $global:copilotChatReleaseTest.Release.draft 'An invalid create response must remain a draft.'
    $uploads = @($global:copilotChatReleaseTest.Calls | Where-Object { ($_ -join ' ') -like 'release upload *' })
    Assert-True ($uploads.Count -eq 0) 'A nonnumeric release ID must fail before uploading.'
    Write-Host 'PASS: nonnumeric create IDs are rejected before asset upload'

    Reset-TestState
    $global:copilotChatReleaseTest.Release = New-TestRelease
    & $publisher -Tag v0.3.0
    Assert-True (-not $global:copilotChatReleaseTest.Release.draft) 'An existing valid draft must publish.'
    Assert-True ($global:copilotChatReleaseTest.Creates -eq 0) 'Retry must reuse the existing draft.'
    Write-Host 'PASS: resume existing draft from a different workflow commit'

    Reset-TestState
    $global:copilotChatReleaseTest.Release = New-TestRelease
    $global:copilotChatReleaseTest.Release.draft = $false
    Assert-Throws { & $publisher -Tag v0.3.0 } '*already public*'
    Assert-NoPublicMutation
    Write-Host 'PASS: public release is rejected before mutation'

    Reset-TestState
    $global:copilotChatReleaseTest.Release = New-TestRelease
    $global:copilotChatReleaseTest.Release.target_commitish = $global:copilotChatReleaseTest.WorkflowCommit
    Assert-Throws { & $publisher -Tag v0.3.0 } '*different commit*'
    Assert-NoPublicMutation
    Write-Host 'PASS: draft targeting the workflow commit is rejected'

    Reset-TestState
    $global:copilotChatReleaseTest.Release = New-TestRelease
    $global:copilotChatReleaseTest.BadDigest = $true
    Assert-Throws { & $publisher -Tag v0.3.0 } '*digest differs*'
    Assert-True $global:copilotChatReleaseTest.Release.draft 'A digest mismatch must leave the release as a draft.'
    Write-Host 'PASS: mismatched asset digest leaves a recoverable draft'

    Reset-TestState
    $global:copilotChatReleaseTest.OmitDigest = $true
    & $publisher -Tag v0.3.0
    $downloads = @($global:copilotChatReleaseTest.Calls | Where-Object { ($_ -join ' ') -like 'release download *' })
    Assert-True ($downloads.Count -eq 9) 'Missing API digests must verify all downloaded bytes.'
    Assert-True (-not $global:copilotChatReleaseTest.Release.draft) 'Matching downloaded bytes must allow publication.'
    Write-Host 'PASS: byte verification supports API responses without digests'

    Reset-TestState
    $global:copilotChatReleaseTest.ListFails = $true
    Assert-Throws { & $publisher -Tag v0.3.0 } '*Could not query existing releases*'
    Assert-NoPublicMutation
    Write-Host 'PASS: list/auth failure is not treated as a missing release'

    Reset-TestState
    Assert-Throws { & $publisher -Tag main } '*form vMAJOR.MINOR.PATCH*'
    Assert-True ($global:copilotChatReleaseTest.Calls.Count -eq 0) 'Invalid tags must fail before calling GitHub.'
    Write-Host 'PASS: branch names cannot be used as release tags'

    Push-Location $fixtureRoot
    try {
        Assert-True ((Get-ReleaseConfig -ExpectedTag v0.3.0 -FromTag).version -ceq '0.3.0') 'Publisher must read the tagged version.'
        Set-Content -LiteralPath mygo.json -Value '{"version":"0.3.0"}'
        Assert-Throws { Get-ReleaseConfig -ExpectedTag v0.3.0 } '*does not point at the checked-out commit*'
    } finally { Pop-Location }
    Write-Host 'PASS: tagged metadata is separate from strict build checkout validation'

    Reset-TestState
    Remove-Item -LiteralPath (Join-Path $fixtureRoot 'release/copilotchat-0.3.0-macos-arm64.dmg')
    Assert-Throws { & $publisher -Tag v0.3.0 } '*incomplete release*'
    Assert-True ($global:copilotChatReleaseTest.Calls.Count -eq 0) 'Missing packages must fail before calling GitHub.'
    Write-Host 'PASS: missing platform package blocks all GitHub calls'

    Set-Content -LiteralPath (Join-Path $fixtureRoot 'release/copilotchat-0.3.0-macos-arm64.dmg') -Value 'restored package fixture'
    Invoke-CheckedCommand -Command go -Arguments @('run', (Join-Path $PSScriptRoot 'testdata/update-fixtures.go'), (Join-Path $fixtureRoot 'release'))
    $testPublicKeyPath = Join-Path $fixtureRoot 'release/test-public-key.txt'
    $testPublicKey = Get-Content -LiteralPath $testPublicKeyPath -Raw
    Remove-Item -LiteralPath $testPublicKeyPath
    $signedConfig = [pscustomobject]@{
        name = 'Copilot Chat'; version = '0.3.0'
        updates = [pscustomobject]@{ github = 'test-owner/test-repo'; publicKey = $testPublicKey; deltas = 0 }
    }
    Reset-TestState
    $global:copilotChatReleaseTest.Config = $signedConfig
    & $publisher -Tag v0.3.0
    Assert-True (-not $global:copilotChatReleaseTest.Release.draft) 'A complete signed release must publish.'
    Assert-True ($global:copilotChatReleaseTest.Release.assets.Count -eq 17) 'Signed release requires eight packages, eight update assets, and the checksum.'
    Write-Host 'PASS: signed releases verify all four manifests and archives before publication'

    $manifestPath = Join-Path $fixtureRoot 'release/update-windows-amd64.json'
    $manifestSource = Get-Content -LiteralPath $manifestPath -Raw
    $manifestWithNotes = $manifestSource | ConvertFrom-Json
    $manifestWithNotes | Add-Member -NotePropertyName notes -NotePropertyValue 'Release fixture: install this version manually once to enable future updates.'
    Set-Content -LiteralPath $manifestPath -Value ($manifestWithNotes | ConvertTo-Json)
    Reset-TestState
    $global:copilotChatReleaseTest.Config = $signedConfig
    & $publisher -Tag v0.3.0
    Assert-True ($global:copilotChatReleaseTest.ReleaseNotes.Contains($manifestWithNotes.notes)) 'Release notes must include changes from the verified update manifest.'
    Write-Host 'PASS: release notes include the verified manifest changelog when present'

    $invalidManifest = $manifestSource | ConvertFrom-Json
    $invalidManifest.signature = [Convert]::ToBase64String([byte[]]::new(64))
    Set-Content -LiteralPath $manifestPath -Value ($invalidManifest | ConvertTo-Json)
    Reset-TestState
    $global:copilotChatReleaseTest.Config = $signedConfig
    Assert-Throws { & $publisher -Tag v0.3.0 } '*go failed with exit code*'
    Assert-True ($global:copilotChatReleaseTest.Calls.Count -eq 0) 'Invalid update signatures must fail before calling GitHub.'
    Set-Content -LiteralPath $manifestPath -Value $manifestSource
    Write-Host 'PASS: invalid update signature blocks all GitHub calls'

    Remove-Item -LiteralPath $manifestPath
    Reset-TestState
    $global:copilotChatReleaseTest.Config = $signedConfig
    Assert-Throws { & $publisher -Tag v0.3.0 } '*incomplete release*'
    Assert-True ($global:copilotChatReleaseTest.Calls.Count -eq 0) 'Missing update manifests must fail before calling GitHub.'
    Write-Host 'PASS: missing platform update manifest blocks all GitHub calls'

    Reset-TestState
    $prepare = Join-Path $fixtureRoot 'scripts/prepare-release.ps1'
    $identity = Join-Path $fixtureRoot 'release-info.json'
    $env:GITHUB_OUTPUT = Join-Path $fixtureRoot 'outputs.txt'
    $prepared = (& $prepare -Channel dev -RunNumber 42 -MetadataPath $identity) -join "`n" | ConvertFrom-Json
    Assert-True ($prepared.tag -ceq 'v0.2.1-dev.42' -and $prepared.source -ceq $global:copilotChatReleaseTest.WorkflowCommit) 'Dev versions must increment automatically and bind to the source commit.'
    $global:copilotChatReleaseTest.TagList += 'v0.2.1'
    $retry = (& $prepare -Channel dev -RunNumber 42 -MetadataPath $identity) -join "`n" | ConvertFrom-Json
    Assert-True ($retry.tag -ceq $prepared.tag) 'A retry must preserve its release identity after newer stable tags appear.'
    $stable = (& $prepare -Channel stable -RunNumber 43) -join "`n" | ConvertFrom-Json
    Assert-True ($stable.tag -ceq 'v0.2.2') 'Stable versions must increment without editing mygo.json.'
    Write-Host 'PASS: automatic dev/stable versions and frozen retry metadata'
    Push-Location $fixtureRoot
    try {
        $buildConfig = [pscustomobject]@{ name = 'Copilot Chat'; version = $prepared.version; updates = [pscustomobject]@{ github = 'test-owner/test-repo' } }
        Initialize-ReleaseBuildConfig $buildConfig
        $materialized = Get-Content -LiteralPath mygo.json -Raw | ConvertFrom-Json
        $notes = Get-Content -LiteralPath $materialized.updates.changelog -Raw
        Assert-True ($materialized.version -ceq $prepared.version -and $notes.Contains("## $($prepared.version)") -and $notes.Contains('Fixture release source')) 'Generated versions must have matching MyGo update notes.'
    } finally { Pop-Location }
    Write-Host 'PASS: generated build configuration includes versioned MyGo release notes'

    Reset-TestState
    $global:copilotChatReleaseTest.Config.version = '0.3.1-dev.42'
    $global:copilotChatReleaseTest.RemoteTagMissing = $true
    Remove-Item -LiteralPath (Join-Path $fixtureRoot 'release') -Recurse -Force
    New-Item -ItemType Directory -Path (Join-Path $fixtureRoot 'release') | Out-Null
    foreach ($name in Get-ReleaseAssetNames '0.3.1-dev.42') {
        Set-Content -LiteralPath (Join-Path $fixtureRoot "release/$name") -Value "fixture for $name"
    }
    & $publisher -Tag v0.3.1-dev.42 -CreateTag
    Assert-True (-not $global:copilotChatReleaseTest.Release.draft -and $global:copilotChatReleaseTest.Release.prerelease) 'Dev must publish as a prerelease.'
    Assert-True (-not $global:copilotChatReleaseTest.Latest -and $global:copilotChatReleaseTest.TagsCreated -eq 1) 'Dev must create its tag after validation without changing latest.'
    Assert-True ($global:copilotChatReleaseTest.Release.assets.Count -eq 7) 'Dev must publish three platform package pairs plus checksums.'
    Assert-True (-not ($global:copilotChatReleaseTest.Release.assets.name -match 'macos-x64')) 'Dev must omit macOS Intel.'
    $devConfig = [pscustomobject]@{ name = 'Fixture'; version = '0.3.1-dev.42'; updates = [pscustomobject]@{ github = 'test-owner/test-repo'; publicKey = 'fixture'; deltas = 0 } }
    Assert-True (@(Get-ReleaseUpdateAssets $devConfig).Count -eq 3) 'Dev signed update targets must match its package matrix.'
    Write-Host 'PASS: validated dev tag creation, three platforms, prerelease and stable latest preservation'

    Invoke-CheckedCommand -Command go -Arguments @('run', (Join-Path $PSScriptRoot 'testdata/update-fixtures.go'), (Join-Path $fixtureRoot 'release'), '0.3.1-dev.42', 'windows-amd64', 'windows-arm64', 'darwin-arm64')
    $devKeyPath = Join-Path $fixtureRoot 'release/test-public-key.txt'
    $devKey = Get-Content -LiteralPath $devKeyPath -Raw
    Remove-Item -LiteralPath $devKeyPath
    Reset-TestState
    $global:copilotChatReleaseTest.Config = [pscustomobject]@{
        name = 'Copilot Chat'; version = '0.3.1-dev.42'
        updates = [pscustomobject]@{ github = 'test-owner/test-repo'; publicKey = $devKey; deltas = 0 }
    }
    & $publisher -Tag v0.3.1-dev.42
    Assert-True ($global:copilotChatReleaseTest.Release.assets.Count -eq 13 -and -not $global:copilotChatReleaseTest.Release.draft) 'Dev must verify and publish all three signed update archives and manifests.'
    Write-Host 'PASS: signed dev publication verifies exactly three native targets'
} finally {
    $env:GITHUB_REPOSITORY = $savedRepository
    $env:GITHUB_SHA = $savedSHA
    $env:GITHUB_OUTPUT = $savedOutput
    if ($null -ne $savedExitCode) { $global:LASTEXITCODE = $savedExitCode.Value }
    # Only delete this suite's uniquely named directory inside the OS temp root.
    $resolvedRoot = [IO.Path]::GetFullPath($fixtureRoot)
    $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    if (-not $resolvedRoot.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase) -or (Split-Path -Leaf $resolvedRoot) -notmatch '^copilotchat-release-tests-[a-f0-9]{32}$') {
        throw 'Refusing to remove a test directory outside the expected temporary path.'
    }
    if (Test-Path -LiteralPath $resolvedRoot) { Remove-Item -LiteralPath $resolvedRoot -Recurse -Force }
}
