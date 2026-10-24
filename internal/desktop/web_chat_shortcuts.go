package desktop

import (
	"context"

	"github.com/egoist/mygo"
)

// Only the currently selected chat page can invoke application commands.
// TrustedOrigins also exposes MyGo's built-in window methods, not other app APIs.
type webChatShortcuts struct{ app *app }

func (s *webChatShortcuts) Invoke(ctx context.Context, command string) {
	page, window := mygo.CallerPage(ctx), mygo.CallerWindow(ctx)
	if page == nil || window == nil || ctx.Err() != nil {
		return
	}
	s.app.update(func() {
		p := s.app.webChat
		if ctx.Err() != nil || p == nil || p.view == nil || p.view.IsDestroyed() || !s.app.currentWebChatView(p) || page != p.view.Page() || window != s.app.win {
			return
		}
		s.app.webChatCommand(command)
	})
}

// Input notifications contain no text; the owned page worker reads the snapshot.
func (s *webChatShortcuts) DraftChanged(ctx context.Context) {
	page, window := mygo.CallerPage(ctx), mygo.CallerWindow(ctx)
	if page == nil || window == nil || ctx.Err() != nil {
		return
	}
	s.app.update(func() {
		p := s.app.webChat
		if ctx.Err() != nil || p == nil || p.view == nil || p.view.IsDestroyed() || !s.app.currentWebChatView(p) || page != p.view.Page() || window != s.app.win {
			return
		}
		select {
		case p.wake <- struct{}{}:
		default:
		}
	})
}

func (a *app) webChatCommand(command string) {
	switch command {
	case "new-chat":
		a.newChat()
	case "settings":
		a.openSettings("General")
	case "sidebar":
		a.sidebarHidden = !a.sidebarHidden
	case "search":
		a.openChatSearch()
	case "models":
		if !a.streaming {
			a.webModelToggle = !a.webModelToggle
		}
	case "focus":
		a.focusWebChat()
	case "next-chat":
		a.cycleChat(1)
	case "previous-chat":
		a.cycleChat(-1)
	case "stop":
		a.stop()
	case "minimize":
		if a.win != nil {
			a.win.Minimize()
		}
	case "close":
		if a.win != nil {
			a.win.Close()
		}
	}
}
