package desktop

import (
	"context"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/fondoger/copilotchat/internal/desktop/doubaoweb"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/provider/doubao"
	"github.com/fondoger/copilotchat/internal/store"
)

// A lease owns one temporary page. Nothing is acquired by Models or history views.
type doubaoSession struct {
	*doubaoweb.Transport
	app            *app
	providerID     string
	view           *mygo.WebView
	cancel         context.CancelFunc
	release        func()
	once, pageOnce sync.Once
}

func (a *app) openDoubaoSession(ctx context.Context, id string, client provider.Client, gate chan struct{}) (doubao.Session, error) {
	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	handed := false
	release := sync.OnceFunc(func() { <-gate })
	defer func() {
		if !handed {
			release()
		}
	}()
	if err := doubaoweb.RequireLogin(ctx); err != nil {
		return nil, err
	}
	result := make(chan *doubaoSession, 1)
	a.update(func() {
		p := a.provider(id)
		if ctx.Err() != nil || a.quitting || p == nil || p.Disabled || !nativeProvider(p) || a.clientForProvider(id) != client || a.win == nil {
			result <- nil
			return
		}
		view, err := a.win.NewWebView(mygo.WebViewOptions{Page: mygo.PageOptions{DevTools: mygo.DevToolsDisabled}, BackgroundColor: "#ffffff"})
		if err != nil {
			result <- nil
			return
		}
		life, cancel := context.WithCancel(ctx)
		page := view.Page()
		s := &doubaoSession{Transport: doubaoweb.NewTransport(life, page), app: a, providerID: id, view: view, cancel: cancel, release: release}
		page.SetPermissionHandler(func(mygo.PermissionRequest) bool { return false })
		page.SetWindowOpenHandler(func(mygo.WindowOpenRequest) *mygo.WindowOptions { return nil })
		page.OnWillDownload(func(e *mygo.DownloadEvent) { e.PreventDefault() })
		page.OnWillNavigate(func(e *mygo.NavigateEvent) {
			if !webNavigationURL(store.ProviderKindDoubao, e.URL) {
				e.PreventDefault()
			}
		})
		if a.doubaoSessions == nil {
			a.doubaoSessions = map[*doubaoSession]struct{}{}
		}
		a.doubaoSessions[s] = struct{}{}
		go func() { <-life.Done(); s.Close() }()
		_ = page.LoadURL(store.DoubaoChatURL)
		result <- s
	})
	var s *doubaoSession
	select {
	case s = <-result:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if s == nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, doubao.SessionUnavailable()
	}
	handed = true
	if err := s.WaitReady(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *doubaoSession) releasePage() {
	s.pageOnce.Do(func() {
		s.app.update(func() {
			if s.view.IsDestroyed() {
				return
			}
			abort, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			_ = s.Abort(abort)
			cancel()
			s.view.Destroy()
		})
	})
}

func (s *doubaoSession) Close() {
	s.once.Do(func() {
		s.cancel()
		s.releasePage()
		s.CloseIdleConnections()
		s.release()
		s.app.update(func() { delete(s.app.doubaoSessions, s) })
	})
}
