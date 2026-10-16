#requires -Version 7.0
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# These mocks are inherited by the copied publisher script. Every unrecognized
# command fails; this suite never invokes Git, gh, or a live release API.
$global:copilotChatReleaseTest = $null
function git {
    $arguments = @($args | ForEach-Object { [string]$_ })
    $global:LASTEXITCODE = 0
    switch ($arguments -join ' ') {
        'rev-parse --verify refs/tags/v0.3.0^{commit}' { return $global:copilotChatReleaseTest.TagCommit }
        'rev-parse HEAD' { return $global:copilotChatReleaseTest.WorkflowCommit }
        "show $($global:copilotChatReleaseTest.TagCommit):mygo.json" { return $global:copilotChatReleaseTest.Config | ConvertTo-Json -Depth 5 -Compress }
        "show $($global:copilotChatReleaseTest.TagCommit):internal/desktop/app.go" { return 'const version = "0.3.0"' }
        "show $($global:copilotChatReleaseTest.TagCommit):internal/copilot/client.go" { return 'const userAgent = "CopilotChatNative/0.3.0"' }
        default { throw "Unexpected mocked git command: $($arguments -join ' ')" }
    }
}

function gh {
    $arguments = @($args | ForEach-Object { [string]$_ })
    $global:copilotChatReleaseTest.Calls.Add($arguments) | Out-Null
    $global:LASTEXITCODE = 0
    $command = $arguments -join ' '
    if ($command -eq 'api --paginate --slurp repos/test-owner/test-repo/releases?per_page=100') {
        if ($global:copilotChatReleaseTest.ListFails) { $global:LASTEXITCODE = 1; return '' }
        # A second page exercises the --slurp page-array shape as well as drafts.
        if ($null -eq $global:copilotChatReleaseTest.Release) { return '[[],[]]' }
        return '[[],[' + ($global:copilotChatReleaseTest.Release | ConvertTo-Json -Depth 10 -Compress) + ']]'
    }
    if ($command.StartsWith('release create v0.3.0 ')) {
        $targetIndex = [Array]::IndexOf($arguments, '--target')
        if ($targetIndex -lt 0 -or $arguments[$targetIndex + 1] -cne $global:copilotChatReleaseTest.TagCommit) {
            throw 'Create must target the tag commit, not the workflow commit.'
        }
        $notesIndex = [Array]::IndexOf($arguments, '--notes-file')
        $global:copilotChatReleaseTest.ReleaseNotes = Get-Content -LiteralPath $arguments[$notesIndex + 1] -Raw
        $global:copilotChatReleaseTest.Release = New-TestRelease
        return
    }
    if ($command.StartsWith('release upload v0.3.0 ')) {
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
    if ($command.StartsWith('release download v0.3.0 ')) {
        $name = $arguments[[Array]::IndexOf($arguments, '--pattern') + 1]
        $destination = $arguments[[Array]::IndexOf($arguments, '--dir') + 1]
        Copy-Item -LiteralPath (Join-Path release $name) -Destination (Join-Path $destination $name) -Force
        return
    }
    if ($command -eq 'release edit v0.3.0 --repo test-owner/test-repo --draft=false --latest') {
        $global:copilotChatReleaseTest.Release.draft = $false
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
        tag_name = 'v0.3.0'
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
        ReleaseNotes = ''
        Config = [pscustomobject]@{ name = 'Copilot Chat'; version = '0.3.0' }
    }
    $env:GITHUB_REPOSITORY = 'test-owner/test-repo'
    $env:GITHUB_SHA = $global:copilotChatReleaseTest.WorkflowCommit
}

function Assert-NoPublicMutation {
    $mutations = @($global:copilotChatReleaseTest.Calls | Where-Object { ($_ -join ' ') -match '^release (create|upload|edit) ' })
    Assert-True ($mutations.Count -eq 0) 'Rejected release must not be created, uploaded, or published.'
}

$fixtureRoot = Join-Path ([IO.Path]::GetTempPath()) ('copilotchat-release-tests-' + [guid]::NewGuid().ToString('N'))
$savedRepository = $env:GITHUB_REPOSITORY
$savedSHA = $env:GITHUB_SHA
$savedExitCode = Get-Variable LASTEXITCODE -Scope Global -ErrorAction SilentlyContinue
try {
    New-Item -ItemType Directory -Path (Join-Path $fixtureRoot 'scripts'), (Join-Path $fixtureRoot 'release'), (Join-Path $fixtureRoot 'internal/desktop'), (Join-Path $fixtureRoot 'internal/copilot') -Force | Out-Null
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'release-common.ps1'), (Join-Path $PSScriptRoot 'publish-release.ps1') -Destination (Join-Path $fixtureRoot 'scripts')
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'verify-update') -Destination (Join-Path $fixtureRoot 'scripts/verify-update') -Recurse
    . (Join-Path $fixtureRoot 'scripts/release-common.ps1')
    $publisher = Join-Path $fixtureRoot 'scripts/publish-release.ps1'
    # Current workflow metadata deliberately differs from the release tag.
    Set-Content -LiteralPath (Join-Path $fixtureRoot 'mygo.json') -Value '{"version":"9.9.9"}'
    Set-Content -LiteralPath (Join-Path $fixtureRoot 'internal/desktop/app.go') -Value 'const version = "9.9.9"'
    Set-Content -LiteralPath (Join-Path $fixtureRoot 'internal/copilot/client.go') -Value 'const userAgent = "CopilotChatNative/9.9.9"'
    foreach ($name in Get-ReleaseAssetNames '0.3.0') {
        Set-Content -LiteralPath (Join-Path $fixtureRoot "release/$name") -Value "fixture for $name"
    }

    Reset-TestState
    & $publisher -Tag v0.3.0
    Assert-True (-not $global:copilotChatReleaseTest.Release.draft) 'A complete new draft must publish.'
    Assert-True ($global:copilotChatReleaseTest.Release.assets.Count -eq 9) 'Eight packages and the checksum must upload.'
    $metadataCalls = @($global:copilotChatReleaseTest.Calls | Where-Object { ($_ -join ' ') -eq 'api repos/test-owner/test-repo/releases/414008935' })
    Assert-True ($metadataCalls.Count -eq 1) 'A new draft must be verified using its numeric REST ID.'
    Write-Host 'PASS: create draft, read tagged metadata, verify all assets by release ID, publish'

    Reset-TestState
    $global:copilotChatReleaseTest.Release = New-TestRelease
    & $publisher -Tag v0.3.0
    Assert-True (-not $global:copilotChatReleaseTest.Release.draft) 'An existing valid draft must publish.'
    $creates = @($global:copilotChatReleaseTest.Calls | Where-Object { ($_ -join ' ') -like 'release create *' })
    Assert-True ($creates.Count -eq 0) 'Retry must reuse the existing draft.'
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
        Set-Content -LiteralPath internal/desktop/app.go -Value 'const version = "0.3.0"'
        Set-Content -LiteralPath internal/copilot/client.go -Value 'const userAgent = "CopilotChatNative/0.3.0"'
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
} finally {
    $env:GITHUB_REPOSITORY = $savedRepository
    $env:GITHUB_SHA = $savedSHA
    if ($null -ne $savedExitCode) { $global:LASTEXITCODE = $savedExitCode.Value }
    # Only delete this suite's uniquely named directory inside the OS temp root.
    $resolvedRoot = [IO.Path]::GetFullPath($fixtureRoot)
    $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    if (-not $resolvedRoot.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase) -or (Split-Path -Leaf $resolvedRoot) -notmatch '^copilotchat-release-tests-[a-f0-9]{32}$') {
        throw 'Refusing to remove a test directory outside the expected temporary path.'
    }
    if (Test-Path -LiteralPath $resolvedRoot) { Remove-Item -LiteralPath $resolvedRoot -Recurse -Force }
}
