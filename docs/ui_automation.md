# UI automation

[MyGo automation](https://mygo.egoist.dev/docs/plugins/automation) exposes the running app over a local Unix socket. It is included only with the `e2e` build tag and listens only when `MYGO_AUTOMATION` is set. Normal builds and release packages do not include it; `mygo_noinspector` disables it even with `e2e`.

In Bash (including Git Bash on Windows), choose the client’s default socket and start an isolated development app:

```bash
export MYGO_AUTOMATION="$(go run ./scripts/automation -print-socket)"
```

```bash
go run -tags e2e .
```

Add `--data-dir <directory>` to reuse a particular development fixture. This does not replace the installed app. The socket grants full control of the app, including its pages: keep it in a directory only your user can write to. The default is `copilotchat-automation.sock` in Go's temporary directory, which is per-user on Windows and macOS; choose a private directory on systems with shared temporary directories.

From another terminal, list windows and use the returned window ID:

```bash
go run ./scripts/automation windows
```

```bash
go run ./scripts/automation tree '{"window":1}'
```

The tree contains labels, values, bounds, actions and stable element keys. For example, set the unsent composer text by key:

```bash
go run ./scripts/automation act '{"window":1,"key":"composer","action":"setValue","text":"Automation probe"}'
```

```bash
go run ./scripts/automation idle '{"window":1,"timeout":3000}'
```

```bash
go run ./scripts/automation act '{"window":1,"key":"composer","action":"setValue","text":""}'
```

Use `id` from the latest tree for controls without keys. The client forwards any official method and JSON params, prints the result as JSON, and exits nonzero on protocol errors or timeout. Its socket comes from `-socket`, then `MYGO_AUTOMATION`, then the default path; `-timeout` defaults to 10 seconds. Native windows support `tree`/`act`/`input`; whole-page windows use `eval`. Embedded WebViews appear as `webView` nodes without their page contents. Use `idle` between actions that depend on the preceding frame.

Validate both build paths after changing the integration:

```bash
go test -tags e2e ./...
```
