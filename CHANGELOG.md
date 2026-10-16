# Changelog

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
