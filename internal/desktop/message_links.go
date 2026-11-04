package desktop

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/fondoger/copilotchat/internal/provider/doubao"
	"github.com/fondoger/copilotchat/internal/provider/webchat"
	"github.com/fondoger/copilotchat/internal/store"
)

func (a *app) messageMarkdownOptions(c *ui.Context, owner ui.Element, providerID string) *markdown.Options {
	options := ui.Local(owner, "message-markdown", func() markdown.Options { return markdown.Options{} })
	options.Theme = &markdown.Theme{FontSize: float32(a.state.Settings.FontSize), Font: c.Theme().Font, LineHeight: 1.5}
	options.OnLink = func(dest string) {
		if strings.HasPrefix(dest, "#") {
			options.Anchor = dest
			options.AnchorVersion++
			return
		}
		if action := a.appLinkAction(providerID, dest); action != nil {
			action()
		} else if ui.SafeToOpen(dest) {
			c.OpenURL(dest)
		}
	}
	return options
}

// Only explicit clicks in native message Markdown reach these actions. WebViews
// and generated previews do not share this router or gain a native bridge.
func (a *app) appLinkAction(providerID, dest string) func() {
	kind := ""
	switch dest {
	case doubao.RecoveryURL:
		kind = store.ProviderKindDoubao
	case webchat.ChatGPTRecoveryURL:
		kind = store.ProviderKindChatGPTWeb
	case webchat.ClaudeRecoveryURL:
		kind = store.ProviderKindClaudeWeb
	}
	if p := a.provider(providerID); kind != "" && p != nil && !p.Disabled && p.Kind == kind {
		return func() {
			if p := a.provider(providerID); p != nil && p.Kind == kind {
				a.openWebAPILogin(providerID)
			}
		}
	}
	return nil
}
