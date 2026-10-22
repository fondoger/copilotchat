# UI design reference

Copilot Chat's layout follows ChatWise 26.9.1, inspected with its author's permission. The reference was the packaged application CSS and Svelte components. Extracted application code is not distributed with this project. Third-party license texts are included in the packaged resources/licenses directory.

Measurements below are logical pixels, applied as device-independent pixels in MyGo before operating-system display scaling.

## Colors and typography

| Surface | Light | Dark |
| --- | --- | --- |
| Chat background | `#ffffff` | `#181818` |
| Chat sidebar | `#e3e2e6` | `#201f21` |
| Main text | `#09090b` | `#fafafa` |
| Muted text | `#71717a` | `#a1a1aa` |
| Chat border | `#e4e4e7` | `#27272a` |
| Settings navigation | `#e2e1e2` | `#262626` |
| Settings content | `#efeeef` | `#1e1e1e` |
| Settings active row | `#d4d4d8` | `rgba(82,82,91,.70)` |
| Composer background | `#fafafa` | `#1c1c1c` |

Base chat text is 16px, ordinary settings labels 14px, helper text 12px, and settings navigation icons 20px. Controls use a 6px radius, cards 8px, and the composer 16px. Light settings controls use white backgrounds and subtle borders.

Windows uses Segoe UI with Microsoft YaHei UI / Microsoft YaHei for Chinese fallback. Sidebar titles additionally prefer an installed Noto Sans SC at weight 500. This keeps Chinese glyphs consistent under different system locales. Font files are not bundled. Chat font-size changes apply to user messages, assistant Markdown, and the composer.

## Chat layout and interactions

- The sidebar starts at 240px and can be resized between 240 and 500px. Width and group-collapse preferences persist.
- The header is 50px high with 20px horizontal padding and space for native window controls.
- Messages and the composer are centered in a column with a maximum width of 1024px and 20px horizontal padding. User bubbles use at most 70% of the available width.
- The composer has a 16px radius and 1px border. Its editor grows up to 300px, with 24px toolbar buttons and a circular send/stop action.
- Conversation rows use 14px medium titles and an 8px radius. Hover reveals Delete and Favorite; the context menu also offers Rename and Export.
- Message actions appear on hover or keyboard focus. Both message roles support copy and regenerate; user edits regenerate the conversation from that point, while assistant edits update the answer in place.
- Search opens a centered 512px dialog with title and content matches, highlighted terms, a message excerpt, and keyboard selection. Searching does not alter sidebar group state.

Code blocks have a 40px header and 28px hover actions. Long HTML/SVG initially shows 20 plain-text lines, a bottom fade, and the remaining-line count. Expanded HTML remains unhighlighted to avoid unnecessary tokenizer work. Play opens the embedded preview; closing it returns to the source. The remaining-lines button uses a translucent background because the native renderer has no backdrop-blur primitive.

Image attachments have square thumbnails in the composer and sent message. Clicking one opens an unchanged copy of the original image with the system image application; preview filenames use a content hash and detected image format.

## Menus

The Windows application popup uses MyGo native menu commands, including Edit roles that operate on the focused input or selected text. Opening the menu preserves the editing target. Its trigger remains available when the sidebar is collapsed.

| Menu | Commands |
| --- | --- |
| File | New Chat, Quit |
| Edit | Undo, Redo, Cut, Copy, Paste, Delete, Select All |
| View | Toggle Model Dropdown, Search, Toggle Sidebar, Abort Generation |
| Window | Minimize, Zoom, Next Chat, Previous Chat, Close |
| Help | Application documentation and About Copilot Chat |

The sidebar Settings popup offers Appearance and Settings. Appearance choices are Light, Dark, and System. The implementation does not expose controls for unimplemented ChatWise features such as Command Center, the right sidebar, voice input, or agent memory.

## Settings

Settings opens in a separate 900 × 620 native window with a minimum size of 700 × 480. The primary navigation is 160px wide with fixed icon positions and left-aligned text. The content header is 48px high. General and Appearance use grouped rows; Providers, MCP, and Extensions use a 200px secondary list and a detail pane.

| Page | Controls |
| --- | --- |
| General | Enter or the platform command modifier plus Enter; optional tray residence |
| Appearance | Light, Dark, System; chat font size and text preview |
| Providers | Named Copilot accounts, validated domains, device authorization, enable/disable, removal, model refresh, hidden and pinned models |
| MCP | Saved server enable/disable, JSON configuration, stdio and Streamable HTTP discovery tests |
| Extensions | Chat Title on/off and title-generation model |

Provider changes save automatically. A single enabled provider has an ungrouped model menu; multiple enabled providers receive named groups. Model capabilities and thinking options come from the service's model metadata.

## Asset provenance

The repository includes 37 extracted SVG files: 34 assets from eight public icon sets and three custom new-chat/sidebar icons used with the ChatWise author's permission. CSS-mask paint changes to `currentColor` for native tinting; paths and view boxes are retained. The manifests under `internal/desktop/assets/icons/` record packaged source identifiers, selectors, and hashes.

The original packaged reference locations are:

| Reference | Packaged path |
| --- | --- |
| Colors, typography, and CSS icon masks | `out/client/_app/immutable/assets/0.BxQNCRRI.css` |
| Chat layout and sidebar | `out/client/_app/immutable/nodes/3.CraAOkMI.js` |
| Settings navigation | `out/client/_app/immutable/nodes/2.BgjbSOHt.js` |
| Settings header and shared panes | `out/client/_app/immutable/chunks/Bya9XYu0.js`, `chunks/BOB8A2v6.js` |
| General and Appearance | `out/client/_app/immutable/nodes/5.D6P9y3zE.js`, `nodes/7.AQ5a25hx.js` |
| Chat Title | `out/client/_app/immutable/nodes/8.DKaMFEUC.js` |
| Windows application menu | `out/main/index.js` |

The application and tray use Copilot Chat's own artwork. Third-party trademarks remain their owners' property.

## Verification

UI tests exercise navigation, provider settings, message edits, attachment thumbnails, search, hover actions, and saved preferences using temporary state and synthetic models. Layout tests cover 720–1920px chat windows, minimum settings size, adjustable font sizes, and Chinese rendering at 100%, 125%, and 200% scale. These headless checks complement native runtime testing; they do not establish that an untested operating system behaves identically.

The public screenshots use those fixtures. See [building.md](building.md#documentation-screenshots) for regeneration commands and the current platform-validation limits.
