# Building and releasing

The source repository is [fondoger/copilotchat](https://github.com/fondoger/copilotchat). Download published packages from [GitHub Releases](https://github.com/fondoger/copilotchat/releases).

## Requirements

- Go 1.27.1 or newer, as declared by `go.mod`. Go's automatic toolchain selection can download the required version.
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

Network protocol tests use local mock servers and synthetic credentials. UI tests use MyGo's software renderer and temporary settings, so they do not require a Copilot account or modify normal application data.

## Package a target

Run the corresponding command on a matching host:

| Target | Command | Release filename stem |
| --- | --- | --- |
| Windows x64 | `pwsh ./scripts/package.ps1 -Platform windows/amd64` | `copilotchat-<version>-windows-x64` |
| Windows ARM64 | `pwsh ./scripts/package.ps1 -Platform windows/arm64` | `copilotchat-<version>-windows-arm64` |
| macOS Intel | `pwsh ./scripts/package.ps1 -Platform darwin/amd64` | `copilotchat-<version>-macos-x64` |
| macOS Apple Silicon | `pwsh ./scripts/package.ps1 -Platform darwin/arm64` | `copilotchat-<version>-macos-arm64` |

The script runs tests and `go vet`, then invokes `go tool mygo build`. MyGo's intermediate packages are under `dist/`; the normalized release files are under `release/`. Windows produces a portable ZIP and a `-setup.exe` installer. macOS produces an application ZIP and a DMG. Packages include third-party notices and license texts.

Windows packages are unsigned. macOS packages use ad-hoc signing and are not notarized by Apple. No signing certificate or Apple account is required for this workflow. These packages may require an explicit first-open confirmation in the operating system.

Windows x64 has been exercised in a native desktop session. Windows ARM64 and both macOS architectures have not yet received physical-device runtime testing. Successful builds and automated tests do not verify login dialogs, clipboard behavior, tray/menu-bar integration, rendering, or HTML previews on those systems.

## Continuous integration

[GitHub Actions](https://github.com/fondoger/copilotchat/actions) runs the test, vet, and package pipeline for four native runner targets on pushes to `main`, pull requests, and manual dispatches. The release workflow uses the same target matrix for version tags.

To publish a release, keep the application version in `internal/desktop/app.go`, the request User-Agent in `internal/copilot/client.go`, and `mygo.json` consistent. Update any versioned download references in the README, then commit the release source before creating the tag:

```sh
git tag v0.3.0
git push origin v0.3.0
```

The `v*` tag workflow builds and checks all four targets, gathers the eight package files, and creates `SHA256SUMS.txt`. It uploads a draft release and publishes only after checking that the complete asset set is present. If a build or check fails, inspect the workflow run before retrying; do not publish a partial asset set.

### Retry a failed release

Keep the existing release tag unchanged. After a workflow or publishing-script fix has landed on `main`, dispatch the current **Release** workflow with the existing tag. The required `tag` input has no default and must have the form `vMAJOR.MINOR.PATCH`:

```sh
gh workflow run release.yml --ref main -f tag=v0.3.0 --repo fondoger/copilotchat
```

The same action is available through **Actions → Release → Run workflow**: select `main` and enter the existing tag. Use this new dispatch to pick up the repaired workflow; rerunning the original failed run uses its original workflow revision.

Validation and all four builds check out the requested tag, so the application source remains the tagged release source. Publishing uses the scripts from the workflow run's `main` revision, while reading the version and target commit from the existing tag. It can resume a matching draft and replace its draft assets, then verifies the full asset list, sizes, and SHA-256 hashes before publishing. A draft targeting a different commit is rejected. An already published release is never overwritten; any application changes require a new version and tag.

## Verify a download

Download the package and `SHA256SUMS.txt` from the same release. Compare the computed hash with the entry for that filename.

```powershell
Get-FileHash ./copilotchat-0.3.0-windows-x64.zip -Algorithm SHA256
```

On macOS:

```sh
shasum -a 256 copilotchat-0.3.0-macos-arm64.zip
```

## Documentation screenshots

The committed screenshots use synthetic fixtures. Regenerate them with:

```powershell
$env:COPILOT_CHAT_SCREENSHOTS = (Join-Path $PWD 'docs/screenshots')
go test ./internal/desktop -run '^(TestMainLayoutAndSnapshots|TestSettingsScreenshots)$' -count=1
```

Inspect generated images before committing them. The screenshot directory allowlists the public examples; other local captures remain ignored.
