# Third-party notices

The application depends on MyGo and its Go text/rendering dependencies. Their full license texts are distributed under `resources/licenses/` and copied alongside the MyGo packaged application.

| Dependency | Version | License |
| --- | --- | --- |
| github.com/egoist/mygo | v0.5.2-0.20261015013204-7e8688284e4b | MIT |
| github.com/ebitengine/purego | v0.11.1 | Apache-2.0 |
| github.com/go-text/typesetting | v0.3.5 | BSD-3-Clause |
| golang.org/x/image | v0.46.0 | BSD-3-Clause |
| golang.org/x/text | v0.43.0 | BSD-3-Clause |

The Copilot wire protocol was independently implemented in Go using the public OpenCode provider implementation as a reference; upstream links are recorded in `docs/copilot.md`.

The owner explicitly authorized copying UI icons and colors from their ChatWise 26.9.1 installation. The 37 SVG files under `internal/desktop/assets/icons/` are extracted from that installation, with CSS mask paint changed from black to `currentColor` for native tinting. Their paths and view boxes are preserved. `sources.json`, `sidebar-sources.json`, and `interaction-sources.json` record the packaged source, selectors, and source hashes. Of these files, 34 come from the eight public icon sets listed below; the remaining three are ChatWise's custom new-chat and two sidebar-toggle variants, used under the owner's permission. Copilot Chat retains its own application name and original launcher/tray artwork; the extracted application JavaScript and other packaged files are not distributed.

The extracted UI assets include these open-source icon sets. Their complete license texts are included under `resources/licenses/` and in packaged releases:

| Icon set | Used assets | License / source |
| --- | --- | --- |
| Tabler Icons | Menu, Settings, More, Close, chevrons, star, filled star, sun, trash, check, search, plus, minus | MIT — https://github.com/tabler/tabler-icons |
| Ionicons | Appearance, Providers, Extensions, code expand/collapse | MIT — https://github.com/ionic-team/ionicons |
| Framework7 Icons | MCP hammer, preview play variants, search clear | MIT — https://github.com/framework7io/framework7-icons |
| Feathericon | Sidebar search | MIT — https://github.com/feathericon/feathericon |
| Remix Icon | Attachment and send arrow | Apache-2.0; corresponding icons are available in v4.6.0 — https://github.com/Remix-Design/RemixIcon/tree/v4.6.0 |
| MingCute | Copy, regenerate, rename, response details, search result | Apache-2.0 — https://github.com/Richard9394/MingCute |
| Bootstrap Icons | Stop and empty-chat illustration | MIT — https://github.com/twbs/icons |
| Lobe Icons | GitHub Copilot provider symbol (`githubcopilot.svg`) | MIT — https://github.com/lobehub/lobe-icons |

The GitHub Copilot symbol identifies the configured provider; the associated trademarks remain their owners' property.
