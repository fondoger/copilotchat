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
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/store"
)

//go:embed doubao.js
var doubaoPreload string

// All fields except the immutable worker inputs are owned by the UI thread.
// There is at most one chat view and one settings view, each in its own window.
type doubaoView struct {
	view               *mygo.WebView
	owner              *mygo.Window
	chatID, providerID string
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

type doubaoSnapshot struct {
	Session    uint64  `json:"session"`
	Switching  bool    `json:"switching"`
	URL        string  `json:"url"`
	Title      string  `json:"title"`
	CreatedURL string  `json:"createdURL"`
	Started    bool    `json:"started"`
	Edited     bool    `json:"edited"`
	Draft      *string `json:"draft"`
}

func (p *doubaoView) trace(stage string) {
	if os.Getenv("COPILOT_CHAT_WEBVIEW_TIMING") == "1" {
		log.Printf("web-chat %s chat=%s elapsed=%s", stage, p.chatID, time.Since(p.started).Round(time.Millisecond))
	}
}

func destroyDoubao(p *doubaoView) {
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

func (a *app) parkDoubaoChat() {
	if a.doubaoChat == nil {
		return
	}
	destroyDoubao(a.doubaoParked)
	p := a.doubaoChat
	a.doubaoParked, a.doubaoChat = p, nil
	p.focusPending = false
	p.idleGeneration++
	generation := p.idleGeneration
	p.idleTimer = time.AfterFunc(5*time.Minute, func() {
		a.update(func() {
			if p.idleGeneration == generation {
				a.expireDoubao(p)
			}
		})
	})
}

func (a *app) expireDoubao(p *doubaoView) {
	if a.doubaoParked == p {
		a.doubaoParked = nil
		destroyDoubao(p)
	}
}

func (a *app) closeDoubaoChat() {
	p := a.doubaoChat
	a.doubaoChat = nil
	destroyDoubao(p)
}

func (a *app) closeDoubaoSettings() {
	p := a.doubaoSettings
	a.doubaoSettings = nil
	destroyDoubao(p)
}

func (a *app) closeDoubaoProvider(id string) {
	if a.doubaoParked != nil && a.doubaoParked.providerID == id {
		destroyDoubao(a.doubaoParked)
		a.doubaoParked = nil
	}
	if a.doubaoChat != nil && a.doubaoChat.providerID == id {
		a.closeDoubaoChat()
	}
	if a.doubaoSettings != nil && a.doubaoSettings.providerID == id {
		a.closeDoubaoSettings()
	}
}

func (a *app) focusDoubaoChat() {
	if p := a.doubaoChat; p != nil && a.currentDoubao(p) && p.view != nil && !p.view.IsDestroyed() {
		p.view.Focus()
	}
}

// Empty web placeholders are useful within a session, but not after restarting.
func (a *app) pruneEmptyWebChats() {
	kept := a.state.Conversations[:0]
	removed := false
	for _, chat := range a.state.Conversations {
		if a.isDoubaoChat(&chat) && chat.WebURL == "" && !chat.WebStarted && len(chat.Messages) == 0 && !chat.TitleManual && !chat.Favorite && (chat.Title == "New Chat" || chat.Title == "") {
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

func (a *app) isDoubaoChat(chat *store.Conversation) bool {
	if chat == nil {
		return false
	}
	if chat.WebKind != "" {
		return chat.WebKind == store.ProviderKindDoubao
	}
	p := a.provider(chat.ProviderID)
	return p != nil && p.Kind == store.ProviderKindDoubao
}

func (a *app) currentDoubao(p *doubaoView) bool {
	if p == nil || a.quitting {
		return false
	}
	provider := a.provider(p.providerID)
	if provider == nil || provider.Disabled || provider.Kind != store.ProviderKindDoubao {
		return false
	}
	if p.chatID == "" {
		return a.doubaoSettings == p && a.settingsWin == p.owner && a.settingsPage == "Providers" && a.providerSettingsID == p.providerID
	}
	chat := a.conversation(p.chatID)
	return a.doubaoChat == p && a.win == p.owner && a.state.ActiveID == p.chatID && chat != nil && chat.ProviderID == p.providerID && a.isDoubaoChat(chat)
}

// Authentication may navigate to another HTTPS origin, but never to a native
// protocol, local resource, privileged page, or loopback service. Popups reuse
// this owned view rather than launching the external browser.
func doubaoNavigationURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.Port() != "" || u.Hostname() == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	// Keep the authentication surface on the site's own production domains.
	return host == store.DoubaoHost || host == "doubao.com" || strings.HasSuffix(host, ".doubao.com") || host == "sso.douyin.com" || host == "passport.douyin.com" || host == "sso.snssdk.com"
}

func doubaoScript(chat bool, bound string) string {
	config, _ := json.Marshal(struct {
		Host  string `json:"host"`
		Chat  bool   `json:"chat"`
		Bound string `json:"bound"`
	}{store.DoubaoHost, chat, bound})
	return strings.Replace(doubaoPreload, "__DOUBAO_CONFIG__", string(config), 1)
}

func (a *app) newDoubaoView(owner *mygo.Window, providerID, chatID, target string) *doubaoView {
	p := &doubaoView{owner: owner, providerID: providerID, chatID: chatID, started: time.Now(), focusPending: chatID != ""}
	if owner == nil {
		p.err = "Doubao requires a desktop window."
		return p
	}
	bound, _ := store.DoubaoConversationURL(target)
	preload := ""
	if chatID != "" {
		preload = doubaoScript(true, bound)
	}
	pageOptions := mygo.PageOptions{PreloadScript: preload, DevTools: mygo.DevToolsDisabled}
	if chatID != "" {
		pageOptions.TrustedOrigins = []string{"https://" + store.DoubaoHost}
	}
	view, err := owner.NewWebView(mygo.WebViewOptions{
		Page:            pageOptions,
		BackgroundColor: "#ffffff",
	})
	if err != nil {
		p.err = "Could not open Doubao: " + err.Error()
		return p
	}
	p.view = view
	page := view.Page()
	p.trace("view-created")
	p.off = append(p.off,
		owner.OnFocus(func() {
			a.update(func() {
				if p.ready && p.focusPending && a.currentDoubao(p) && a.settingsWin == nil && !a.searchOpen {
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
				if p.nextURL == "" && p.focusPending && a.currentDoubao(p) && a.settingsWin == nil && !a.searchOpen && !a.renameOpen && !a.deleteOpen {
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
				if p.chatID != "" && p.cancel == nil && a.currentDoubao(p) {
					ctx, cancel := context.WithCancel(context.Background())
					p.cancel = cancel
					go a.pollDoubao(ctx, p, page, p.wake, a.draft, p.generation, p.session)
				}
				if p.wake != nil {
					select {
					case p.wake <- struct{}{}:
					default:
					}
				}
				if p.nextURL == "" && p.focusPending && a.currentDoubao(p) && a.settingsWin == nil && !a.searchOpen && !a.renameOpen && !a.deleteOpen {
					p.view.Focus()
					p.focusPending = !owner.IsFocused()
				}
			})
		}),
	)
	page.SetPermissionHandler(func(mygo.PermissionRequest) bool { return false })
	page.OnWillDownload(func(e *mygo.DownloadEvent) { e.PreventDefault() })
	page.SetWindowOpenHandler(func(request mygo.WindowOpenRequest) *mygo.WindowOptions {
		if doubaoNavigationURL(request.URL) {
			a.update(func() {
				if a.currentDoubao(p) && a.doubaoRouteAllowed(p, request.URL) {
					_ = page.LoadURL(request.URL)
				}
			})
		}
		return nil
	})
	p.off = append(p.off, page.OnWillNavigate(func(e *mygo.NavigateEvent) {
		if !doubaoNavigationURL(e.URL) {
			e.PreventDefault()
			return
		}
		// These callbacks arrive on the native UI thread. Do not defer a
		// preventable event: the decision must precede its return.
		if !a.currentDoubao(p) || !a.doubaoRouteAllowed(p, e.URL) {
			e.PreventDefault()
		}
	}))
	failed := func(message string) {
		a.update(func() {
			if a.currentDoubao(p) {
				p.err = message
			}
		})
	}
	p.off = append(p.off,
		page.OnDidFailLoad(func(err *mygo.LoadError) {
			generation := p.generation
			a.update(func() {
				if a.currentDoubao(p) && p.generation == generation && !doubaoNavigationAborted(runtime.GOOS, err.Code) {
					p.err = "Could not load Doubao: " + err.Description
				}
			})
		}),
		page.OnRenderProcessGone(func(string) { failed("The Doubao web process stopped.") }),
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
		go a.pollDoubao(ctx, p, page, wake, a.draft, p.generation, p.session)
	}
	if err := page.LoadURL(target); err != nil {
		p.err = "Could not load Doubao: " + err.Error()
	}
	return p
}

// Replacing an in-flight navigation can report an abort after the next one starts.
// WebView2 uses its own positive status codes rather than Chromium's net errors.
func doubaoNavigationAborted(platform string, code int) bool {
	return code == -3 || platform == "windows" && (code == 9 || code == 13)
}

// Capture before changing the active local identity; input notifications are async.
func (a *app) captureDoubaoDeparture(next func()) {
	a.modelSelection++
	request := a.modelSelection
	p := a.doubaoChat
	if p == nil || p.view == nil || p.switching || !a.currentDoubao(p) {
		next()
		return
	}
	generation, page := p.generation, p.view.Page()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		snapshot, err := readDoubaoSnapshot(ctx, page)
		a.update(func() {
			if a.modelSelection != request || p.generation != generation || !a.currentDoubao(p) {
				return
			}
			if err == nil {
				a.applyDoubaoSnapshot(p, snapshot)
			}
			next()
		})
	}()
}

func (a *app) retargetDoubao(p *doubaoView, chat *store.Conversation) {
	if p.cancel != nil {
		p.cancel()
	}
	p.generation++
	p.startedChat, p.resetDraft = false, false
	p.chatID, p.providerID = chat.ID, chat.ProviderID
	p.err, p.notice, p.ready = "", "", false
	p.started, p.focusPending = time.Now(), true
	p.nextURL = store.DoubaoChatURL
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
		value, err := page.EvalContext(evalCtx, `window.__copilotDoubao?.switchTarget(`+strconv.FormatUint(generation, 10)+`,`+string(encoded)+`)`)
		done()
		a.update(func() {
			if ctx.Err() != nil || p.generation != generation || !a.currentDoubao(p) {
				return
			}
			cancel()
			p.cancel = nil
			p.switching = false
			if err != nil || value != true {
				p.session = 0
				p.fallback = true
				if err := page.LoadURL("about:blank"); err != nil {
					p.err = "Could not reset Doubao: " + err.Error()
				}
				p.trace("reset-to-blank")
				return
			}
			p.nextURL, p.session, p.ready = "", generation, true
			pollCtx, stop := context.WithCancel(context.Background())
			p.cancel = stop
			go a.pollDoubao(pollCtx, p, page, p.wake, draft, generation, generation)
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

func (a *app) doubaoRouteAllowed(p *doubaoView, raw string) bool {
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
	if u.Host == store.DoubaoHost && (u.Path == "/chat" || strings.HasPrefix(u.Path, "/chat/")) {
		remote, ok := store.DoubaoConversationURL(raw)
		return ok && remote == chat.WebURL
	}
	return true // HTTPS authentication pages are allowed by the outer policy.
}

func doubaoBlankRoute(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == store.DoubaoHost && (u.Path == "/chat" || u.Path == "/chat/")
}

func readDoubaoSnapshot(ctx context.Context, page *mygo.Page) (doubaoSnapshot, error) {
	var snapshot doubaoSnapshot
	value, err := page.EvalContext(ctx, `JSON.stringify(window.__copilotDoubao?.snapshot() || null)`)
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

func (a *app) pollDoubao(ctx context.Context, p *doubaoView, page *mygo.Page, wake <-chan struct{}, draft string, generation, session uint64) {
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
			value, _ := page.EvalContext(evalCtx, `window.__copilotDoubao?.restoreDraft(`+string(encoded)+`,`+strconv.FormatUint(session, 10)+`)`)
			attempts++
			if value == true {
				attempts = 10
			}
		}
		snapshot, err := readDoubaoSnapshot(evalCtx, page)
		cancel()
		if err != nil || ctx.Err() != nil {
			continue
		}
		a.update(func() {
			if ctx.Err() == nil && p.nextURL == "" && p.generation == generation {
				a.applyDoubaoSnapshot(p, snapshot)
			}
		})
	}
}

// CreatedURL requires the local_<id> → server ID transition in this chat view.
// Settings views never import; a bound record never changes identity.
func (a *app) applyDoubaoSnapshot(p *doubaoView, snapshot doubaoSnapshot) {
	if !a.currentDoubao(p) || p.chatID == "" || p.switching || snapshot.Switching || snapshot.Session != p.session {
		return
	}
	chat := a.conversation(p.chatID)
	if !chat.WebStarted && (snapshot.Started || snapshot.Edited || snapshot.CreatedURL != "" || snapshot.Draft != nil && *snapshot.Draft != "") {
		chat.WebStarted = true
		a.save()
	}
	p.startedChat = p.startedChat || snapshot.Started || snapshot.CreatedURL != ""
	if chat.WebURL == "" && !p.startedChat && doubaoBlankRoute(snapshot.URL) && snapshot.Draft != nil {
		if snapshot.Edited || a.draft != *snapshot.Draft {
			a.attachments = nil
		}
		a.draft = *snapshot.Draft
		a.rememberDraft()
	}
	remote, ok := store.DoubaoConversationURL(snapshot.URL)
	if !ok {
		if u, err := url.Parse(snapshot.URL); err == nil && chat.WebURL != "" && u.Host == store.DoubaoHost && (u.Path == "/chat" || strings.HasPrefix(u.Path, "/chat/")) {
			p.notice = "This local chat is linked to another Doubao conversation."
		}
		return
	}
	changed := false
	if chat.WebURL == "" {
		created, proven := store.DoubaoConversationURL(snapshot.CreatedURL)
		if !proven || remote != created {
			p.notice = "This website conversation could not be linked to this local chat."
			return
		}
		chat.WebKind, chat.WebURL, chat.UpdatedAt = store.ProviderKindDoubao, remote, time.Now()
		changed = true
	} else if chat.WebURL != remote {
		p.notice = "This local chat is linked to another Doubao conversation."
		return
	}
	p.notice = ""
	if title := doubaoTitle(snapshot.Title); !chat.TitleManual && title != "" && title != chat.Title {
		chat.Title = title
		changed = true
	}
	if changed {
		a.save()
	}
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

func (a *app) doubaoChatView(c *ui.Context, chat *store.Conversation) {
	if chat == nil {
		a.closeDoubaoChat()
		return
	}
	provider := a.provider(chat.ProviderID)
	if provider == nil || provider.Kind != store.ProviderKindDoubao || provider.Disabled {
		a.closeDoubaoChat()
		ui.Text(c, "Doubao is disabled or its provider was removed. Enable it in Settings to open this chat.").Padding(16).TextColor(c.Theme().Danger)
		return
	}
	if p := a.doubaoChat; p != nil && (!a.currentDoubao(p) || p.chatID != chat.ID || (p.view != nil && p.view.IsDestroyed())) {
		a.closeDoubaoChat()
	}
	if a.doubaoChat == nil && a.doubaoParked != nil {
		p := a.doubaoParked
		a.doubaoParked = nil
		p.idleGeneration++
		if p.idleTimer != nil {
			p.idleTimer.Stop()
			p.idleTimer = nil
		}
		if p.owner == a.win && p.view != nil && !p.view.IsDestroyed() && p.err == "" {
			a.doubaoChat = p
			if p.chatID != chat.ID || p.resetDraft || p.switching {
				a.retargetDoubao(p, chat)
			} else {
				p.view.Focus()
			}
		} else {
			destroyDoubao(p)
		}
	}
	if a.win == nil {
		return // NewWindow can render its content before assigning the owner.
	}
	if a.doubaoChat == nil {
		target := store.DoubaoChatURL
		if chat.WebURL != "" {
			var ok bool
			target, ok = store.DoubaoConversationURL(chat.WebURL)
			if !ok {
				ui.Text(c, "Invalid Doubao conversation URL.").Padding(16).TextColor(c.Theme().Danger)
				return
			}
		}
		a.doubaoChat = a.newDoubaoView(a.win, chat.ProviderID, chat.ID, target)
	}
	a.renderDoubao(c, a.doubaoChat, a.closeDoubaoChat)
}

func (a *app) doubaoProviderView(c *ui.Context, providerID string) {
	provider := a.provider(providerID)
	if provider == nil || provider.Disabled || provider.Kind != store.ProviderKindDoubao {
		a.closeDoubaoSettings()
		ui.Text(c, "Enable Doubao to sign in.").TextColor(c.Theme().TextMuted)
		return
	}
	if p := a.doubaoSettings; p != nil && (!a.currentDoubao(p) || p.providerID != providerID || (p.view != nil && p.view.IsDestroyed())) {
		a.closeDoubaoSettings()
	}
	if a.settingsWin == nil {
		return
	}
	if a.doubaoSettings == nil {
		a.doubaoSettings = a.newDoubaoView(a.settingsWin, providerID, "", store.DoubaoChatURL)
	}
	a.renderDoubao(c, a.doubaoSettings, a.closeDoubaoSettings)
}

func (a *app) renderDoubao(c *ui.Context, p *doubaoView, retry func()) {
	if p.err != "" {
		ui.Column(c).Fill().Padding(16).Gap(12).Children(func() {
			ui.Text(c, p.err).TextColor(c.Theme().Danger)
			ui.Button(c, "Retry Doubao").OnClick(retry)
		})
		return
	}
	if p.notice != "" {
		ui.Row(c).FillWidth().Shrink(0).Padding(8, 12).Gap(8).Children(func() {
			ui.Text(c, p.notice).Grow(1).TextColor(c.Theme().Danger).FontSize(12)
			ui.Button(c, "Retry Doubao").OnClick(retry)
		})
	}
	ui.WebView(c, p.view).Label("Doubao").Grow(1).FillWidth().MinHeight(0)
}
