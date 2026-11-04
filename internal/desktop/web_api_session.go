package desktop

import (
	"context"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/fondoger/copilotchat/internal/desktop/doubaoweb"
	"github.com/fondoger/copilotchat/internal/desktop/webapi"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/provider/doubao"
	"github.com/fondoger/copilotchat/internal/provider/webchat"
	"github.com/fondoger/copilotchat/internal/store"
)

type webAPITransport interface {
	WaitReady(context.Context) error
	Abort(context.Context) error
	Close()
}

// A lease owns one temporary page, including its transport and serialization gate.
type webAPISession struct {
	transport  webAPITransport
	app        *app
	providerID string
	view       *mygo.WebView
	cancel     context.CancelFunc
	release    func()
	once       sync.Once
}

type doubaoSession struct {
	*doubaoweb.Transport
	lease *webAPISession
}

func (s *doubaoSession) Close() { s.lease.Close() }

type nativeWebSession struct {
	*webapi.Transport
	lease *webAPISession
}

func (s *nativeWebSession) Close() { s.lease.Close() }

func webSessionFailure(kind string) error {
	if kind == store.ProviderKindDoubao {
		return doubao.SessionUnavailable()
	}
	return webchat.Failure(kind, 502)
}

func (a *app) openWebAPISession(ctx context.Context, id, kind string, client provider.Client, gate chan struct{}) (*webAPISession, error) {
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
	if kind == store.ProviderKindDoubao {
		if err := doubaoweb.RequireLogin(ctx); err != nil {
			return nil, err
		}
	}
	result := make(chan *webAPISession, 1)
	a.update(func() {
		p := a.provider(id)
		if ctx.Err() != nil || a.quitting || p == nil || p.Disabled || p.Kind != kind || !nativeProvider(p) || a.clientForProvider(id) != client || a.win == nil {
			result <- nil
			return
		}
		site, ok := store.WebProviderFor(kind)
		if !ok || !supportsWebAPIMode(kind) {
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
		var transport webAPITransport
		if kind == store.ProviderKindDoubao {
			transport = doubaoweb.NewTransport(life, page)
		} else {
			transport = webapi.NewTransport(life, page, kind)
		}
		s := &webAPISession{transport: transport, app: a, providerID: id, view: view, cancel: cancel, release: release}
		page.SetPermissionHandler(func(mygo.PermissionRequest) bool { return false })
		page.SetWindowOpenHandler(func(mygo.WindowOpenRequest) *mygo.WindowOptions { return nil })
		page.OnWillDownload(func(e *mygo.DownloadEvent) { e.PreventDefault() })
		page.OnWillNavigate(func(e *mygo.NavigateEvent) {
			if !webNavigationURL(kind, e.URL) {
				e.PreventDefault()
			}
		})
		if a.webAPISessions == nil {
			a.webAPISessions = map[*webAPISession]struct{}{}
		}
		a.webAPISessions[s] = struct{}{}
		go func() { <-life.Done(); s.Close() }()
		_ = page.LoadURL(site.StartURL)
		result <- s
	})
	var s *webAPISession
	select {
	case s = <-result:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if s == nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, webSessionFailure(kind)
	}
	handed = true
	if err := s.transport.WaitReady(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *webAPISession) Close() {
	s.once.Do(func() {
		s.cancel()
		s.app.update(func() {
			if s.view.IsDestroyed() {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			_ = s.transport.Abort(ctx)
			cancel()
			s.view.Destroy()
		})
		s.transport.Close()
		s.release()
		s.app.update(func() { delete(s.app.webAPISessions, s) })
	})
}
