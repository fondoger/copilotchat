#requires -Version 7.0
param([Parameter(Mandatory)][string]$Tag)
. (Join-Path $PSScriptRoot 'release-common.ps1')
Push-Location (Split-Path -Parent $PSScriptRoot)
try {
    $config = Get-ReleaseConfig -ExpectedTag $Tag
    if (-not $env:GITHUB_REPOSITORY -or -not $env:GITHUB_SHA) { throw 'Publishing requires the GitHub Actions repository and commit context.' }
    $repository = $env:GITHUB_REPOSITORY
    $expected = @(Get-ReleaseAssetNames $config.version | Sort-Object)
    $actual = @(Get-ChildItem -LiteralPath release -File | Where-Object Name -ne 'SHA256SUMS.txt' | ForEach-Object Name | Sort-Object)
    if (Compare-Object $expected $actual) {
        throw "Refusing an incomplete release. Expected: $($expected -join ', '). Found: $($actual -join ', ')."
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
    $releasesJSON = & gh api --paginate --slurp "repos/$repository/releases?per_page=100"
    if ($LASTEXITCODE -ne 0) { throw 'Could not query existing releases.' }
    $existing = @($releasesJSON | ConvertFrom-Json | ForEach-Object { $_ } | Where-Object tag_name -CEQ $Tag)
    if ($existing.Count -gt 1) { throw "More than one release exists for $Tag." }
    if ($existing.Count -eq 1 -and -not $existing[0].draft) {
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
    [IO.File]::WriteAllText($notesPath, $notes, [Text.UTF8Encoding]::new($false))
    if ($existing.Count -eq 0) {
        Invoke-CheckedCommand -Command gh -Arguments @('release', 'create', $Tag, '--repo', $repository, '--verify-tag', '--target', $env:GITHUB_SHA, '--draft', '--title', "Copilot Chat $Tag", '--notes-file', $notesPath, '--generate-notes')
    } elseif ($existing[0].target_commitish -cne $env:GITHUB_SHA) {
        throw 'The existing draft targets a different commit; inspect it before retrying.'
    }
    $paths = @($names | ForEach-Object { Join-Path release $_ })
    Invoke-CheckedCommand -Command gh -Arguments (@('release', 'upload', $Tag, '--repo', $repository, '--clobber') + $paths)

    $metadataJSON = & gh api "repos/$repository/releases/tags/$Tag"
    if ($LASTEXITCODE -ne 0) { throw 'Could not verify the uploaded release.' }
    $metadata = $metadataJSON | ConvertFrom-Json
    if (-not $metadata.draft) { throw 'The release must remain a draft until all assets are verified.' }
    if (Compare-Object @($names | Sort-Object) @($metadata.assets | ForEach-Object name | Sort-Object)) {
        throw 'The draft does not contain exactly the eight packages and checksum file.'
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
