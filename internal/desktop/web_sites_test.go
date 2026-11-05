package desktop

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fondoger/copilotchat/internal/store"
)

// Keep the fixtures explicit: deriving expected routes/selectors from the script
// would hide regressions in the site adapters themselves.
var webSiteFixtures = []struct {
	kind, start, remote, other, editor, suffix string
	auth                                       []string
}{
	{store.ProviderKindDeepSeekWeb, "https://chat.deepseek.com/", "https://chat.deepseek.com/a/chat/s/01234567-89ab-cdef-0123-456789abcdef", "https://chat.deepseek.com/a/chat/s/abcdef01-2345-6789-abcd-ef0123456789", `textarea[placeholder="Message DeepSeek"]`, "DeepSeek", []string{"https://www.deepseek.com/", "https://accounts.google.com/signin"}},
	{store.ProviderKindClaudeWeb, "https://claude.ai/new", "https://claude.ai/chat/01234567-89ab-cdef-0123-456789abcdef", "https://claude.ai/chat/abcdef01-2345-6789-abcd-ef0123456789", `[data-testid="chat-input"][contenteditable="true"]`, "Claude", []string{"https://accounts.google.com/signin", "https://platform.claude.com/login"}},
	{store.ProviderKindGeminiWeb, "https://gemini.google.com/app", "https://gemini.google.com/app/0123456789abcdef", "https://gemini.google.com/app/abcdef0123456789", `.ql-editor[role="textbox"][contenteditable="true"]`, "Google Gemini", []string{"https://accounts.google.com/signin", "https://myaccount.google.com/", "https://consent.google.com/"}},
	{store.ProviderKindChatGPTWeb, "https://chatgpt.com/", "https://chatgpt.com/c/01234567-89ab-cdef-0123-456789abcdef", "https://chatgpt.com/c/abcdef01-2345-6789-abcd-ef0123456789", `[contenteditable="true"][data-composer-markdown]`, "ChatGPT", []string{"https://auth.openai.com/", "https://auth0.openai.com/", "https://accounts.google.com/signin", "https://login.microsoftonline.com/", "https://login.live.com/", "https://appleid.apple.com/"}},
}

func TestWebSitesNavigationIsolation(t *testing.T) {
	for _, site := range webSiteFixtures {
		t.Run(site.kind, func(t *testing.T) {
			for _, raw := range append([]string{site.start, site.remote}, site.auth...) {
				if !webNavigationURL(site.kind, raw) {
					t.Errorf("blocked site or authentication URL %q", raw)
				}
			}
			host := strings.Split(strings.TrimPrefix(site.start, "https://"), "/")[0]
			denied := []string{
				"http://" + host + "/", "https://" + host + ":443/", "https://user@" + host + "/",
				"https://" + host + ".evil.invalid/", "https://evil.invalid/" + host,
				"https://sub." + host + "/", "https://accounts.google.com.evil.invalid/",
				"https://accounts.google.com:443/", "https://user@accounts.google.com/",
				"https://localhost/", "https://127.0.0.1/", "https://[::1]/", "//" + host + "/",
				"file:///tmp/chat", "javascript:alert(1)", "mygo://app", "https:opaque", "https://%zz/",
			}
			for _, other := range webSiteFixtures {
				if other.kind != site.kind {
					denied = append(denied, other.start, other.remote)
				}
			}
			denied = append(denied, store.DoubaoChatURL)
			for _, raw := range denied {
				if webNavigationURL(site.kind, raw) {
					t.Errorf("allowed cross-site or unsafe navigation %q", raw)
				}
			}
		})
	}
	if webNavigationURL("unknown-web", "https://accounts.google.com/") || webScript("unknown-web", true) != "" {
		t.Fatal("unknown provider acquired navigation or script privileges")
	}
}

func TestWebSitesTitleCleanup(t *testing.T) {
	for _, site := range webSiteFixtures {
		t.Run(site.kind, func(t *testing.T) {
			for _, separator := range []string{" - ", " | ", " — "} {
				if got := webTitle(site.kind, "  Research notes"+separator+site.suffix+"  "); got != "Research notes" {
					t.Errorf("site branding not removed: %q", got)
				}
			}
			for _, raw := range []string{"", "  ", site.suffix, "New chat", "New conversation", "新对话", "新聊天", "Login", "Log in", "Sign in", "登录", strings.Repeat("界", 201)} {
				if got := webTitle(site.kind, raw); got != "" {
					t.Errorf("non-conversation title %q accepted as %q", raw, got)
				}
			}
			for _, raw := range []string{"Deep research - methods", "你好，世界", strings.Repeat("界", 200)} {
				if got := webTitle(site.kind, raw); got != raw {
					t.Errorf("meaningful title changed: %q", got)
				}
			}
		})
	}
}

func webSitesTestApp(kind string) (*app, *webChatView) {
	a := &app{state: store.State{
		Settings:      store.Settings{Providers: []store.Provider{{ID: "web-provider", Kind: kind}}},
		ActiveID:      "local-chat",
		Conversations: []store.Conversation{{ID: "local-chat", ProviderID: "web-provider", WebKind: kind, Title: "Saved title"}},
	}}
	p := &webChatView{kind: kind, chatID: "local-chat", providerID: "web-provider"}
	a.webChat = p
	return a, p
}

func TestWebSitesCurrentViewRejectsChangedProviderKind(t *testing.T) {
	for _, site := range webSiteFixtures {
		for _, other := range webSiteFixtures {
			if other.kind == site.kind {
				continue
			}
			t.Run(site.kind+"/"+other.kind, func(t *testing.T) {
				a, p := webSitesTestApp(site.kind)
				if !a.currentWebChatView(p) {
					t.Fatal("fresh view not current")
				}
				a.state.Settings.Providers[0].Kind = other.kind
				if a.currentWebChatView(p) {
					t.Fatal("view retained ownership after provider kind changed")
				}
				a.applyWebChatSnapshot(p, webChatSnapshot{URL: other.remote, CreatedURL: other.remote, Title: "Wrong site", Started: true})
				if chat := a.current(); chat.WebURL != "" || chat.WebStarted || chat.Title != "Saved title" || chat.WebKind != site.kind {
					t.Fatalf("old view modified chat after provider kind changed: %+v", chat)
				}
				p.chatID = ""
				a.webSettings, a.providerSettingsID, a.settingsPage = p, p.providerID, "Providers"
				if a.currentWebChatView(p) {
					t.Fatal("settings view retained ownership after provider kind changed")
				}
			})
		}
	}
}

func TestWebSitesSnapshotsRequireOwnRouteAndCreationProof(t *testing.T) {
	for _, site := range webSiteFixtures {
		t.Run(site.kind, func(t *testing.T) {
			invalid := []webChatSnapshot{
				{URL: site.remote, Title: "Existing history"},
				{URL: site.remote, CreatedURL: site.other, Title: "Mismatched proof"},
				{URL: site.start, CreatedURL: site.remote, Title: "Blank route"},
				{URL: site.remote, CreatedURL: site.start, Title: "Invalid proof"},
				{URL: site.remote, CreatedURL: site.remote, Session: 1, Title: "Stale session"},
				{URL: site.remote, CreatedURL: site.remote, Switching: true, Title: "Switch in progress"},
			}
			for _, other := range webSiteFixtures {
				if other.kind != site.kind {
					invalid = append(invalid,
						webChatSnapshot{URL: other.remote, CreatedURL: other.remote, Title: "Cross-site route"},
						webChatSnapshot{URL: site.remote, CreatedURL: other.remote, Title: "Cross-site proof"})
				}
			}
			for _, snapshot := range invalid {
				a, p := webSitesTestApp(site.kind)
				a.applyWebChatSnapshot(p, snapshot)
				if chat := a.current(); chat.WebURL != "" || chat.Title != "Saved title" || len(chat.Messages) != 0 {
					t.Errorf("snapshot imported history or changed title: %+v => %+v", snapshot, chat)
				}
			}
			a, p := webSitesTestApp(site.kind)
			a.applyWebChatSnapshot(p, webChatSnapshot{URL: site.remote + "?source=web#latest", CreatedURL: site.remote, Title: "New research - " + site.suffix})
			chat := a.current()
			if chat.WebURL != site.remote || chat.WebKind != site.kind || chat.Title != "New research" || chat.UpdatedAt.IsZero() || len(chat.Messages) != 0 {
				t.Fatalf("proven creation not bound canonically: %+v", chat)
			}
			for _, raw := range []string{site.other, site.start} {
				a.applyWebChatSnapshot(p, webChatSnapshot{URL: raw, CreatedURL: raw, Title: "Different conversation"})
				if chat.WebURL != site.remote || chat.Title != "New research" {
					t.Fatal("bound identity changed after navigating to another conversation")
				}
				if a.webChatRouteAllowed(p, raw) {
					t.Errorf("bound view allowed another chat route %q", raw)
				}
			}
			if !a.webChatRouteAllowed(p, site.remote) || !a.webChatRouteAllowed(p, site.auth[0]) {
				t.Fatal("bound view blocked its own conversation or authentication")
			}
		})
	}
}

func TestWebSitesOrphanKeepsPersistedIdentity(t *testing.T) {
	for _, site := range webSiteFixtures {
		t.Run(site.kind, func(t *testing.T) {
			a, p := webSitesTestApp(site.kind)
			chat := a.current()
			chat.WebURL = site.remote
			a.state.Settings.Providers = nil
			if !a.isWebChat(chat) || a.chatProviderKind(chat) != site.kind || a.currentWebChatView(p) {
				t.Fatal("removed provider lost persisted site identity or retained view ownership")
			}
			a.applyWebChatSnapshot(p, webChatSnapshot{URL: site.other, CreatedURL: site.other, Title: "Orphan update"})
			if chat.WebURL != site.remote || chat.Title != "Saved title" {
				t.Fatal("orphaned view changed persisted conversation")
			}
			// Reusing a provider ID must not reinterpret an old conversation's site.
			a.state.Settings.Providers = []store.Provider{{ID: p.providerID, Kind: store.ProviderKindDoubao}}
			if !a.isWebChat(chat) || a.chatProviderKind(chat) != site.kind {
				t.Fatal("replacement provider overrode persisted web kind")
			}
		})
	}
}

func TestWebSitesScriptSubmissionAndIsolation(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is needed to execute actual preload behavior")
	}
	var fixtures []map[string]string
	for _, site := range webSiteFixtures {
		for _, scenario := range []string{"trusted Enter", "cross origin", "subframe", "settings"} {
			fixtures = append(fixtures, map[string]string{
				"script": webScript(site.kind, scenario != "settings"), "kind": site.kind,
				"start": site.start, "remote": site.remote, "editor": site.editor, "auth": site.auth[0], "scenario": scenario,
			})
		}
	}
	input, err := json.Marshal(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fixtures.json")
	if err := os.WriteFile(path, input, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "-e", webSitesScriptHarness, path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("actual webScript behavior: %v (context: %v)\n%s", err, ctx.Err(), out)
	}
}

const webSitesScriptHarness = `
const vm = require('node:vm');
const assert = require('node:assert/strict');
const fixtures = JSON.parse(require('node:fs').readFileSync(process.argv[1], 'utf8'));
function runFixture(fixture) {
  const listeners = new Map(), calls = [], styles = [];
  const editor = {isContentEditable:fixture.kind !== 'deepseek-web', children:[], selectors:[fixture.editor],
    value:'Research question', textContent:'Research question', innerText:'Research question',
    contains(target) {return target === this}, closest() {return null}};
  const clipboard = {isContentEditable:true, selectors:['[contenteditable="true"]'], textContent:'Clipboard'};
  const nodes = fixture.kind === 'gemini-web' ? [clipboard, editor] : [editor];
  const document = {title:'Research title', head:null,
    documentElement:{appendChild(style) {style.parentNode = this; styles.push(style)}},
    createElement() {return {}},
    addEventListener(name, callback) {
      if (!listeners.has(name)) listeners.set(name, []);
      listeners.get(name).push(callback);
    },
    querySelector(selector) {
      const options = selector.split(',').map(part => part.trim());
      return nodes.find(node => options.some(part => node.selectors.includes(part) ||
        part === '[contenteditable="true"]' && node.isContentEditable)) || null;
    }};
  const state = {document, URL, setTimeout, navigator:{platform:'Win32'},
    MutationObserver:class {observe() {} disconnect() {}},
    location:new URL(fixture.scenario === 'cross origin' ? fixture.auth : fixture.start),
    addEventListener() {},
    mygo:{call(...args) {calls.push(args); return Promise.resolve()}}};
  state.window = state;
  state.top = fixture.scenario === 'subframe' ? {} : state;
  state.history = {
    pushState(_state, _title, raw) {state.location = new URL(raw, state.location)},
    replaceState(_state, _title, raw) {state.location = new URL(raw, state.location)}
  };
  vm.runInNewContext(fixture.script, state);
  const bridge = state.__copilotWebChat;
  if (fixture.scenario !== 'trusted Enter') {
    assert.equal(bridge, undefined, 'untrusted document installed the bridge');
    assert.equal(styles.length, 0, 'untrusted document was modified');
    assert.equal(calls.length, 0, 'untrusted document called native code');
    return;
  }
  assert.ok(bridge, 'site bridge missing');
  const key = extra => {
    let stopped = false;
    const event = {target:editor, isTrusted:true, key:'Enter', isComposing:false,
      shiftKey:false, ctrlKey:false, metaKey:false, altKey:false, repeat:false,
      preventDefault() {}, stopImmediatePropagation() {stopped = true}, ...extra};
    for (const callback of listeners.get('keydown') || []) {
      callback(event);
      if (stopped) break;
    }
  };
  key({key:'b', ctrlKey:true});
  assert.ok(calls.some(call => call[0] === 'WebChatShortcuts.Invoke' && call[1] === 'sidebar'));
  key({});
  assert.equal(bridge.snapshot().started, false, 'attempted send must not lock the draft');
  assert.equal(bridge.snapshot().createdURL, '');
  state.history.pushState({}, '', fixture.remote + '?source=web#latest');
  assert.equal(bridge.snapshot().createdURL, fixture.remote, 'new conversation was not bound canonically');
}
for (const fixture of fixtures) {
  try { runFixture(fixture); }
  catch (error) {
    console.error(fixture.kind + '/' + fixture.scenario, error);
    process.exitCode = 1;
  }
}
`
