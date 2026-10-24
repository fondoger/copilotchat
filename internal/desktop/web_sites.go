package desktop

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/fondoger/copilotchat/internal/store"
)

// Navigation permissions do not grant bridge trust to authentication origins.
func webNavigationURL(kind, raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.Port() != "" || u.Hostname() == "" {
		return false
	}
	site, ok := store.WebProviderFor(kind)
	if !ok {
		return false
	}
	if u.Host == site.Host {
		return true
	}
	switch kind {
	case store.ProviderKindDoubao:
		return doubaoNavigationURL(raw)
	case store.ProviderKindDeepSeekWeb:
		return u.Host == "www.deepseek.com" || u.Host == "accounts.google.com"
	case store.ProviderKindClaudeWeb:
		return u.Host == "accounts.google.com" || u.Host == "platform.claude.com"
	case store.ProviderKindGeminiWeb:
		return u.Host == "accounts.google.com" || u.Host == "myaccount.google.com" || u.Host == "consent.google.com"
	case store.ProviderKindChatGPTWeb:
		return u.Host == "auth.openai.com" || u.Host == "auth0.openai.com" || u.Host == "accounts.google.com" || u.Host == "login.microsoftonline.com" || u.Host == "login.live.com" || u.Host == "appleid.apple.com"
	}
	return false
}

func webScript(kind string, chat bool) string {
	site, ok := store.WebProviderFor(kind)
	if !ok {
		return ""
	}
	config, _ := json.Marshal(struct {
		Host string `json:"host"`
		Kind string `json:"kind"`
		Chat bool   `json:"chat"`
	}{site.Host, kind, chat})
	return strings.Replace(webChatPreload, "__WEB_CHAT_CONFIG__", string(config), 1)
}

func (a *app) webChatKind(chat *store.Conversation) string {
	if chat == nil {
		return ""
	}
	if chat.WebKind != "" {
		return chat.WebKind
	}
	if p := a.provider(chat.ProviderID); p != nil {
		return p.Kind
	}
	return ""
}

func (p *webChatView) siteName() string {
	if p.kind == "" || p.kind == store.ProviderKindDoubao {
		return "Doubao"
	}
	site, _ := store.WebProviderFor(p.kind)
	return site.Name
}

func webTitle(kind, raw string) string {
	if kind == store.ProviderKindDoubao {
		return doubaoTitle(raw)
	}
	title := strings.TrimSpace(raw)
	for _, name := range []string{"DeepSeek", "Claude", "Google Gemini", "Gemini", "ChatGPT"} {
		for _, separator := range []string{" - ", " | ", " — "} {
			title = strings.TrimSpace(strings.TrimSuffix(title, separator+name))
		}
	}
	switch strings.ToLower(title) {
	case "", "deepseek", "deepseek - into the unknown", "claude", "google gemini", "gemini", "chatgpt", "new chat", "new conversation", "新对话", "新聊天", "login", "log in", "sign in", "登录":
		return ""
	}
	if len([]rune(title)) > 200 {
		return ""
	}
	return title
}

func doubaoNavigationURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.Port() != "" || u.Hostname() == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	// Keep the authentication surface on the site's own production domains.
	return host == store.DoubaoHost || host == "doubao.com" || strings.HasSuffix(host, ".doubao.com") || host == "sso.douyin.com" || host == "passport.douyin.com" || host == "sso.snssdk.com"
}

func doubaoTitle(raw string) string {
	title := strings.TrimSpace(raw)
	for _, suffix := range []string{" - 豆包", " - Doubao", " | 豆包", " | Doubao"} {
		title = strings.TrimSpace(strings.TrimSuffix(title, suffix))
	}
	switch strings.ToLower(title) {
	case "", "doubao", "豆包", "豆包 - 字节跳动旗下 ai 智能助手", "豆包 - 随时帮你", "新对话", "新的对话", "新聊天", "new chat", "登录", "login", "sign in":
		return ""
	}
	if strings.HasPrefix(title, "豆包 - ") || len([]rune(title)) > 200 {
		return ""
	}
	return title
}
