package desktop

import (
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/store"
)

var (
	microsoftCopilotIcon = loadIcon("microsoft-365-copilot-gray-bold")
	doubaoIcon           = loadIcon("doubao-mono")
)

func providerKindName(kind string) string {
	if kind == store.ProviderKindDoubao {
		return "豆包"
	}
	if kind == store.ProviderKindM365 {
		return "Microsoft 365 Copilot"
	}
	return "GitHub Copilot"
}

func providerIcon(c *ui.Context, kind string, size float32) ui.Element {
	if kind == store.ProviderKindDoubao {
		return ui.Image(c, doubaoIcon).Size(size, size).Shrink(0).Label("Doubao icon")
	}
	if kind == store.ProviderKindM365 {
		// Image preserves the supplied icon's grayscale shading.
		return ui.Image(c, microsoftCopilotIcon).Size(size, size).Shrink(0).Label("Microsoft 365 Copilot icon")
	}
	return ui.Icon(c, iChat).Size(size, size).Shrink(0).Label(providerKindName(kind) + " icon")
}
