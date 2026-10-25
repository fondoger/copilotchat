package desktop

import (
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/store"
)

func (a *app) webChatContent(c *ui.Context, chat *store.Conversation) {
	if chat == nil {
		a.closeWebChat()
		return
	}
	provider := a.provider(chat.ProviderID)
	if !a.webProviderAvailable(chat.ProviderID, chat) {
		a.closeWebChat()
		ui.Text(c, "This web conversation is unavailable. Check its provider in Settings.").Padding(16).TextColor(c.Theme().Danger)
		return
	}
	if p := a.webChat; p != nil && (!a.currentWebChatView(p) || p.chatID != chat.ID || (p.view != nil && p.view.IsDestroyed())) {
		a.closeWebChat()
	}
	if a.webChat == nil && a.webParked != nil {
		p := a.webParked
		a.webParked = nil
		p.idleGeneration++
		if p.idleTimer != nil {
			p.idleTimer.Stop()
			p.idleTimer = nil
		}
		if p.kind == provider.Kind && p.owner == a.win && p.view != nil && !p.view.IsDestroyed() && p.err == "" {
			a.webChat = p
			if p.chatID != chat.ID || p.resetDraft || p.switching {
				a.retargetWebChat(p, chat)
			} else {
				p.view.Focus()
			}
		} else {
			destroyWebChatView(p)
		}
	}
	if a.win == nil {
		return // NewWindow can render its content before assigning the owner.
	}
	if a.webChat == nil {
		site, _ := store.WebProviderFor(provider.Kind)
		target := site.StartURL
		if chat.WebURL != "" {
			var ok bool
			target, ok = store.WebConversationURL(provider.Kind, chat.WebURL)
			if !ok {
				ui.Text(c, "Invalid website conversation URL.").Padding(16).TextColor(c.Theme().Danger)
				return
			}
		}
		a.webChat = a.newWebChatView(a.win, chat.ProviderID, chat.ID, target)
	}
	a.renderWebChat(c, a.webChat, a.closeWebChat)
}

func (a *app) webProviderView(c *ui.Context, providerID string) {
	provider := a.provider(providerID)
	if !a.webProviderAvailable(providerID, nil) {
		a.closeWebSettings()
		ui.Text(c, "Enable this web provider to sign in.").TextColor(c.Theme().TextMuted)
		return
	}
	if p := a.webSettings; p != nil && (!a.currentWebChatView(p) || p.providerID != providerID || (p.view != nil && p.view.IsDestroyed())) {
		a.closeWebSettings()
	}
	if a.settingsWin == nil {
		return
	}
	if a.webSettings == nil {
		a.webSettings = a.newWebChatView(a.settingsWin, providerID, "", func() string { site, _ := store.WebProviderFor(provider.Kind); return site.StartURL }())
	}
	a.renderWebChat(c, a.webSettings, a.closeWebSettings)
}

func (a *app) renderWebChat(c *ui.Context, p *webChatView, retry func()) {
	if p.err != "" {
		ui.Column(c).Fill().Padding(16).Gap(12).Children(func() {
			ui.Text(c, p.err).TextColor(c.Theme().Danger)
			ui.Button(c, "Retry "+p.siteName()).OnClick(retry)
		})
		return
	}
	if p.notice != "" {
		ui.Row(c).FillWidth().Shrink(0).Padding(8, 12).Gap(8).Children(func() {
			ui.Text(c, p.notice).Grow(1).TextColor(c.Theme().Danger).FontSize(12)
			ui.Button(c, "Retry "+p.siteName()).OnClick(retry)
		})
	}
	ui.WebView(c, p.view).Label(p.siteName()).Grow(1).FillWidth().MinHeight(0)
}
