package desktop

import (
	"context"
	"encoding/json"
	"os/exec"
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
			if !a.isWebChat(chat) || a.webChatKind(chat) != site.kind || a.currentWebChatView(p) {
				t.Fatal("removed provider lost persisted site identity or retained view ownership")
			}
			a.applyWebChatSnapshot(p, webChatSnapshot{URL: site.other, CreatedURL: site.other, Title: "Orphan update"})
			if chat.WebURL != site.remote || chat.Title != "Saved title" {
				t.Fatal("orphaned view changed persisted conversation")
			}
			// Reusing a provider ID must not reinterpret an old conversation's site.
			a.state.Settings.Providers = []store.Provider{{ID: p.providerID, Kind: store.ProviderKindDoubao}}
			if !a.isWebChat(chat) || a.webChatKind(chat) != site.kind {
				t.Fatal("replacement provider overrode persisted web kind")
			}
		})
	}
}

func TestWebSitesScriptCreationIntent(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is needed to execute actual preload behavior")
	}
	for _, site := range webSiteFixtures {
		for _, scenario := range []string{
			"ordinary edit", "IME", "ShiftEnter", "programmatic Enter", "outside Enter", "empty Enter",
			"SPA roundtrip", "trusted Enter", "temporary route", "rejected submit", "attachment submit", "history back", "history click", "history without intent", "noncanonical route", "editor selection", "cross origin", "subframe", "settings",
		} {
			t.Run(site.kind+"/"+scenario, func(t *testing.T) {
				input, err := json.Marshal(map[string]string{
					"script": webScript(site.kind, scenario != "settings"), "kind": site.kind,
					"start": site.start, "remote": site.remote, "editor": site.editor, "auth": site.auth[0], "scenario": scenario,
				})
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, node, "-e", webSitesScriptHarness)
				cmd.Stdin = strings.NewReader(string(input))
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("actual webScript behavior: %v\n%s", err, out)
				}
			})
		}
	}
}

const webSitesScriptHarness = `
const vm = require('node:vm');
const assert = require('node:assert/strict');
const fixture = JSON.parse(require('node:fs').readFileSync(0, 'utf8'));
// DOM dispatch must preserve every listener: the shortcut and creation handlers
// both subscribe to keydown, in registration order.
function events() {
  const listeners = new Map();
  return {
    addEventListener(name, callback) {
      if (!listeners.has(name)) listeners.set(name, []);
      listeners.get(name).push(callback);
    },
    dispatch(type, target, extra = {}) {
      let stopped = false;
      const event = {type, target, isTrusted:true, key:'', isComposing:false,
        shiftKey:false, ctrlKey:false, metaKey:false, altKey:false, repeat:false,
        preventDefault() {}, stopImmediatePropagation() {stopped = true}, ...extra};
      for (const callback of listeners.get(type) || []) {
        callback(event);
        if (stopped) break;
      }
    }
  };
}
function node(contentEditable, selectors, value) {
  return {isContentEditable:contentEditable, children:[], selectors,
    value, textContent:value, innerText:value,
    contains(target) {return target === this}, closest() {return null}};
}
let editor = node(fixture.kind !== 'deepseek-web', [fixture.editor], 'Research question');
// Gemini also has a hidden contenteditable clipboard before the real composer.
const clipboard = node(true, ['[contenteditable="true"]'], 'Clipboard, not the message');
const nodes = fixture.kind === 'gemini-web' ? [clipboard, editor] : [editor];
const documentEvents = events(), windowEvents = events(), styles = [], calls = [];
const document = {title:'Research title', head:{appendChild(style) {styles.push(style)}},
  createElement() {return {}}, addEventListener:documentEvents.addEventListener,
  querySelector(selector) {
    const options = selector.split(',').map(part => part.trim());
    return nodes.find(node => options.some(part => node.selectors.includes(part) ||
      part === '[contenteditable="true"]' && node.isContentEditable)) || null;
  }};
const outside = {closest() {return null}};
const state = {document, URL, setTimeout, navigator:{platform:'Win32'},
  location:new URL(fixture.scenario === 'cross origin' ? fixture.auth : fixture.start),
  addEventListener:windowEvents.addEventListener,
  mygo:{call(...args) {calls.push(args); return Promise.resolve()}}};
state.window = state;
state.top = fixture.scenario === 'subframe' ? {} : state;
state.history = {
  pushState(_state, _title, raw) {state.location = new URL(raw, state.location)},
  replaceState(_state, _title, raw) {state.location = new URL(raw, state.location)}
};
vm.runInNewContext(fixture.script, state);
const bridge = state.__copilotWebChat;
if (['cross origin', 'subframe', 'settings'].includes(fixture.scenario)) {
  assert.equal(bridge, undefined, 'untrusted document installed the bridge');
  assert.equal(styles.length, 0, 'untrusted document was modified');
  assert.equal(calls.length, 0);
} else {
  assert.ok(bridge, 'site bridge missing');
  const snapshot = () => bridge.snapshot();
  const key = (extra = {}, target = editor) => documentEvents.dispatch('keydown', target, {key:'Enter', ...extra});
  const navigate = raw => state.history.pushState({}, '', raw);
  const noBinding = () => {
    assert.equal(snapshot().createdURL, '', 'navigation acquired creation proof without explicit send');
    assert.equal(snapshot().started, false, 'ordinary interaction marked a send');
  };
  assert.equal(snapshot().draft, null);
  switch (fixture.scenario) {
    case 'SPA roundtrip': {
      const control = (text, href, label, destination) => ({textContent:text, getAttribute(name) {return name === 'href' ? href : name === 'aria-label' ? label : null}, click() {editor = node(editor.isContentEditable, [fixture.editor], ''); nodes.splice(0, nodes.length, editor); navigate(destination)}});
      const blankPath = new URL(fixture.start).pathname;
      const remotePath = new URL(fixture.remote).pathname;
      const fresh = control(fixture.kind === 'claude-web' ? 'New' : 'New chat', blankPath, 'New chat', fixture.start);
      const history = control('Saved chat', remotePath, null, fixture.remote);
      const decoy = control('', blankPath, 'Home', fixture.start);
      document.documentElement = {};
      document.querySelectorAll = selector => selector === 'a[href]' ? [fresh,history] : fixture.kind === 'claude-web' || fixture.kind === 'chatgpt-web' ? [fresh,decoy] : [fresh];
      editor.value = editor.textContent = editor.innerText = '';
      (async () => {
        assert.equal(await bridge.switchTarget(1, fixture.remote), true);
        assert.equal(snapshot().session, 1);
        assert.equal(await bridge.switchTarget(2, fixture.start), true);
        assert.equal(snapshot().session, 2);
        assert.equal(snapshot().createdURL, '');
        assert.equal(document.documentElement.inert, false);
      })().catch(error => {console.error(error);process.exitCode=1});
      break;
    }
    case 'ordinary edit':
      documentEvents.dispatch('input', editor);
      assert.equal(snapshot().draft, 'Research question');
      assert.equal(snapshot().edited, true);
      navigate(fixture.remote); noBinding(); break;
    case 'IME':
      key({isComposing:true});
      documentEvents.dispatch('compositionend', editor);
      assert.equal(snapshot().draft, 'Research question');
      navigate(fixture.remote); noBinding(); break;
    case 'ShiftEnter':
      key({shiftKey:true}); navigate(fixture.remote); noBinding(); break;
    case 'programmatic Enter':
      key({isTrusted:false}); navigate(fixture.remote); noBinding(); break;
    case 'outside Enter':
      key({}, outside); navigate(fixture.remote); noBinding(); break;
    case 'empty Enter':
      editor.value = editor.textContent = editor.innerText = ' \n ';
      key(); navigate(fixture.remote); noBinding(); break;
    case 'trusted Enter':
      // Also prove that the earlier keydown shortcut listener was not overwritten.
      documentEvents.dispatch('keydown', editor, {key:'b', ctrlKey:true});
      assert.ok(calls.some(call => call[0] === 'WebChatShortcuts.Invoke' && call[1] === 'sidebar'));
      key();
      assert.equal(snapshot().started, false, 'attempted send must not lock the draft');
      assert.equal(snapshot().createdURL, '');
      navigate(fixture.remote + '?source=web#latest');
      assert.equal(snapshot().createdURL, fixture.remote, 'send-to-server transition did not produce canonical proof');
      break;
    case 'temporary route':
      key();
      if (fixture.kind === 'chatgpt-web') navigate('/c/local-chatgpt%3A047fe4f3-f273-4802-a716-76522d574de3');
      navigate(fixture.remote);
      assert.equal(snapshot().createdURL, fixture.remote);
      break;
    case 'rejected submit':
      key();
      editor.value = editor.textContent = editor.innerText = 'Edited after rejection';
      documentEvents.dispatch('input', editor);
      assert.equal(snapshot().started, false);
      assert.equal(snapshot().draft, 'Edited after rejection');
      break;
    case 'attachment submit': {
      editor.value = editor.textContent = editor.innerText = '';
      const send = {closest(selector) {return selector.includes('button[type="submit"]') ? this : null}};
      documentEvents.dispatch('click', send);
      navigate(fixture.remote);
      assert.equal(snapshot().createdURL, fixture.remote);
      break;
    }
    case 'history back':
      key();
      state.location = new URL(fixture.remote);
      windowEvents.dispatch('popstate', outside);
      noBinding();
      break;
    case 'history click': {
      key();
      const link = {closest(selector) {return selector.includes('a[href]') ? this : null}};
      const iconInsideLink = {closest:selector => link.closest(selector)};
      documentEvents.dispatch('click', iconInsideLink);
      navigate(fixture.remote);
      assert.equal(snapshot().createdURL, '', 'history click reused pending send intent');
      break;
    }
    case 'history without intent':
      navigate(fixture.remote); noBinding(); break;
    case 'noncanonical route':
      key(); navigate(fixture.remote + '/extra');
      assert.equal(snapshot().createdURL, '');
      navigate(fixture.remote);
      assert.equal(snapshot().createdURL, '', 'non-chat navigation retained send intent');
      break;
    case 'editor selection':
      documentEvents.dispatch('input', editor);
      assert.equal(snapshot().draft, 'Research question', 'composer selector captured hidden clipboard or missed editor');
      key(); navigate(fixture.remote);
      assert.equal(snapshot().createdURL, fixture.remote);
      break;
    default: throw new Error('unknown fixture scenario');
  }
}
`
