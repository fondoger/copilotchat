package desktop

import (
	"context"
	_ "embed"
	"encoding/json"
	"log"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/fondoger/copilotchat/internal/store"
)

//go:embed web_chat.js
var webChatPreload string

// All fields except the immutable worker inputs are owned by the UI thread.
// There is at most one chat view and one settings view, each in its own window.
type webChatView struct {
	view               *mygo.WebView
	owner              *mygo.Window
	chatID, providerID string
	kind               string
	err, notice        string
	cancel             context.CancelFunc
	off                []func()
	started            time.Time
	focusPending       bool
	ready              bool
	nextURL            string
	idleTimer          *time.Timer
	idleGeneration     uint64
	wake               chan struct{}
	generation         uint64
	startedChat        bool
	resetDraft         bool
	switching          bool
	fallback           bool
	session            uint64
}

type webChatSnapshot struct {
	Session    uint64  `json:"session"`
	Switching  bool    `json:"switching"`
	URL        string  `json:"url"`
	Title      string  `json:"title"`
	CreatedURL string  `json:"createdURL"`
	Started    bool    `json:"started"`
	Edited     bool    `json:"edited"`
	Draft      *string `json:"draft"`
}

func (p *webChatView) trace(stage string) {
	if os.Getenv("COPILOT_CHAT_WEBVIEW_TIMING") == "1" {
		log.Printf("web-chat %s chat=%s elapsed=%s", stage, p.chatID, time.Since(p.started).Round(time.Millisecond))
	}
}

func destroyWebChatView(p *webChatView) {
	if p == nil {
		return
	}
	if p.idleTimer != nil {
		p.idleTimer.Stop()
	}
	if p.cancel != nil {
		p.cancel()
	}
	for _, off := range p.off {
		off()
	}
	if p.view != nil {
		p.view.Destroy()
	}
}

func (a *app) parkWebChat() {
	if a.webChat == nil {
		return
	}
	destroyWebChatView(a.webParked)
	p := a.webChat
	a.webParked, a.webChat = p, nil
	p.focusPending = false
	p.idleGeneration++
	generation := p.idleGeneration
	p.idleTimer = time.AfterFunc(5*time.Minute, func() {
		a.update(func() {
			if p.idleGeneration == generation {
				a.expireWebChat(p)
			}
		})
	})
}

func (a *app) expireWebChat(p *webChatView) {
	if a.webParked == p {
		a.webParked = nil
		destroyWebChatView(p)
	}
}

func (a *app) closeWebChat() {
	p := a.webChat
	a.webChat = nil
	destroyWebChatView(p)
}

func (a *app) closeWebSettings() {
	p := a.webSettings
	a.webSettings = nil
	destroyWebChatView(p)
}

func (a *app) closeWebProvider(id string) {
	if a.webParked != nil && a.webParked.providerID == id {
		destroyWebChatView(a.webParked)
		a.webParked = nil
	}
	if a.webChat != nil && a.webChat.providerID == id {
		a.closeWebChat()
	}
	if a.webSettings != nil && a.webSettings.providerID == id {
		a.closeWebSettings()
	}
}

func (a *app) focusWebChat() {
	if p := a.webChat; p != nil && a.currentWebChatView(p) && p.view != nil && !p.view.IsDestroyed() {
		p.view.Focus()
	}
}

// Empty web placeholders are useful within a session, but not after restarting.
func (a *app) pruneEmptyWebChats() {
	kept := a.state.Conversations[:0]
	removed := false
	for _, chat := range a.state.Conversations {
		if a.isWebChat(&chat) && chat.WebURL == "" && !chat.WebStarted && len(chat.Messages) == 0 && !chat.TitleManual && !chat.Favorite && (chat.Title == "New Chat" || chat.Title == "") {
			removed = true
			if a.state.ActiveID == chat.ID {
				a.state.ActiveID = ""
			}
			continue
		}
		kept = append(kept, chat)
	}
	a.state.Conversations = kept
	if removed {
		a.save()
	}
}

func (a *app) isWebChat(chat *store.Conversation) bool {
	if chat == nil {
		return false
	}
	if chat.WebKind != "" {
		return store.IsWebProvider(chat.WebKind)
	}
	p := a.provider(chat.ProviderID)
	return p != nil && store.IsWebProvider(p.Kind)
}

// A disabled provider can resume a bound local conversation, but cannot open a
// new chat or a settings sign-in view. Removed or repurposed providers never own
// historical views, even when the conversation still has its saved web identity.
func (a *app) webProviderAvailable(providerID string, chat *store.Conversation) bool {
	provider := a.provider(providerID)
	if provider == nil || !store.IsWebProvider(provider.Kind) {
		return false
	}
	if chat != nil && (chat.ProviderID != providerID || a.webChatKind(chat) != provider.Kind) {
		return false
	}
	if !provider.Disabled {
		return true
	}
	if chat == nil {
		return false
	}
	_, resumable := store.WebConversationURL(provider.Kind, chat.WebURL)
	return resumable
}

func (a *app) currentWebChatView(p *webChatView) bool {
	if p == nil || a.quitting {
		return false
	}
	provider := a.provider(p.providerID)
	if provider == nil || (p.kind != "" && p.kind != provider.Kind) {
		return false
	}
	if p.chatID == "" {
		return a.webProviderAvailable(p.providerID, nil) && a.webSettings == p && a.settingsWin == p.owner && a.settingsPage == "Providers" && a.providerSettingsID == p.providerID
	}
	chat := a.conversation(p.chatID)
	return chat != nil && a.webProviderAvailable(p.providerID, chat) && a.webChat == p && a.win == p.owner && a.state.ActiveID == p.chatID
}

// Authentication may navigate to another HTTPS origin, but never to a native
// protocol, local resource, privileged page, or loopback service. Popups reuse
// this owned view rather than launching the external browser.
func (a *app) newWebChatView(owner *mygo.Window, providerID, chatID, target string) *webChatView {
	provider := a.provider(providerID)
	kind := ""
	if provider != nil {
		kind = provider.Kind
	}
	site, _ := store.WebProviderFor(kind)
	p := &webChatView{owner: owner, providerID: providerID, chatID: chatID, kind: kind, started: time.Now(), focusPending: chatID != ""}
	if owner == nil {
		p.err = "Web chat requires a desktop window."
		return p
	}
	preload := ""
	if chatID != "" {
		preload = webScript(kind, true)
	}
	pageOptions := mygo.PageOptions{PreloadScript: preload, DevTools: mygo.DevToolsDisabled}
	if chatID != "" {
		pageOptions.TrustedOrigins = []string{"https://" + site.Host}
	}
	view, err := owner.NewWebView(mygo.WebViewOptions{
		Page:            pageOptions,
		BackgroundColor: "#ffffff",
	})
	if err != nil {
		p.err = "Could not open " + p.siteName() + ": " + err.Error()
		return p
	}
	p.view = view
	page := view.Page()
	p.trace("view-created")
	p.off = append(p.off,
		owner.OnFocus(func() {
			a.update(func() {
				if p.ready && p.focusPending && a.currentWebChatView(p) && a.settingsWin == nil && !a.searchOpen {
					p.focusPending = false
					p.view.Focus()
				}
			})
		}),
		page.OnDOMReady(func() {
			p.trace("dom-ready")
			a.update(func() {
				// A real document navigation reinstalls the preload with session zero.
				if !p.switching {
					p.session = 0
					if p.cancel != nil {
						p.cancel()
						p.cancel = nil
					}
				}
				if p.nextURL == "" && p.focusPending && a.currentWebChatView(p) && a.settingsWin == nil && !a.searchOpen && !a.renameOpen && !a.deleteOpen {
					p.view.Focus()
				}
			})
		}),
		page.OnDidFinishLoad(func() {
			p.trace("load-finished")
			a.update(func() {
				if p.nextURL != "" {
					if p.fallback && page.URL() == "about:blank" {
						p.fallback = false
						p.session = 0
						target := p.nextURL
						p.nextURL = ""
						_ = page.LoadURL(target)
					}
					return
				}
				if p.switching {
					return
				}
				p.ready = true
				if p.chatID != "" && p.cancel == nil && a.currentWebChatView(p) {
					ctx, cancel := context.WithCancel(context.Background())
					p.cancel = cancel
					go a.pollWebChat(ctx, p, page, p.wake, a.draft, p.generation, p.session)
				}
				if p.wake != nil {
					select {
					case p.wake <- struct{}{}:
					default:
					}
				}
				if p.nextURL == "" && p.focusPending && a.currentWebChatView(p) && a.settingsWin == nil && !a.searchOpen && !a.renameOpen && !a.deleteOpen {
					p.view.Focus()
					p.focusPending = !owner.IsFocused()
				}
			})
		}),
	)
	page.SetPermissionHandler(func(mygo.PermissionRequest) bool { return false })
	page.OnWillDownload(func(e *mygo.DownloadEvent) { e.PreventDefault() })
	page.SetWindowOpenHandler(func(request mygo.WindowOpenRequest) *mygo.WindowOptions {
		if webNavigationURL(p.kind, request.URL) {
			a.update(func() {
				if a.currentWebChatView(p) && a.webChatRouteAllowed(p, request.URL) {
					_ = page.LoadURL(request.URL)
				}
			})
		}
		return nil
	})
	p.off = append(p.off, page.OnWillNavigate(func(e *mygo.NavigateEvent) {
		if !webNavigationURL(p.kind, e.URL) {
			e.PreventDefault()
			return
		}
		// These callbacks arrive on the native UI thread. Do not defer a
		// preventable event: the decision must precede its return.
		if !a.currentWebChatView(p) || !a.webChatRouteAllowed(p, e.URL) {
			e.PreventDefault()
		}
	}))
	failed := func(message string) {
		a.update(func() {
			if a.currentWebChatView(p) {
				p.err = message
			}
		})
	}
	p.off = append(p.off,
		page.OnDidFailLoad(func(err *mygo.LoadError) {
			generation := p.generation
			a.update(func() {
				if a.currentWebChatView(p) && p.generation == generation && !webNavigationAborted(runtime.GOOS, err.Code) {
					p.err = "Could not load " + p.siteName() + ": " + err.Description
				}
			})
		}),
		page.OnRenderProcessGone(func(string) { failed("The " + p.siteName() + " web process stopped.") }),
	)
	if chatID != "" {
		ctx, cancel := context.WithCancel(context.Background())
		p.cancel = cancel
		wake := make(chan struct{}, 1)
		p.wake = wake
		p.off = append(p.off, page.OnPageTitleUpdated(func(*mygo.TitleEvent) {
			select {
			case wake <- struct{}{}:
			default:
			}
		}))
		// The worker captures only a page and its lifetime, never reads app state.
		go a.pollWebChat(ctx, p, page, wake, a.draft, p.generation, p.session)
	}
	if err := page.LoadURL(target); err != nil {
		p.err = "Could not load " + p.siteName() + ": " + err.Error()
	}
	return p
}

// Replacing an in-flight navigation can report an abort after the next one starts.
// WebView2 uses its own positive status codes rather than Chromium's net errors.
func webNavigationAborted(platform string, code int) bool {
	return code == -3 || platform == "windows" && (code == 9 || code == 13)
}

// Capture before changing the active local identity; input notifications are async.
func (a *app) captureWebChatDeparture(next func()) {
	a.modelSelection++
	request := a.modelSelection
	p := a.webChat
	if p == nil || p.view == nil || p.switching || !a.currentWebChatView(p) {
		next()
		return
	}
	generation, page := p.generation, p.view.Page()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		snapshot, err := readWebChatSnapshot(ctx, page)
		a.update(func() {
			if a.modelSelection != request || p.generation != generation || !a.currentWebChatView(p) {
				return
			}
			if err == nil {
				a.applyWebChatSnapshot(p, snapshot)
			}
			next()
		})
	}()
}

func (a *app) retargetWebChat(p *webChatView, chat *store.Conversation) {
	if p.cancel != nil {
		p.cancel()
	}
	p.generation++
	p.startedChat, p.resetDraft = false, false
	p.chatID, p.providerID = chat.ID, chat.ProviderID
	p.err, p.notice, p.ready = "", "", false
	p.started, p.focusPending = time.Now(), true
	site, _ := store.WebProviderFor(a.webChatKind(chat))
	p.nextURL = site.StartURL
	if chat.WebURL != "" {
		p.nextURL = chat.WebURL
	}
	p.cancel = nil
	p.switching = true
	p.fallback = false
	generation, target, draft := p.generation, p.nextURL, a.draft
	page := p.view.Page()
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	go func() {
		evalCtx, done := context.WithTimeout(ctx, 8*time.Second)
		encoded, _ := json.Marshal(target)
		value, err := page.EvalContext(evalCtx, `window.__copilotWebChat?.switchTarget(`+strconv.FormatUint(generation, 10)+`,`+string(encoded)+`)`)
		done()
		a.update(func() {
			if ctx.Err() != nil || p.generation != generation || !a.currentWebChatView(p) {
				return
			}
			cancel()
			p.cancel = nil
			p.switching = false
			if err != nil || value != true {
				p.session = 0
				p.fallback = true
				if err := page.LoadURL("about:blank"); err != nil {
					p.err = "Could not reset " + p.siteName() + ": " + err.Error()
				}
				p.trace("reset-to-blank")
				return
			}
			p.nextURL, p.session, p.ready = "", generation, true
			pollCtx, stop := context.WithCancel(context.Background())
			p.cancel = stop
			go a.pollWebChat(pollCtx, p, page, p.wake, draft, generation, generation)
			select {
			case p.wake <- struct{}{}:
			default:
			}
			if p.focusPending && a.settingsWin == nil && !a.searchOpen && !a.renameOpen && !a.deleteOpen {
				p.view.Focus()
				p.focusPending = !p.owner.IsFocused()
			}
			p.trace("spa-switched")
		})
	}()
}

func (a *app) webChatRouteAllowed(p *webChatView, raw string) bool {
	if p.chatID == "" {
		return true
	}
	chat := a.conversation(p.chatID)
	if chat == nil || chat.WebURL == "" {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	site, _ := store.WebProviderFor(a.webChatKind(chat))
	remote, conversation := store.WebConversationURL(site.Kind, raw)
	if u.Host == site.Host && (store.WebBlankURL(site.Kind, raw) || conversation || site.Kind == store.ProviderKindDoubao && strings.HasPrefix(u.Path, "/chat/")) {
		return conversation && remote == chat.WebURL
	}
	return true // HTTPS authentication pages are allowed by the outer policy.
}

func readWebChatSnapshot(ctx context.Context, page *mygo.Page) (webChatSnapshot, error) {
	var snapshot webChatSnapshot
	value, err := page.EvalContext(ctx, `JSON.stringify(window.__copilotWebChat?.snapshot() || null)`)
	if err != nil {
		return snapshot, err
	}
	raw, ok := value.(string)
	if !ok || raw == "null" {
		return snapshot, context.Canceled
	}
	err = json.Unmarshal([]byte(raw), &snapshot)
	return snapshot, err
}

func (a *app) pollWebChat(ctx context.Context, p *webChatView, page *mygo.Page, wake <-chan struct{}, draft string, generation, session uint64) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	encoded, _ := json.Marshal(draft)
	attempts := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-wake:
		}
		evalCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		if attempts < 10 {
			value, _ := page.EvalContext(evalCtx, `window.__copilotWebChat?.restoreDraft(`+string(encoded)+`,`+strconv.FormatUint(session, 10)+`)`)
			attempts++
			if value == true {
				attempts = 10
			}
		}
		snapshot, err := readWebChatSnapshot(evalCtx, page)
		cancel()
		if err != nil || ctx.Err() != nil {
			continue
		}
		a.update(func() {
			if ctx.Err() == nil && p.nextURL == "" && p.generation == generation {
				a.applyWebChatSnapshot(p, snapshot)
			}
		})
	}
}

// CreatedURL requires the local_<id> → server ID transition in this chat view.
// Settings views never import; a bound record never changes identity.
func (a *app) applyWebChatSnapshot(p *webChatView, snapshot webChatSnapshot) {
	if !a.currentWebChatView(p) || p.chatID == "" || p.switching || snapshot.Switching || snapshot.Session != p.session {
		return
	}
	chat := a.conversation(p.chatID)
	if !chat.WebStarted && (snapshot.Started || snapshot.Edited || snapshot.CreatedURL != "" || snapshot.Draft != nil && *snapshot.Draft != "") {
		chat.WebStarted = true
		a.save()
	}
	p.startedChat = p.startedChat || snapshot.Started || snapshot.CreatedURL != ""
	if chat.WebURL == "" && !p.startedChat && store.WebBlankURL(a.webChatKind(chat), snapshot.URL) && snapshot.Draft != nil {
		if snapshot.Edited || a.draft != *snapshot.Draft {
			a.attachments = nil
		}
		a.draft = *snapshot.Draft
		a.rememberDraft()
	}
	remote, ok := store.WebConversationURL(a.webChatKind(chat), snapshot.URL)
	if !ok {
		if chat.WebURL != "" && store.WebBlankURL(a.webChatKind(chat), snapshot.URL) {
			p.notice = "This local chat is linked to another website conversation."
		}
		return
	}
	changed := false
	if chat.WebURL == "" {
		created, proven := store.WebConversationURL(a.webChatKind(chat), snapshot.CreatedURL)
		if !proven || remote != created {
			p.notice = "This website conversation could not be linked to this local chat."
			return
		}
		chat.WebKind, chat.WebURL, chat.UpdatedAt = a.webChatKind(chat), remote, time.Now()
		changed = true
	} else if chat.WebURL != remote {
		p.notice = "This local chat is linked to another website conversation."
		return
	}
	p.notice = ""
	if title := webTitle(a.webChatKind(chat), snapshot.Title); !chat.TitleManual && title != "" && title != chat.Title {
		chat.Title = title
		changed = true
	}
	if changed {
		a.save()
	}
}
