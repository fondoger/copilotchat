$ErrorActionPreference = 'Stop'
Push-Location (Split-Path -Parent $PSScriptRoot)
try {
    go test ./...
    if ($LASTEXITCODE -ne 0) { throw 'Tests failed.' }
    New-Item -ItemType Directory -Force -Path bin | Out-Null
    go build -trimpath -ldflags '-H windowsgui -s -w' -o bin/copilot-chat.exe .
    if ($LASTEXITCODE -ne 0) { throw 'Build failed.' }
    Write-Output "Built $(Join-Path (Get-Location) 'bin/copilot-chat.exe')"
} finally { Pop-Location }
