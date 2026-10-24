package desktop

import (
	"os/exec"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/fondoger/copilotchat/internal/store"
)

func doubaoTestApp() (*app, *doubaoView) {
	a := &app{state: store.State{
		Settings:      store.Settings{Providers: []store.Provider{{ID: "doubao", Kind: store.ProviderKindDoubao}}},
		ActiveID:      "local",
		Conversations: []store.Conversation{{ID: "local", ProviderID: "doubao", WebKind: store.ProviderKindDoubao, Title: "New chat"}},
	}}
	p := &doubaoView{chatID: "local", providerID: "doubao"}
	a.doubaoChat = p
	return a, p
}

func TestDoubaoRequiresCreationEvidenceAndNeverRebinds(t *testing.T) {
	a, p := doubaoTestApp()
	remote := store.DoubaoChatURL + "/12345678901234567890"
	a.applyDoubaoSnapshot(p, doubaoSnapshot{URL: remote, Title: "History conversation"})
	chat := a.conversation("local")
	if chat.WebURL != "" || chat.Title != "New chat" {
		t.Fatal("history navigation imported a remote chat")
	}
	if p.err != "" || p.notice == "" {
		t.Fatal("unlinked route must warn without hiding the web view")
	}
	p.notice = ""
	a.applyDoubaoSnapshot(p, doubaoSnapshot{URL: remote, CreatedURL: remote, Title: "Actual new conversation - 豆包"})
	if chat.WebURL != remote || chat.Title != "Actual new conversation" || chat.UpdatedAt.IsZero() || p.notice != "" {
		t.Fatalf("proven creation not bound: %+v", chat)
	}
	boundAt := chat.UpdatedAt
	a.applyDoubaoSnapshot(p, doubaoSnapshot{URL: remote, Title: "Delayed generated title"})
	if chat.Title != "Delayed generated title" || chat.UpdatedAt != boundAt {
		t.Fatal("delayed title was lost or reordered the conversation")
	}
	chat.TitleManual = true
	a.applyDoubaoSnapshot(p, doubaoSnapshot{URL: remote, Title: "Automatic title"})
	if chat.Title != "Delayed generated title" {
		t.Fatal("manual title overwritten")
	}
	foreign := store.DoubaoChatURL + "/999"
	a.applyDoubaoSnapshot(p, doubaoSnapshot{URL: foreign, CreatedURL: foreign, Title: "Foreign title"})
	if chat.WebURL != remote || chat.Title != "Delayed generated title" || p.notice == "" {
		t.Fatal("bound identity changed after foreign navigation")
	}
	p.notice = ""
	a.applyDoubaoSnapshot(p, doubaoSnapshot{URL: store.DoubaoChatURL})
	if p.notice == "" || chat.WebURL != remote {
		t.Fatal("bound view did not fail closed on a website new-chat route")
	}
}

func TestDoubaoRejectsSnapshotsFromStaleOwners(t *testing.T) {
	for _, mutate := range []struct {
		name string
		fn   func(*app, *doubaoView)
	}{
		{"view replaced", func(a *app, _ *doubaoView) { a.doubaoChat = &doubaoView{} }},
		{"chat switched", func(a *app, _ *doubaoView) { a.state.ActiveID = "other" }},
		{"window replaced", func(a *app, _ *doubaoView) { a.win = &mygo.Window{} }},
		{"provider reassigned", func(a *app, _ *doubaoView) { a.state.Conversations[0].ProviderID = "other" }},
		{"provider disabled", func(a *app, _ *doubaoView) { a.state.Settings.Providers[0].Disabled = true }},
		{"provider removed", func(a *app, _ *doubaoView) { a.state.Settings.Providers = nil }},
		{"quit", func(a *app, _ *doubaoView) { a.quitting = true }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			a, p := doubaoTestApp()
			mutate.fn(a, p)
			remote := store.DoubaoChatURL + "/123"
			a.applyDoubaoSnapshot(p, doubaoSnapshot{URL: remote, CreatedURL: remote, Title: "Wrong owner"})
			if a.state.Conversations[0].WebURL != "" || a.state.Conversations[0].Title != "New chat" {
				t.Fatal("stale view wrote to conversation")
			}
		})
	}
	a, _ := doubaoTestApp()
	p := &doubaoView{providerID: "doubao"}
	a.doubaoSettings, a.providerSettingsID, a.settingsPage = p, "doubao", "Providers"
	if !a.currentDoubao(p) {
		t.Fatal("current settings owner not recognized")
	}
	a.settingsPage = "General"
	if a.currentDoubao(p) {
		t.Fatal("settings callback remained current after page switch")
	}
	a.settingsPage = "Providers"
	remote := store.DoubaoChatURL + "/123"
	a.applyDoubaoSnapshot(p, doubaoSnapshot{URL: remote, CreatedURL: remote, Title: "Settings history"})
	if a.state.Conversations[0].WebURL != "" {
		t.Fatal("settings view imported a conversation")
	}
	a.closeDoubaoChat()
	a.closeDoubaoSettings()
	a.focusDoubaoChat()
}

func TestDoubaoTitlesAndNavigationRemainConservative(t *testing.T) {
	a, p := doubaoTestApp()
	chat := a.conversation("local")
	chat.WebURL, chat.Title, chat.UpdatedAt = store.DoubaoChatURL+"/123", "Saved", time.Unix(100, 0)
	for _, title := range []string{"豆包", "New chat", "豆包 - 随时帮你", "登录"} {
		a.applyDoubaoSnapshot(p, doubaoSnapshot{URL: chat.WebURL, Title: title})
		if chat.Title != "Saved" {
			t.Fatalf("generic title replaced saved title: %q", title)
		}
	}
	for _, raw := range []string{"file:///etc/passwd", "javascript:alert(1)", "mygo://app", "https://localhost", "https://127.0.0.1", "https://www.doubao.com.evil.test/chat", "https://user@www.doubao.com/chat", "https://www.doubao.com:443/chat"} {
		if doubaoNavigationURL(raw) {
			t.Fatalf("allowed unsafe navigation: %s", raw)
		}
	}
	if !doubaoNavigationURL(store.DoubaoChatURL) || !doubaoNavigationURL("https://sso.douyin.com/login") {
		t.Fatal("blocked site/login navigation")
	}
	if a.doubaoRouteAllowed(p, store.DoubaoChatURL) || a.doubaoRouteAllowed(p, store.DoubaoChatURL+"/999") || !a.doubaoRouteAllowed(p, chat.WebURL) {
		t.Fatal("bound conversation navigation is not locked")
	}
	// Persisted identity survives a removed provider; routing must not fall back to an API chat.
	a.state.Settings.Providers = nil
	if !a.isDoubaoChat(chat) {
		t.Fatal("removed provider changed web chat identity")
	}
}

func TestDoubaoPreloadCreationAndOriginIsolation(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is needed for preload behavior checks")
	}
	const harness = `
const vm = require('node:vm');
const assert = require('node:assert/strict');
const source = process.argv[1];
function page(initial = 'https://www.doubao.com/chat', framed = false) {
  const events = {}, styles = [];
  const document = {
    title: '豆包', head: {appendChild: x => styles.push(x)},
    createElement: () => ({}),
    addEventListener: (name, fn) => events[name] = fn,
  };
  const calls = [];
  const state = {document, location: new URL(initial), navigator: {platform: 'Win32'},
    mygo: {call: (...args) => {calls.push(args); return Promise.resolve();}},
    addEventListener: (name, fn) => events[name] = fn};
  state.window = state; state.top = framed ? {} : state;
  state.history = {pushState(s, u, raw) {state.location = new URL(raw, state.location)}, replaceState(s,u,raw) {state.location = new URL(raw, state.location)}};
  vm.runInNewContext(source, state);
  return {state, events, styles, calls};
}
{
  const {events, calls} = page();
  let prevented = 0;
  const key = (value, extra = {}) => events.keydown({key:value, ctrlKey:true, isTrusted:true, preventDefault() {prevented++}, stopImmediatePropagation() {}, ...extra});
  key('b'); key('/'); key('Tab', {shiftKey:true}); key('c'); key('v'); key('Enter');
  key('n', {isTrusted:false}); key('n', {isComposing:true}); key('n', {repeat:true});
  assert.equal(JSON.stringify(calls), JSON.stringify([['WebChatShortcuts.Invoke','sidebar'],['WebChatShortcuts.Invoke','models'],['WebChatShortcuts.Invoke','previous-chat']]));
  assert.equal(prevented, 3);
}
for (const [initial, framed] of [['https://sso.douyin.com/login', false], [undefined, true]]) {
  const {state, styles} = page(initial, framed);
  assert.equal(state.__copilotDoubao, undefined); assert.equal(styles.length, 0);
}
{
  const {state} = page();
  state.history.pushState({}, '', '/chat/999');
  assert.equal(state.__copilotDoubao.snapshot().createdURL, '');
}
{
  const {state, styles} = page();
  state.history.pushState({}, '', '/chat/local_5916022949835137');
  assert.equal(state.__copilotDoubao.snapshot().createdURL, '');
  state.history.replaceState({}, '', '/chat/12345678901234567890');
  state.document.title = '实际标题 - 豆包';
  const snapshot = state.__copilotDoubao.snapshot();
  assert.equal(snapshot.createdURL, 'https://www.doubao.com/chat/12345678901234567890');
  assert.equal(snapshot.title, '实际标题 - 豆包');
  state.history.pushState({}, '', '/chat/999');
  assert.equal(state.__copilotDoubao.snapshot().createdURL, snapshot.createdURL);
  vm.runInNewContext(source, state);
  assert.equal(styles.length, 1);
}
{
  const {state} = page();
  state.history.pushState({}, '', '/chat/local_123');
  state.history.replaceState({}, '', '/chat');
  state.history.pushState({}, '', '/chat/999');
  assert.equal(state.__copilotDoubao.snapshot().createdURL, '');
}
`
	cmd := exec.Command(node, "-e", harness, doubaoScript(true, ""))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("preload behavior: %v\n%s", err, out)
	}
}
