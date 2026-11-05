# Build and publish

## Publish stable

On `main`, with changes committed:

```bash
make publish-stable
```

Requires Git, GNU Make, and GitHub CLI with the `fondoger` account logged in. The command switches to that account, pulls with rebase, pushes, and starts the stable release workflow. Any failure stops the sequence.

Versions, tags, and update notes are generated in GitHub Actions. Follow the run in [Actions](https://github.com/fondoger/copilotchat/actions); download packages from [Releases](https://github.com/fondoger/copilotchat/releases).

## Development

Use the Go version declared in `go.mod`. Scripts require PowerShell 7 (`pwsh`).

```bash
go run .
```

Starts an isolated debug instance. `--data-dir` selects its data directory. Logs are written to `logs/app.log` under that directory; on Windows, the default directories are `%APPDATA%\Copilot Chat` and `%APPDATA%\Copilot Chat Dev`.

To replace and restart an installed version on Windows:

```bash
make dev
```

This keeps the installed app's data, accounts, and Gateway port, and replaces its executable without a backup. A failed build leaves the running app untouched.

Run checks locally:

```bash
go test ./...
```

```bash
go vet ./...
```

## Local packages

Build on a host matching the target OS and architecture:

```bash
pwsh ./scripts/package.ps1 -Platform windows/amd64
```

Other targets: `windows/arm64`, `darwin/amd64`, `darwin/arm64`. Packages go to `release/`; intermediate files go to `dist/`. Windows packages are unsigned; macOS packages use ad-hoc signing and are not notarized.

## Release behavior and troubleshooting

- Main code pushes automatically publish dev prereleases. Stable includes macOS Intel; dev omits it. Build and test jobs run in parallel, and publishing waits for both.
- Settings → General → Updates selects Stable or Dev. A channel switch takes effect after the newer version is installed and restarted. Debug builds cannot self-update.
- Update signing uses the `MYGO_UPDATER_PRIVATE_KEY` repository secret, matching `updates.publicKey` in `mygo.json`. Back up the private key outside the repository; installed apps trust that key.
- Manual downloads can be checked against the release's `SHA256SUMS.txt`.

If publishing fails, read the failed step in Actions and rerun the failed jobs. To retry an existing tag with repaired publishing scripts, use **Release → Run workflow → tag**. Published releases and tag targets stay unchanged.

Release-script regression checks:

```bash
pwsh ./scripts/test-release.ps1
```
