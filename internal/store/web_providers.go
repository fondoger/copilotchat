package store

import (
	"net/url"
	"regexp"
	"strings"
)

// WebProvider describes a browser provider's fixed site and new-chat entry point.
type WebProvider struct {
	Kind     string
	Name     string
	Host     string
	StartURL string
}

type webProviderRoute struct {
	WebProvider
	conversationPath string
	idPattern        *regexp.Regexp
}

var (
	webUUIDPattern             = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	geminiAccountPrefixPattern = regexp.MustCompile(`^/u/[0-9]+/`)
	webProviders               = map[string]webProviderRoute{
		ProviderKindDoubao: {
			WebProvider{ProviderKindDoubao, "豆包", DoubaoHost, DoubaoChatURL},
			"/chat/", regexp.MustCompile(`^[0-9]+$`),
		},
		ProviderKindDeepSeekWeb: {
			WebProvider{ProviderKindDeepSeekWeb, "DeepSeek Web", "chat.deepseek.com", "https://chat.deepseek.com/"},
			"/a/chat/s/", webUUIDPattern,
		},
		ProviderKindClaudeWeb: {
			WebProvider{ProviderKindClaudeWeb, "Claude Web", "claude.ai", "https://claude.ai/new"},
			"/chat/", webUUIDPattern,
		},
		ProviderKindGeminiWeb: {
			WebProvider{ProviderKindGeminiWeb, "Gemini Web", "gemini.google.com", "https://gemini.google.com/app"},
			"/app/", regexp.MustCompile(`^[0-9a-fA-F]+$`),
		},
		ProviderKindChatGPTWeb: {
			WebProvider{ProviderKindChatGPTWeb, "ChatGPT Web", "chatgpt.com", "https://chatgpt.com/"},
			"/c/", webUUIDPattern,
		},
	}
)

// WebProviderFor returns the fixed configuration for a supported browser kind.
func WebProviderFor(kind string) (WebProvider, bool) {
	provider, ok := webProviders[kind]
	return provider.WebProvider, ok
}

func IsWebProvider(kind string) bool {
	_, ok := WebProviderFor(kind)
	return ok
}

func parseWebURL(kind, raw string) (webProviderRoute, *url.URL, bool) {
	provider, ok := webProviders[kind]
	if !ok {
		return webProviderRoute{}, nil, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != provider.Host || u.User != nil || u.Opaque != "" || u.RawPath != "" {
		return webProviderRoute{}, nil, false
	}
	return provider, u, true
}

// webRoutePath removes Gemini's optional account selector for route matching.
// The original URL path is retained when persisting a conversation.
func webRoutePath(kind, path string) string {
	if kind == ProviderKindGeminiWeb {
		if prefix := geminiAccountPrefixPattern.FindString(path); prefix != "" {
			return path[len(prefix)-1:]
		}
	}
	return path
}

// WebConversationURL accepts only resumable conversations on the kind's fixed
// site. IDs stay strings, and query strings and fragments are not persisted.
func WebConversationURL(kind, raw string) (string, bool) {
	provider, u, ok := parseWebURL(kind, raw)
	if !ok {
		return "", false
	}
	id, ok := strings.CutPrefix(webRoutePath(kind, u.Path), provider.conversationPath)
	if !ok || !provider.idPattern.MatchString(id) {
		return "", false
	}
	return "https://" + provider.Host + u.Path, true
}

// WebBlankURL recognizes the new-chat route, including Gemini account selectors,
// but not a saved chat.
func WebBlankURL(kind, raw string) bool {
	provider, u, ok := parseWebURL(kind, raw)
	if !ok {
		return false
	}
	path := strings.TrimPrefix(provider.StartURL, "https://"+provider.Host)
	route := webRoutePath(kind, u.Path)
	return route == path || (path == "/" && route == "") || (path != "/" && route == path+"/")
}
