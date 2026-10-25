package desktop

import (
	"net/url"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/provider"
)

func (a *app) nativeSearchControl(c *ui.Context) {
	model, ok := a.modelForProvider(a.selectedProviderID(), a.selectedModel())
	if !ok || !provider.SupportsNativeSearch(model) || a.isWebChat(a.current()) {
		return
	}
	enabled := a.current() != nil && a.current().NativeSearch
	text, tooltip := "", "Native Web Search: Disabled"
	if enabled {
		text, tooltip = "Search", "Native Web Search: Auto"
	}
	button := composerButton(c, iNativeSearch, "Web search", 16, text).Tooltip(tooltip).Disabled(a.streaming)
	if enabled {
		// Match ChatWise's composer accent and selected/hover background opacity.
		accent := ui.Hex("#3180ff")
		if c.Theme().Dark {
			accent = ui.Hex("#155dfc")
		}
		opacity := float32(.2)
		if button.Hovered() {
			opacity = .3
		}
		button.TextColor(accent).Background(accent.Alpha(opacity))
	}
	button.OnClick(func() {
		chat := a.current()
		a.setNativeSearch(chat == nil || !chat.NativeSearch)
	})
}

func (a *app) setNativeSearch(enabled bool) {
	if a.streaming {
		return
	}
	model, ok := a.modelForProvider(a.selectedProviderID(), a.selectedModel())
	if !ok || !provider.SupportsNativeSearch(model) || a.isWebChat(a.current()) {
		return
	}
	if a.current() == nil {
		a.finishNewChat()
	}
	if chat := a.current(); chat != nil {
		chat.NativeSearch = enabled
		a.save()
	}
}

func appendCitations(existing []provider.Citation, incoming []provider.Citation) []provider.Citation {
	seen := make(map[string]bool, len(existing))
	for _, citation := range existing {
		seen[citation.URL] = true
	}
	for _, citation := range incoming {
		if !citationURL(citation.URL) || seen[citation.URL] {
			continue
		}
		seen[citation.URL] = true
		existing = append(existing, citation)
	}
	return existing
}

func citationURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil
}

func (a *app) messageCitations(c *ui.Context, citations []provider.Citation) {
	for _, citation := range citations {
		if !citationURL(citation.URL) {
			continue
		}
		title := strings.TrimSpace(citation.Title)
		if title == "" {
			title = citation.URL
		}
		ui.ButtonBase(c).Label(title).Tooltip(citation.URL).Padding(2, 4).Radius(4).Justify(ui.Start).
			TextColor(c.Theme().Accent).Children(func() {
			ui.Text(c, title).FontSize(12).SingleLine()
		}).OnClick(func() {
			if err := mygo.Shell.OpenExternal(citation.URL); err != nil {
				a.status = err.Error()
			}
		})
	}
}
