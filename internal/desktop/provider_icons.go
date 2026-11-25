package desktop

import (
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/store"
)

var (
	doubaoIcon       = loadIcon("doubao-mono")
	webProviderIcons = map[string]*ui.SVG{
		store.ProviderKindDeepSeekWeb: loadIcon("deepseek-mono"),
		store.ProviderKindChatGPTWeb:  loadIcon("openai-mono"),
		store.ProviderKindClaudeWeb:   loadIcon("claude-mono"),
		store.ProviderKindGeminiWeb:   loadIcon("gemini-mono"),
	}
)

func providerKindName(kind string) string {
	if web, ok := store.WebProviderFor(kind); ok {
		return web.Name
	}
	if store.IsGitHubProvider(kind) {
		return "GitHub Copilot"
	}
	if id := store.PluginID(kind); id != "" {
		return id
	}
	return kind
}

func providerIcon(c *ui.Context, kind string, size float32) ui.Element {
	if icon, ok := webProviderIcons[kind]; ok {
		return ui.Icon(c, icon).Size(size, size).Shrink(0).Label(providerKindName(kind) + " icon")
	}
	if kind == store.ProviderKindDoubao {
		return ui.Image(c, doubaoIcon).Size(size, size).Shrink(0).Label(providerKindName(kind) + " icon")
	}
	if !store.IsGitHubProvider(kind) {
		return ui.Icon(c, settingsIcons["Extensions"]).Size(size, size).Shrink(0).Label("Provider plugin icon")
	}
	return ui.Icon(c, iChat).Size(size, size).Shrink(0).Label(providerKindName(kind) + " icon")
}
