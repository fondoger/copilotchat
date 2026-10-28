#requires -Version 7.0
. (Join-Path $PSScriptRoot 'release-common.ps1')
if (-not $IsWindows) { throw 'make dev replaces the installed Windows application and requires Windows.' }

$projectRoot = Split-Path -Parent $PSScriptRoot
$config = Get-Content -LiteralPath (Join-Path $projectRoot 'mygo.json') -Raw | ConvertFrom-Json -AsHashtable
$executableName = "$($config.name).exe"
$installed = $null
foreach ($root in @(
    'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall',
    'HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall',
    'HKLM:\Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall'
)) {
    $entry = Get-ItemProperty -LiteralPath "$root\$($config.identifier)" -ErrorAction SilentlyContinue
    if ($entry -and $entry.PSObject.Properties['InstallLocation'] -and $entry.InstallLocation) {
        $candidate = Join-Path $entry.InstallLocation $executableName
        if (Test-Path -LiteralPath $candidate -PathType Leaf) {
            $installed = [IO.Path]::GetFullPath($candidate)
            break
        }
    }
}
if (-not $installed) { throw 'Copilot Chat is not installed. Install a dev or stable release first.' }

$stamp = Get-Date -Format 'yyyyMMddHHmmssfff'
$config.version = "$($config.version -replace '-.*$', '')-dev.local.$stamp"
$buildRoot = Join-Path $projectRoot ".local/dev/$stamp"
New-Item -ItemType Directory -Path $buildRoot -Force | Out-Null
$config.main = Join-Path $projectRoot $(if ($config['main']) { $config['main'] } else { '.' })
$config.out = 'dist'
$config.icon = Join-Path $projectRoot $(if ($config['icon']) { $config['icon'] } else { 'resources/icon.png' })
$config.resources = @($config.resources | ForEach-Object { Join-Path $projectRoot $_ })
$config | ConvertTo-Json -Depth 20 | Set-Content -LiteralPath (Join-Path $buildRoot 'mygo.json') -Encoding utf8NoBOM

$previousCgo, $previousSigningKey = $env:CGO_ENABLED, $env:MYGO_UPDATER_PRIVATE_KEY
Push-Location $projectRoot
try {
    $architecture = & go env GOHOSTARCH
    if ($LASTEXITCODE -ne 0 -or $architecture -notin @('amd64', 'arm64')) { throw 'Cannot determine a supported native Windows architecture.' }
    $env:CGO_ENABLED = '0'
    $env:MYGO_UPDATER_PRIVATE_KEY = $null
    Write-Output "Building $($config.version) for windows/$architecture..."
    Invoke-CheckedCommand -Command go -Arguments @('tool', 'mygo', 'build', '-platform', "windows/$architecture", $buildRoot)
} finally {
    $env:CGO_ENABLED = $previousCgo
    $env:MYGO_UPDATER_PRIVATE_KEY = $previousSigningKey
    Pop-Location
}

$built = Join-Path $buildRoot "dist/windows-$architecture/$executableName"
if (-not (Test-Path -LiteralPath $built -PathType Leaf)) { throw "Build did not produce $built" }
$pending = Join-Path (Split-Path -Parent $installed) ".copilot-chat-dev-$PID-$stamp.exe"
try {
    # Stage beside the installed executable before stopping it, then replace it atomically.
    Copy-Item -LiteralPath $built -Destination $pending
    $filter = "Name='$($executableName.Replace("'", "''"))'"
    for ($attempt = 0; ; $attempt++) {
        $running = @(Get-CimInstance Win32_Process -Filter $filter | Where-Object { $_.ExecutablePath -eq $installed })
        if ($running.Count -gt 0) {
            $processIds = @($running | ForEach-Object ProcessId)
            Stop-Process -Id $processIds -Force -ErrorAction SilentlyContinue
            Wait-Process -Id $processIds -Timeout 10 -ErrorAction SilentlyContinue
        }
        try {
            [IO.File]::Move($pending, $installed, $true)
            break
        } catch [IO.IOException] {
            if ($attempt -ge 19) { throw }
            Start-Sleep -Milliseconds 250
        }
    }
} finally {
    if (Test-Path -LiteralPath $pending) { Remove-Item -LiteralPath $pending -Force }
}

$started = Start-Process -FilePath $installed -WorkingDirectory (Split-Path -Parent $installed) -PassThru
Write-Output "Started $installed ($($config.version), PID $($started.Id))."
