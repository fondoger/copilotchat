package desktop

import (
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/store"
)

var microsoftCopilotIcon = loadIcon("microsoft-copilot-color")

func providerKindName(kind string) string {
	if kind == store.ProviderKindM365 {
		return "Microsoft 365 Copilot"
	}
	return "GitHub Copilot"
}

func providerIcon(c *ui.Context, kind string, size float32) ui.Element {
	if kind == store.ProviderKindM365 {
		// Image preserves the Microsoft Copilot symbol's original colors.
		return ui.Image(c, microsoftCopilotIcon).Size(size, size).Shrink(0).Label("Microsoft 365 Copilot icon")
	}
	return ui.Icon(c, iChat).Size(size, size).Shrink(0).Label("GitHub Copilot icon")
}
