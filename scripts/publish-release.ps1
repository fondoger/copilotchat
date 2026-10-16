#requires -Version 7.0
param([Parameter(Mandatory)][string]$Tag)
. (Join-Path $PSScriptRoot 'release-common.ps1')
Push-Location (Split-Path -Parent $PSScriptRoot)
try {
    # A manual retry may run repaired publishing scripts from a newer commit.
    # Package names and versions always come from the immutable release tag.
    $config = Get-ReleaseConfig -ExpectedTag $Tag -FromTag
    $tagCommit = Resolve-ReleaseTagCommit $Tag
    if (-not $env:GITHUB_REPOSITORY) { throw 'Publishing requires the GitHub Actions repository context.' }
    $repository = $env:GITHUB_REPOSITORY
    $expected = @(Get-ReleaseAssetNames -Version $config.version -Config $config | Sort-Object)
    $actual = @(Get-ChildItem -LiteralPath release -File | Where-Object Name -ne 'SHA256SUMS.txt' | ForEach-Object Name | Sort-Object)
    if (Compare-Object $expected $actual) {
        throw "Refusing an incomplete release. Expected: $($expected -join ', '). Found: $($actual -join ', ')."
    }
    $updateAssets = @(Get-ReleaseUpdateAssets $config)
    $updateReleaseNotes = ''
    if ($updateAssets.Count -gt 0) {
        if ($config.updates.github -cne $repository) { throw 'The update repository does not match the release repository.' }
        Assert-ReleaseUpdateArtifacts -Config $config -Directory (Join-Path (Get-Location) 'release')
        $firstManifest = Get-Content -LiteralPath (Join-Path 'release' $updateAssets[0].Manifest) -Raw | ConvertFrom-Json
        $manifestNotes = $firstManifest.PSObject.Properties['notes']
        if ($manifestNotes -and $manifestNotes.Value -is [string]) { $updateReleaseNotes = $manifestNotes.Value.Trim() }
    }
    $hashes = @{}
    $sizes = @{}
    $checksumLines = foreach ($name in $expected) {
        $file = Join-Path release $name
        $hashes[$name] = (Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash.ToLowerInvariant()
        $sizes[$name] = (Get-Item -LiteralPath $file).Length
        if ($sizes[$name] -le 0) { throw "Empty release asset: $name" }
        "$($hashes[$name])  $name"
    }
    [IO.File]::WriteAllLines((Join-Path (Get-Location) 'release/SHA256SUMS.txt'), $checksumLines, [Text.UTF8Encoding]::new($false))
    $names = @($expected) + 'SHA256SUMS.txt'
    $hashes['SHA256SUMS.txt'] = (Get-FileHash -LiteralPath 'release/SHA256SUMS.txt').Hash.ToLowerInvariant()
    $sizes['SHA256SUMS.txt'] = (Get-Item -LiteralPath 'release/SHA256SUMS.txt').Length

    # Fetch release metadata before creating anything: network/auth failures
    # must not be mistaken for an absent release. Listing includes drafts.
    $existing = Get-GitHubRelease $repository $Tag
    if ($existing -and -not $existing.draft) {
        throw "Release $Tag is already public. Published releases are not overwritten."
    }

    New-Item -ItemType Directory -Path dist -Force | Out-Null
    $notesPath = Join-Path (Get-Location) 'dist/release-notes.md'
    $notes = @"
Copilot Chat $Tag is a native Go/MyGo desktop client for GitHub Copilot, with streamed chat, model selection, attachments, local conversation storage, HTML previews, configurable MCP servers, and optional tray residence.

| Platform | Architecture | Downloads |
| --- | --- | --- |
| Windows | Intel/AMD x64 | ``copilotchat-$($config.version)-windows-x64-setup.exe`` or ``copilotchat-$($config.version)-windows-x64.zip`` |
| Windows | ARM64 | ``copilotchat-$($config.version)-windows-arm64-setup.exe`` or ``copilotchat-$($config.version)-windows-arm64.zip`` |
| macOS 12+ | Intel x64 | ``copilotchat-$($config.version)-macos-x64.dmg`` or ``copilotchat-$($config.version)-macos-x64.zip`` |
| macOS 12+ | Apple silicon | ``copilotchat-$($config.version)-macos-arm64.dmg`` or ``copilotchat-$($config.version)-macos-arm64.zip`` |

Windows packages are unsigned. macOS apps are ad-hoc signed, without Developer ID signing or notarization. Operating-system security prompts may require explicit approval. The four native CI targets run automated tests and packaging checks; this does not claim interactive testing on a physical Mac. See the README for installation and troubleshooting. Verify downloads against ``SHA256SUMS.txt``. All packages include third-party notices and license texts.
"@
    if ($updateReleaseNotes) {
        $notes += "`n## Changes`n`n$updateReleaseNotes`n"
    }
    if ($updateAssets.Count -gt 0) {
        $notes += "`nThe four ``update-*.json`` manifests and signed ``.tar.gz`` archives support in-app updates. Their Ed25519 signatures are verified against the public key built into the app; this is separate from operating-system code signing.`n"
    }
    [IO.File]::WriteAllText($notesPath, $notes, [Text.UTF8Encoding]::new($false))
    if (-not $existing) {
        # Keep the former --verify-tag behavior: never let release creation
        # manufacture a missing remote tag. The local tag was resolved above.
        Invoke-CheckedCommand -Command gh -Arguments @('api', "repos/$repository/git/ref/tags/$Tag", '--silent')
        $requestPath = Join-Path (Get-Location) 'dist/create-release.json'
        $request = @{
            tag_name = $Tag
            target_commitish = $tagCommit
            name = "Copilot Chat $Tag"
            body = $notes
            draft = $true
            generate_release_notes = $true
        } | ConvertTo-Json -Depth 5
        [IO.File]::WriteAllText($requestPath, $request, [Text.UTF8Encoding]::new($false))
        # A freshly created draft can be absent from release listings briefly.
        # The POST response already contains the authoritative numeric REST ID.
        $createdJSON = & gh api --method POST "repos/$repository/releases" --input $requestPath
        if ($LASTEXITCODE -ne 0) { throw 'Could not create the draft release.' }
        $existing = $createdJSON | ConvertFrom-Json
    }
    if (-not $existing.draft -or $existing.target_commitish -cne $tagCommit -or $existing.tag_name -cne $Tag) {
        throw 'The draft has a different tag or targets a different commit; inspect it before retrying.'
    }
    $releaseID = 0L
    if (-not [long]::TryParse([string]$existing.id, [ref]$releaseID) -or $releaseID -le 0) {
        throw 'The draft did not provide a valid numeric REST release ID.'
    }
    $paths = @($names | ForEach-Object { Join-Path release $_ })
    Invoke-CheckedCommand -Command gh -Arguments (@('release', 'upload', $Tag, '--repo', $repository, '--clobber') + $paths)

    $metadataJSON = & gh api "repos/$repository/releases/$releaseID"
    if ($LASTEXITCODE -ne 0) { throw 'Could not verify the uploaded release.' }
    $metadata = $metadataJSON | ConvertFrom-Json
    if (-not $metadata.draft -or $metadata.tag_name -cne $Tag -or $metadata.target_commitish -cne $tagCommit) {
        throw 'The draft tag or target changed before asset verification.'
    }
    if (Compare-Object @($names | Sort-Object) @($metadata.assets | ForEach-Object name | Sort-Object)) {
        throw 'The draft does not contain exactly the required packages, update assets, and checksum file.'
    }
    foreach ($asset in $metadata.assets) {
        if ($asset.state -ne 'uploaded' -or $asset.size -ne $sizes[$asset.name]) { throw "Upload verification failed for $($asset.name)." }
        # GitHub currently returns an SHA-256 digest on uploaded assets.
        # If an API deployment omits it, verify the bytes via a download.
        $digest = $asset.PSObject.Properties['digest']
        if ($digest -and $digest.Value) {
            if ($digest.Value -cne "sha256:$($hashes[$asset.name])") { throw "Upload digest differs for $($asset.name)." }
        } else {
            $verificationDir = Join-Path (Get-Location) 'dist/verify-release'
            New-Item -ItemType Directory -Path $verificationDir -Force | Out-Null
            Invoke-CheckedCommand -Command gh -Arguments @('release', 'download', $Tag, '--repo', $repository, '--pattern', $asset.name, '--dir', $verificationDir, '--clobber')
            if ((Get-FileHash -LiteralPath (Join-Path $verificationDir $asset.name)).Hash.ToLowerInvariant() -cne $hashes[$asset.name]) {
                throw "Downloaded asset differs for $($asset.name)."
            }
        }
    }
    # This is the only public-state transition. Any earlier failure leaves a
    # recoverable draft, never a public release with a subset of the platforms.
    Invoke-CheckedCommand -Command gh -Arguments @('release', 'edit', $Tag, '--repo', $repository, '--draft=false', '--latest')
} finally { Pop-Location }
