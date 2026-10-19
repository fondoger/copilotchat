#requires -Version 7.0
param(
    [Parameter(Mandatory)]
    [ValidateSet('windows/amd64', 'windows/arm64', 'darwin/amd64', 'darwin/arm64')]
    [string]$Platform,
    [string]$ExpectedTag = '',
    [switch]$RequireSignedUpdates,
    [switch]$SkipTests
)

. (Join-Path $PSScriptRoot 'release-common.ps1')
$projectRoot = Split-Path -Parent $PSScriptRoot
$previousCgo = $env:CGO_ENABLED
Push-Location $projectRoot
try {
    $config = Get-ReleaseConfig -ExpectedTag $ExpectedTag
    $updateAssets = @(Get-ReleaseUpdateAssets -Config $config -Targets @($Platform.Replace('/', '-')))
    if ($RequireSignedUpdates -and ($updateAssets.Count -ne 1 -or -not $env:MYGO_UPDATER_PRIVATE_KEY)) {
        throw 'Release packaging requires configured signed updates and MYGO_UPDATER_PRIVATE_KEY.'
    }
    $goos, $goarch = $Platform.Split('/')
    if (($goos -eq 'darwin' -and -not $IsMacOS) -or ($goos -eq 'windows' -and -not $IsWindows)) {
        throw "Packaging $Platform needs its native OS (macOS for codesign/DMG, Windows for NSIS)."
    }
    $env:CGO_ENABLED = '0'
    Invoke-CheckedCommand -Command go -Arguments @('mod', 'verify')
    if (-not $SkipTests) {
        Invoke-CheckedCommand -Command go -Arguments @('test', './...', '-count=1', '-timeout', '5m')
        Invoke-CheckedCommand -Command go -Arguments @('vet', './...')
        Invoke-CheckedCommand -Command go -Arguments @('tool', 'mygo', 'vet')
    }

    # Build the helper before MyGo collects resources, signs, and creates updates.
    ./scripts/build-helper.ps1 -Platform $Platform

    # MyGo includes resources/licenses automatically and the explicit resources
    # in mygo.json before making either installer. Do not add files after signing.
    foreach ($notice in @('README.md', 'THIRD_PARTY_NOTICES.md')) {
        if ($config.resources -notcontains $notice -or -not (Test-Path -LiteralPath $notice -PathType Leaf)) {
            throw "mygo.json must include the existing $notice in resources."
        }
    }
    Invoke-CheckedCommand -Command go -Arguments @('tool', 'mygo', 'build', '-platform', $Platform, '-o', 'dist/packages', '-sign', '-', '-skip-notarize')

    $packageRoot = Join-Path $projectRoot "dist/packages/$goos-$goarch"
    $releaseRoot = Join-Path $projectRoot 'release'
    New-Item -ItemType Directory -Path $releaseRoot -Force | Out-Null
    $assetOS = if ($goos -eq 'darwin') { 'macos' } else { 'windows' }
    $assetArch = if ($goarch -eq 'amd64') { 'x64' } else { 'arm64' }
    $prefix = "copilotchat-$($config.version)-$assetOS-$assetArch"
    $zipPath = Join-Path $releaseRoot "$prefix.zip"

    if ($goos -eq 'darwin') {
        $appPath = Join-Path $packageRoot "$($config.name).app"
        $executable = Join-Path $appPath "Contents/MacOS/$($config.name)"
        $resourceRoot = Join-Path $appPath 'Contents/Resources'
        $installerPath = Join-Path $packageRoot "$($config.name) $($config.version) $goarch.dmg"
        $installerAsset = Join-Path $releaseRoot "$prefix.dmg"
    } else {
        $executable = Join-Path $packageRoot "$($config.name).exe"
        $resourceRoot = $packageRoot
        $installerPath = Join-Path $packageRoot "$($config.name) Setup $($config.version) $goarch.exe"
        $installerAsset = Join-Path $releaseRoot "$prefix-setup.exe"
    }
    foreach ($path in @($executable, $installerPath)) {
        if (-not (Test-Path -LiteralPath $path -PathType Leaf) -or (Get-Item -LiteralPath $path).Length -eq 0) {
            throw "MyGo did not produce the required artifact: $path"
        }
    }
    $helperName = if ($goos -eq 'windows') { 'copilot-chat-api-key-helper.exe' } else { 'copilot-chat-api-key-helper' }
    $helperPath = Join-Path $resourceRoot $helperName
    if (-not (Test-Path -LiteralPath $helperPath -PathType Leaf)) { throw 'Packaged credential helper is missing.' }
    $helperInfo = & go version -m $helperPath
    if ($LASTEXITCODE -ne 0 -or ($helperInfo -join "`n") -notmatch "\bGOOS=$goos\b" -or ($helperInfo -join "`n") -notmatch "\bGOARCH=$goarch\b") {
        throw 'Credential helper architecture does not match the application.'
    }
    Invoke-CheckedCommand -Command $helperPath -Arguments @('--help')
    $buildInfo = & go version -m $executable
    if ($LASTEXITCODE -ne 0 -or ($buildInfo -join "`n") -notmatch "\bGOOS=$goos\b" -or ($buildInfo -join "`n") -notmatch "\bGOARCH=$goarch\b") {
        throw "Executable does not contain the expected $Platform build metadata."
    }
    foreach ($notice in @('README.md', 'THIRD_PARTY_NOTICES.md')) {
        $installedNotice = Join-Path $resourceRoot $notice
        if (-not (Test-Path -LiteralPath $installedNotice -PathType Leaf) -or
            (Get-FileHash -LiteralPath $installedNotice).Hash -ne (Get-FileHash -LiteralPath $notice).Hash) {
            throw "Packaged $notice is missing or differs from its source."
        }
    }
    $licenseFiles = @(Get-ChildItem -LiteralPath (Join-Path $resourceRoot 'licenses') -File -Recurse)
    if ($licenseFiles.Count -eq 0) { throw 'Third-party license texts were not packaged.' }

    # Archive the signed bundle with ditto so executable modes, symlinks and
    # Apple metadata survive. The Windows archive excludes its own installer.
    if (Test-Path -LiteralPath $zipPath) { Remove-Item -LiteralPath $zipPath -Force }
    if ($goos -eq 'darwin') {
        Invoke-CheckedCommand -Command codesign -Arguments @('--verify', '--deep', '--strict', $appPath)
        Invoke-CheckedCommand -Command hdiutil -Arguments @('verify', $installerPath)
        Invoke-CheckedCommand -Command ditto -Arguments @('-c', '-k', '--sequesterRsrc', '--keepParent', $appPath, $zipPath)
    } else {
        $portableFiles = @(Get-ChildItem -LiteralPath $packageRoot | Where-Object {
            $_.FullName -ne $installerPath -and $_.Name -notlike '*.tar.gz' -and
            $_.Name -notlike '*.delta' -and $_.Name -notlike 'update-*.json'
        } | ForEach-Object FullName)
        Compress-Archive -LiteralPath $portableFiles -DestinationPath $zipPath -CompressionLevel Optimal
    }
    Copy-Item -LiteralPath $installerPath -Destination $installerAsset -Force

    if ($updateAssets.Count -gt 0 -and ($RequireSignedUpdates -or $env:MYGO_UPDATER_PRIVATE_KEY)) {
        Assert-ReleaseUpdateArtifacts -Config $config -Directory $packageRoot -Targets @($Platform.Replace('/', '-'))
        foreach ($asset in $updateAssets) {
            Copy-Item -LiteralPath (Join-Path $packageRoot $asset.Archive), (Join-Path $packageRoot $asset.Manifest) -Destination $releaseRoot -Force
        }
    }

    $archive = [System.IO.Compression.ZipFile]::OpenRead($zipPath)
    try {
        $entries = @($archive.Entries | ForEach-Object { $_.FullName.Replace('\', '/') })
        $noticePrefix = if ($goos -eq 'darwin') { "$($config.name).app/Contents/Resources/" } else { '' }
        foreach ($notice in @('README.md', 'THIRD_PARTY_NOTICES.md')) {
            if ($entries -cnotcontains "$noticePrefix$notice") { throw "ZIP is missing $notice." }
        }
        if ($entries -cnotcontains "$noticePrefix$helperName") { throw 'ZIP is missing the credential helper.' }
        if (-not ($entries | Where-Object { $_.StartsWith("${noticePrefix}licenses/") -and -not $_.EndsWith('/') })) {
            throw 'ZIP is missing third-party licenses.'
        }
    } finally { $archive.Dispose() }
    Write-Output "Packaged $Platform`: $zipPath and $installerAsset"
} finally {
    if ($null -eq $previousCgo) { Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue } else { $env:CGO_ENABLED = $previousCgo }
    Pop-Location
}
