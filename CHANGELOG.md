# Changelog

## 0.5.10

- Use `mygo.json` as the single version source for the application and provider request headers, with `dev` for plain Go development builds.
- Simplify release validation to check the tag against `mygo.json` without parsing Go source files.

## 0.5.8

- Improve multi-selection appearance and animate favorite conversation movement.
- Allow disabled web providers to reopen existing conversations.

## 0.5.6

- Add five web chat providers with shared sessions, drafts, and dedicated provider icons.
- Add Shift-click range selection and bulk conversation deletion.
- Improve embedded chat layouts and prevent sidebar/header flashes.

## 0.5.5

- Map Desktop model families by effort support: max to fable, xhigh to opus, other reasoning or 1M-context models to opus-4-5, and remaining models to sonnet-4-5. Preserve native Claude IDs.

- Add Claude Desktop configuration with one-click Apply, opaque compatible model IDs, capability discovery, and `modelPrefer1mContext` enabled.
- Add optional GoVisual inspection for inbound Gateway requests and outbound provider HTTP calls, including upstream URL, status and timing. Keep captures bounded and credential headers redacted.
- Forward token-counting requests to the upstream Messages or Responses API, preserving upstream errors without local token estimates.
- Exclude disabled providers from LLM Gateway while keeping its model selections independent of native chat visibility.
- Use provider-prefixed Gateway model IDs, support Anthropic-to-Responses translation, and apply Claude Code, Claude Desktop and Codex configuration directly.
- Remove the standalone third-party notices document and its packaging references; retain bundled dependency license texts.

## 0.5.2

- Redesign Agent setup with compact profile tabs, context menus for rename/duplicate/delete, and a fully expanded model table below configuration.
- Add five optional Claude Code model defaults in a collapsed section. Keep selections attached to their provider and model when aliases change.
- Renumber automatic aliases among enabled models while preserving manually edited aliases. Keep each profile's choices and configuration independent.
- Add a Codex remote compaction toggle: use provider name `OpenAI` when enabled and `Copilot Chat Proxy` otherwise. Forward `/responses/compact` with the same conversation ID; availability depends on the upstream service.
- Collapse configuration previews while keeping Copy available. Maintain the local Codex model catalog automatically, including while forwarding is off.

## 0.5.1

- Add named forwarding profiles with separate base URLs, independent model selections and aliases, plus create, duplicate, rename and delete controls.
- Combine models from multiple accounts. Resolve duplicate default aliases with `(2)`, `(3)`, and route each alias to its exact account and model.
- Use the readable local key `copilotchat`. Re-copy Agent setup configuration after upgrading from provider-specific URLs and the previous long key.
- Return aliases as model IDs and names. Support Claude Code discovery and a live local Codex catalog compatible with Codex 0.146.0; restart agents after editing a profile's models.
- Preserve tools, thinking, cache fields and streamed usage while translating the request's model alias. Isolate conversation IDs and revoke active requests when a profile is deleted or renamed.

## 0.5.0

- Replace the standalone credential helper with an embedded, authenticated loopback forwarding service. Copy a provider-specific base URL and fixed local key for Claude Code or Codex.
- Preserve raw Messages/Responses payloads and streamed usage, expose Claude model discovery, and map agent session IDs to stable provider-scoped conversation IDs.
- Add Open at login, tray/startup recommendations, and confirmation before exiting or disabling tray mode while forwarding is enabled.
- Stop in-flight forwarding when its provider is disabled, signed out, or removed. Document setup and protocol handling in Chinese.
- Remove helper executable sources and packaging; existing users should re-copy Agent setup configuration.

## 0.4.3

- Generate a readable direct executable command for Claude Code on Windows, without a PowerShell or Base64 wrapper. Re-copy the configuration from Agent setup to replace an earlier command.
- Verify native command execution with spaces, Unicode, shell metacharacters, and trailing directory separators, including preservation of the helper's exit status.

## 0.4.2

- Bundle a standalone credential helper for Windows and macOS. It reads the selected provider from the system credential store without requiring the GUI to run.
- Add Settings → Extensions → Agent setup. Select a provider, Claude Code or Codex / ChatGPT Desktop, and a compatible model to copy its configuration without embedding a token.
- Filter agent model choices by Messages or Responses support and preserve all advertised model protocols.
- Include and verify the helper in installation packages and signed updates for all four platforms.

## 0.4.1

- Simplify the README and keep a single light-theme screenshot.

## 0.4.0

- Restore provider icons in settings and model lists.
- Replace model visibility modes with an Enabled checkbox for every model, plus Select all and Deselect all. Models default to enabled and changes save automatically for each provider.
- Organize shared inference code and the GitHub integration under the provider package.

## 0.3.3

- Focus the chat input with Ctrl+L on Windows or Command+L on macOS.
- Preserve the existing text, cursor, and selection when the input already has focus.

## 0.3.2

- Upgrade MyGo to the latest stable release, v0.5.5.
- Reduce the model picker width from 480 to 360 and reserve space between its pin buttons and scrollbar.
- Use a Tabler pin icon consistent with the app's other controls.
- Left-align the Thought process control, including reasoning returned by Grok 4.7.
- Preserve rounded chat-row corners when sidebar hover actions appear.

## 0.3.1

- Add signed automatic updates from GitHub Releases for Windows and macOS, on x64 and ARM64.
- Add automatic update preferences, immediate frequency changes, manual checks, download progress, release notes, and restart controls in Settings → General.
- Check daily by default; choose every hour, 6 hours, 12 hours, day, or week. Disabling automatic updates still allows manual updates.
- Keep chats running while an update installs. Restart when ready or use the update on the next launch; protect unsent drafts and in-progress responses before restarting.

Version 0.3.0 has no updater. Install 0.3.1 manually once to receive future updates automatically. Update signatures are separate from Windows code signing and Apple notarization.

## 0.3.0

- First public release of the native GitHub Copilot chat client.
- Support multiple providers, streamed responses, attachments, model capabilities, local conversations, HTML previews, and MCP tools.
- Publish Windows and macOS packages for x64 and ARM64.
