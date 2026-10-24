package store

import (
	"strings"
	"testing"
)

var webProviderCases = []struct {
	kind, name, host, blank, chat string
}{
	{ProviderKindDoubao, "豆包", "www.doubao.com", "/chat", "/chat/38447877386550786"},
	{ProviderKindDeepSeekWeb, "DeepSeek Web", "chat.deepseek.com", "/", "/a/chat/s/01234567-89ab-cdef-0123-456789abcdef"},
	{ProviderKindClaudeWeb, "Claude Web", "claude.ai", "/new", "/chat/01234567-89ab-cdef-0123-456789abcdef"},
	{ProviderKindGeminiWeb, "Gemini Web", "gemini.google.com", "/app", "/app/0123456789abcdef"},
	{ProviderKindChatGPTWeb, "ChatGPT Web", "chatgpt.com", "/", "/c/01234567-89ab-cdef-0123-456789abcdef"},
}

func TestWebProviderURLBoundaries(t *testing.T) {
	for _, test := range webProviderCases {
		t.Run(test.kind, func(t *testing.T) {
			origin := "https://" + test.host
			chat := origin + test.chat
			provider, ok := WebProviderFor(test.kind)
			if !ok || !IsWebProvider(test.kind) || provider != (WebProvider{test.kind, test.name, test.host, origin + test.blank}) {
				t.Fatalf("incorrect registry entry: %+v", provider)
			}
			for _, suffix := range []string{"", "?source=chat#latest"} {
				if got, ok := WebConversationURL(test.kind, chat+suffix); !ok || got != chat {
					t.Errorf("valid conversation rejected: %q -> %q", chat+suffix, got)
				}
				if !WebBlankURL(test.kind, provider.StartURL+suffix) {
					t.Errorf("valid blank route rejected: %q", provider.StartURL+suffix)
				}
			}
			if test.blank == "/" && !WebBlankURL(test.kind, origin) {
				t.Error("origin without explicit root path rejected")
			}
			if WebBlankURL(test.kind, chat) {
				t.Error("saved conversation treated as blank")
			}
			invalidChats := []string{test.blank, test.chat + "/", test.chat + "/extra", test.chat + "-", test.chat + "g", "/../" + strings.TrimPrefix(test.chat, "/")}
			if test.kind != ProviderKindGeminiWeb {
				invalidChats = append(invalidChats, "/u/0"+test.chat)
			}
			for _, path := range invalidChats {
				if got, ok := WebConversationURL(test.kind, origin+path); ok {
					t.Errorf("invalid conversation accepted: %q -> %q", path, got)
				}
			}
			for _, badOrigin := range []string{
				"http://" + test.host, "https://" + test.host + ":443",
				"https://" + test.host + ".evil.test", "https://" + test.host + ".",
				"https://evil.test@" + test.host, "https://" + test.host + "@evil.test",
				"https://" + strings.ToUpper(test.host), "//" + test.host,
			} {
				if got, ok := WebConversationURL(test.kind, badOrigin+test.chat); ok {
					t.Errorf("unsafe origin accepted: %q -> %q", badOrigin, got)
				}
				if WebBlankURL(test.kind, badOrigin+test.blank) {
					t.Errorf("unsafe blank origin accepted: %q", badOrigin)
				}
			}
			if test.blank != "/" && !WebBlankURL(test.kind, provider.StartURL+"/") {
				t.Error("blank route trailing slash rejected")
			}
			invalidBlanks := []string{test.blank + "//", test.blank + "/extra"}
			if test.kind != ProviderKindGeminiWeb {
				invalidBlanks = append(invalidBlanks, "/u/0"+test.blank)
			}
			for _, path := range invalidBlanks {
				if WebBlankURL(test.kind, origin+path) {
					t.Errorf("non-blank path accepted: %q", path)
				}
			}
			// Even equivalent percent-encoded paths are outside the allowed routes.
			escapedChat := strings.Replace(test.chat, "a", "%61", 1)
			if escapedChat == test.chat {
				escapedChat = strings.Replace(test.chat, "0", "%30", 1)
			}
			for _, raw := range []string{origin + escapedChat, "javascript:alert(1)", "file://" + test.host + test.chat, origin + "/%zz"} {
				if got, ok := WebConversationURL(test.kind, raw); ok {
					t.Errorf("unsafe route accepted: %q -> %q", raw, got)
				}
				if WebBlankURL(test.kind, raw) {
					t.Errorf("unsafe blank route accepted: %q", raw)
				}
			}
		})
	}
	for _, kind := range []string{"", "unknown", ProviderKindGitHub, ProviderKindM365} {
		if _, ok := WebProviderFor(kind); ok || IsWebProvider(kind) {
			t.Errorf("native/unknown kind registered as web: %q", kind)
		}
		if _, ok := WebConversationURL(kind, "https://claude.ai/chat/01234567-89ab-cdef-0123-456789abcdef"); ok || WebBlankURL(kind, "https://claude.ai/new") {
			t.Errorf("native/unknown kind accepted web URL: %q", kind)
		}
	}
}

func TestGeminiAccountRoutes(t *testing.T) {
	const origin = "https://gemini.google.com"
	for _, prefix := range []string{"", "/u/0", "/u/1", "/u/001", "/u/123456789012345678901234567890"} {
		t.Run(prefix, func(t *testing.T) {
			blank := origin + prefix + "/app"
			chat := blank + "/0123456789aBcDeF"
			for _, suffix := range []string{"", "?source=chat#latest"} {
				if !WebBlankURL(ProviderKindGeminiWeb, blank+suffix) || !WebBlankURL(ProviderKindGeminiWeb, blank+"/"+suffix) {
					t.Errorf("blank route rejected: %q", blank+suffix)
				}
				if got, ok := WebConversationURL(ProviderKindGeminiWeb, chat+suffix); !ok || got != chat {
					t.Errorf("account prefix not preserved: %q -> %q", chat+suffix, got)
				}
			}
			if _, ok := WebConversationURL(ProviderKindGeminiWeb, blank); ok || WebBlankURL(ProviderKindGeminiWeb, chat) {
				t.Error("blank and conversation routes were confused")
			}
		})
	}
	for _, path := range []string{
		"/u/", "/u/1", "/u/1/", "/u//app", "/u/-1/app", "/u/+1/app", "/u/1.0/app", "/u/1a/app", "/u/１/app",
		"/u/1app", "/u/1//app", "/u/1/u/2/app", "/u/1/../app", "/u/1/./app", "/U/1/app", "/u/1/App",
		"/u/%31/app", "/%75/1/app", "/u/1/%61pp", "/u/1%2fapp", "/u/1%252fapp", "/u/1\\app",
	} {
		for _, suffix := range []string{"", "/0123456789abcdef"} {
			raw := origin + path + suffix
			if _, ok := WebConversationURL(ProviderKindGeminiWeb, raw); ok || WebBlankURL(ProviderKindGeminiWeb, raw) {
				t.Errorf("invalid account route accepted: %q", raw)
			}
		}
	}
	for _, raw := range []string{
		origin + "/u/1/app//", origin + "/u/1/app/extra", origin + "/u/1/app/abc/", origin + "/u/1/app/abc/extra",
		origin + "/u/1/app/abcg", origin + "/u/1/app/-abc", origin + "/u/1/app/%61bc", origin + "/u/1/app/abc%2fdef", origin + "/u/1/app/%zz",
		"http://gemini.google.com/u/1/app", "https://gemini.google.com:443/u/1/app", "https://gemini.google.com:/u/1/app",
		"https://user@gemini.google.com/u/1/app", "https://user:pass@gemini.google.com/u/1/app",
		"https://gemini.google.com.evil.test/u/1/app", "https://gemini.google.com./u/1/app", "https://GEMINI.GOOGLE.COM/u/1/app",
		"//gemini.google.com/u/1/app",
	} {
		for _, suffix := range []string{"", "/0123456789abcdef"} {
			if _, ok := WebConversationURL(ProviderKindGeminiWeb, raw+suffix); ok || WebBlankURL(ProviderKindGeminiWeb, raw+suffix) {
				t.Errorf("unsafe account URL accepted: %q", raw+suffix)
			}
		}
	}
}

func TestWebProviderPersistence(t *testing.T) {
	cases := append(webProviderCases[:len(webProviderCases):len(webProviderCases)],
		struct{ kind, name, host, blank, chat string }{ProviderKindGeminiWeb, "Gemini Web", "gemini.google.com", "/u/1/app", "/u/1/app/0123456789abcdef"},
		struct{ kind, name, host, blank, chat string }{ProviderKindGeminiWeb, "Gemini Web", "gemini.google.com", "/u/001/app", "/u/001/app/ABCdef0123456789"},
	)
	for _, test := range cases {
		t.Run(test.kind, func(t *testing.T) {
			disk, err := New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			state := DefaultState()
			state.Settings.Providers = append(state.Settings.Providers, Provider{ID: "web", Kind: test.kind})
			state.Settings.ActiveProviderID = "web"
			canonical := "https://" + test.host + test.chat
			state.Conversations = []Conversation{
				{ID: "remote", ProviderID: "web", WebKind: test.kind, WebURL: canonical + "?source=chat#latest", Title: "My title", TitleManual: true, WebStarted: true},
				{ID: "draft", ProviderID: "web", WebKind: test.kind},
				{ID: "native", ProviderID: DefaultProviderID, Model: "native-model", Messages: []Message{{ID: "message", Content: "native message"}}},
			}
			if err := disk.Save(state); err != nil {
				t.Fatal(err)
			}
			got, err := disk.Load()
			if err != nil {
				t.Fatal(err)
			}
			p := got.Settings.Providers[1]
			if p.Kind != test.kind || p.Host != test.host || p.Name != test.name || got.Settings.ActiveProviderID != "web" {
				t.Fatalf("web provider did not round trip: %+v", p)
			}
			chat := got.Conversations[0]
			if chat.WebKind != test.kind || chat.WebURL != canonical || chat.Title != "My title" || !chat.TitleManual || !chat.WebStarted {
				t.Fatalf("remote conversation did not round trip: %+v", chat)
			}
			if got.Conversations[1].WebKind != test.kind || got.Conversations[1].WebURL != "" {
				t.Fatalf("draft lost: %+v", got.Conversations[1])
			}
			native := got.Conversations[2]
			if native.WebKind != "" || native.WebURL != "" || native.Model != "native-model" || native.Messages[0].Content != "native message" {
				t.Fatalf("native conversation changed: %+v", native)
			}
			got.Settings.Providers = got.Settings.Providers[:1]
			if err := disk.Save(got); err != nil {
				t.Fatal(err)
			}
			orphan, err := disk.Load()
			if err != nil || orphan.Conversations[0].WebKind != test.kind || orphan.Conversations[0].WebURL != canonical || orphan.Conversations[1].WebKind != test.kind {
				t.Fatalf("orphan web conversation lost: %v, %+v", err, orphan)
			}
		})
	}
}

func TestWebProviderRejectsInvalidState(t *testing.T) {
	for _, test := range webProviderCases {
		t.Run(test.kind, func(t *testing.T) {
			for _, host := range []string{"github.com", test.host + ":443", "https://" + test.host, strings.ToUpper(test.host), " " + test.host} {
				state := DefaultState()
				state.Settings.Providers = []Provider{{ID: "web", Kind: test.kind, Host: host}}
				if err := validateProviders(&state); err == nil {
					t.Errorf("invalid provider host accepted: %q", host)
				}
			}
			for _, chat := range []Conversation{
				{WebKind: test.kind, WebURL: "https://evil.test" + test.chat},
				{WebKind: test.kind, WebURL: "https://" + test.host + test.blank},
				{WebKind: "", WebURL: "https://" + test.host + test.chat},
				{WebKind: ProviderKindGitHub},
				{WebKind: "unknown"},
			} {
				state := DefaultState()
				state.Conversations = []Conversation{chat}
				if err := validateProviders(&state); err == nil {
					t.Errorf("invalid conversation accepted: %+v", chat)
				}
			}
			for _, other := range webProviderCases {
				if other.kind == test.kind {
					continue
				}
				state := DefaultState()
				state.Conversations = []Conversation{{WebKind: test.kind, WebURL: "https://" + other.host + other.chat}}
				if err := validateProviders(&state); err == nil {
					t.Errorf("cross-provider conversation URL accepted for %q", other.kind)
				}
			}
		})
	}
}
