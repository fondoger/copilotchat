# Building and releasing

The source repository is [fondoger/copilotchat](https://github.com/fondoger/copilotchat). Download published packages from [GitHub Releases](https://github.com/fondoger/copilotchat/releases).

## Requirements

- Go 1.27.3 or newer, as declared by `go.mod`. Go's automatic toolchain selection can download the required version.
- PowerShell 7 (`pwsh`) for the shared packaging script on Windows and macOS.
- A host matching the target operating system and architecture for the complete test and package process. MyGo uses a pure-Go native backend; no Node.js runtime is needed by the application.
- Network access for Go modules and any packaging tools that MyGo needs to download. Windows packaging uses NSIS; macOS packaging uses the system disk-image and signing tools.

## Development

```sh
git clone https://github.com/fondoger/copilotchat.git
cd copilotchat
go run .
go test ./...
go vet ./...
```

`go run . --data-dir <directory>` selects a separate settings and conversation directory. It still uses the same application namespace in the system credential store.

Plain `go run` and `go build` report version `debug`; MyGo builds obtain the version from `mygo.json` automatically. Release builds generate that version in the Actions workspace.

At startup, the app appends standard application logs to `logs/app.log` under its data directory. On Windows, the default is `%APPDATA%\Copilot Chat\logs\app.log`; isolated development instances use `%APPDATA%\Copilot Chat Dev\logs\app.log`. `--data-dir` also changes the log location.

On Windows, with GNU Make and PowerShell 7 installed, replace an installer-installed dev or stable version with a local dev build:

```bash
make dev
```

This builds a native `-dev.local.<timestamp>` version under `.local/dev/`, stops processes running the installed executable, replaces it without a backup, and starts it again. The app keeps its existing settings, accounts, and Gateway port; MCP diagnostics go to `logs/app.log` in the application data directory. A failed build leaves the running app untouched. The source `mygo.json` stays unchanged.

Network protocol tests use local mock servers and synthetic credentials. UI tests use MyGo's software renderer and temporary settings, so they do not require a Copilot account or modify normal application data.

## Package a target

Run the corresponding command on a matching host:

| Target | Command | Release filename stem |
| --- | --- | --- |
| Windows x64 | `pwsh ./scripts/package.ps1 -Platform windows/amd64` | `copilotchat-<version>-windows-x64` |
| Windows ARM64 | `pwsh ./scripts/package.ps1 -Platform windows/arm64` | `copilotchat-<version>-windows-arm64` |
| macOS Intel | `pwsh ./scripts/package.ps1 -Platform darwin/amd64` | `copilotchat-<version>-macos-x64` |
| macOS Apple Silicon | `pwsh ./scripts/package.ps1 -Platform darwin/arm64` | `copilotchat-<version>-macos-arm64` |

The script runs tests and `go vet`, then invokes `go tool mygo build`. MyGo's intermediate packages are under `dist/`; the normalized release files are under `release/`. Windows produces a portable ZIP and a `-setup.exe` installer. macOS produces an application ZIP and a DMG. Packages include third-party license texts.

The application hosts its local forwarding service in-process; no separate credential executable is built or bundled. For a plain Windows development build, use `scripts/build.ps1`; see [LLM Gateway](agents.md).

Windows packages are unsigned. macOS packages use ad-hoc signing and are not notarized by Apple. No signing certificate or Apple account is required for this workflow. These packages may require an explicit first-open confirmation in the operating system.

### Signed update assets

Starting with v0.3.1, releases also contain a signed MyGo update archive and manifest for each target:

| Target | Update archive | Update manifest |
| --- | --- | --- |
| Windows x64 | `copilot-chat-<version>-windows-amd64.tar.gz` | `update-windows-amd64.json` |
| Windows ARM64 | `copilot-chat-<version>-windows-arm64.tar.gz` | `update-windows-arm64.json` |
| macOS Intel | `copilot-chat-<version>-darwin-amd64.tar.gz` | `update-darwin-amd64.json` |
| macOS Apple Silicon | `copilot-chat-<version>-darwin-arm64.tar.gz` | `update-darwin-arm64.json` |

These are separate from the ZIP, setup executable, and DMG used for manual installation. MyGo embeds the public key and platform feed from `mygo.json` into the application. It downloads the appropriate manifest from the latest GitHub release, verifies the archive's Ed25519 signature over its SHA-256 digest, and replaces the installed application. Updates require a writable installation directory; a macOS app must first be copied out of its disk image. Delta updates are disabled (`updates.deltas: 0`).

**v0.3.0 does not contain an updater. Install v0.3.1 or later manually once to enable future in-app updates.**

For a local signed package, load the private key from its protected location outside the repository and require signed output:

```powershell
$env:MYGO_UPDATER_PRIVATE_KEY = Get-Content -LiteralPath '<private-key-file>' -Raw
try {
    ./scripts/package.ps1 -Platform windows/amd64 -RequireSignedUpdates
} finally {
    Remove-Item Env:MYGO_UPDATER_PRIVATE_KEY
}
```

The private key must match `updates.publicKey`. Keep a secure backup outside the repository: existing installations trust that key, so losing it prevents signing future compatible updates. GitHub Actions stores it as the repository secret `MYGO_UPDATER_PRIVATE_KEY`; release builds receive it. Pull-request checks build and test installation packages without this secret. Release packaging fails when signed updates are required and the key or generated assets are missing.

Ed25519 update signing is separate from Windows Authenticode and Apple Developer ID signing/notarization. It authenticates downloaded updates against the key already in the app; it does not remove operating-system warnings when initially installing these packages.

Windows x64 has been exercised in a native desktop session. Windows ARM64 and both macOS architectures have not yet received physical-device runtime testing. Successful builds and automated tests do not verify login dialogs, clipboard behavior, tray/menu-bar integration, rendering, or HTML previews on those systems.

## Continuous integration

[GitHub Actions](https://github.com/fondoger/copilotchat/actions) publishes a dev prerelease for each push to `main`. Native tests and package builds run in parallel; publishing waits for all checks and packages. Main pushes use this release workflow instead of a separate CI run. Pull requests retain CI checks.

Versions are generated automatically. After stable `0.5.10`, dev builds use `0.5.11-dev.<workflow-run-number>`; the next stable release uses `0.5.11`. The workflow writes the version and matching update notes into its workspace, and MyGo embeds the version in the application and request headers. Saved workflow metadata keeps retries on the same version. Version changes do not create commits.

Dev publishes Windows x64, Windows ARM64, and macOS ARM64. Stable also publishes macOS Intel. Dev releases are marked as GitHub prereleases and leave the latest stable release unchanged. To publish the next stable version:

```bash
gh workflow run release.yml --ref main -f channel=stable --repo fondoger/copilotchat
```

The publisher checks the channel's complete package set, signed update manifests and archives, uploaded sizes, and hashes before making the release public. The release tag is created only after build and test success. A failed build remains visible in Actions.

### Update channels

Settings → General → Updates offers Stable and Dev. Stable receives newer official releases; Dev also receives newer prereleases, using MyGo's `SetPrereleases` API. Dev is unavailable on macOS Intel. Debug builds cannot update themselves.

Selecting another channel downloads and installs a newer matching update, then restarts the app. The new process confirms the channel after starting the expected version. Failed updates and requests with no newer version leave the current channel unchanged. Existing unsent-draft and active-response protections can postpone restarting.

The release-publisher regression suite runs without GitHub credentials or live API calls. Run `pwsh ./scripts/test-release.ps1` and `go test ./scripts/verify-update` to check release retries, channel-specific assets, signed manifests, and altered archives.

### Retry a failed release

For a run whose tag has not been published, rerun it in Actions; the saved release metadata preserves its version. To retry an existing tag using publishing fixes from `main`:

```bash
gh workflow run release.yml --ref main -f tag=v0.5.11 --repo fondoger/copilotchat
```

The tag input also accepts dev tags such as `v0.5.11-dev.123`. Builds use the tagged source; publishing uses the current workflow's scripts. A matching draft can be resumed. Published releases and existing tag targets are never overwritten.

## Verify a download

Download the package and `SHA256SUMS.txt` from the same release. Compare the computed hash with the entry for that filename.

```powershell
Get-FileHash ./copilotchat-0.5.1-windows-x64.zip -Algorithm SHA256
```

On macOS:

```sh
shasum -a 256 copilotchat-0.5.1-macos-arm64.zip
```

## Documentation screenshots

The committed screenshots use synthetic fixtures. Regenerate them with:

```powershell
$env:COPILOT_CHAT_SCREENSHOTS = (Join-Path $PWD 'docs/screenshots')
go test ./internal/desktop -run '^(TestMainLayoutAndSnapshots|TestSettingsScreenshots)$' -count=1
```

Inspect generated images before committing them. The screenshot directory allowlists the public examples; other local captures remain ignored.
