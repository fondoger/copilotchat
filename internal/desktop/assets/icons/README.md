# UI icon sources

Most SVGs were extracted from the user's ChatWise 26.9.1 installation with their explicit permission on 2026-10-15. They reproduce the shipped UI geometry instead of approximating it with newly drawn paths.

- CSS masks: `out/client/_app/immutable/assets/0.BxQNCRRI.css`; original selectors and decoded-source SHA-256 hashes are in `sources.json`, `sidebar-sources.json`, and `interaction-sources.json`.
- Sidebar toggles: `nodes/3.CraAOkMI.js`, compiled `Gj`/`Kj` component. The two files instantiate its left-sidebar open/closed states. The dynamic `line` coordinates are resolved, and `1em` dimensions become 24; the view box and geometry are unchanged.
- Copilot provider icon: `chunks/CDQJJyzO.js`, compiled `be`/`xe` component (`copilot-mono.svg`). Its path is the Lobe Icons `githubcopilot.svg` design. The component's `fill-rule="evenodd"` and 24×24 view box are retained.

The user-supplied `microsoft-365-copilot-gray-bold.svg` and `doubao-mono.svg` identify the Microsoft 365 Copilot and Doubao providers. Both are rendered as images to preserve their grayscale shading. The model picker's Web Chat group heading uses the existing neutral `bi-chat-square-quote.svg`, not a provider brand.

`tabler-route.svg` identifies LLM Gateway using connected endpoints to represent request routing. It comes unchanged from [Tabler Icons v3.35.0](https://github.com/tabler/tabler-icons/blob/v3.35.0/icons/outline/route.svg), under the MIT license distributed with the application.

CSS mask `fill="black"` / `stroke="black"` becomes `currentColor`, so MyGo can apply the same UI colors. No path coordinates were redrawn. All SVGs are embedded in the executable; the extracted ChatWise bundle is not a runtime dependency.

Third-party license texts are included in the packaged resources/licenses directory. The custom edit and sidebar icons are copied under the owner's permission; they are not represented as newly authored or independently licensed artwork. Launcher and tray branding remain Copilot Chat's own.
