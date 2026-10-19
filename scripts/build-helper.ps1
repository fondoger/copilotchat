#requires -Version 7.0
param(
    [Parameter(Mandatory)][ValidateSet('windows/amd64', 'windows/arm64', 'darwin/amd64', 'darwin/arm64')][string]$Platform,
    [string]$OutputDirectory = ''
)
$ErrorActionPreference = 'Stop'
$previousOS, $previousArch, $previousCgo = $env:GOOS, $env:GOARCH, $env:CGO_ENABLED
Push-Location (Split-Path -Parent $PSScriptRoot)
try {
    $goos, $goarch = $Platform.Split('/')
    $env:GOOS, $env:GOARCH, $env:CGO_ENABLED = $goos, $goarch, '0'
    if (-not $OutputDirectory) { $OutputDirectory = "resources/$goos-$goarch" }
    New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null
    $name = 'copilot-chat-api-key-helper'
    if ($goos -eq 'windows') { $name += '.exe' }
    $outputPath = Join-Path $OutputDirectory $name
    # Keep the console subsystem: callers capture stdout, and errors use stderr.
    go build -trimpath -ldflags '-s -w' -o $outputPath ./cmd/copilot-chat-api-key-helper
    if ($LASTEXITCODE -ne 0) { throw 'Credential helper build failed.' }
    Write-Output "Built credential helper for $Platform."
} finally {
    $env:GOOS, $env:GOARCH, $env:CGO_ENABLED = $previousOS, $previousArch, $previousCgo
    Pop-Location
}
