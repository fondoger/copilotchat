package desktop

import (
	"context"
	"errors"
	"log"
	"net/url"
	"sync"

	"github.com/egoist/mygo"
	"github.com/fondoger/copilotchat/internal/agentviz"
	"github.com/fondoger/copilotchat/internal/localproxy"
)

const visualizerDocument = "/web/index.html"

// The read-only bridge belongs to exactly one page. Other trusted WebViews
// (including provider login/chat pages) cannot read captured prompts through it.
// UI callbacks publish its owner/server; bound calls run on worker goroutines.
type gatewayVisualizerAPI struct {
	mu     sync.RWMutex
	page   *mygo.Page
	server *localproxy.Server
}

func (s *gatewayVisualizerAPI) attach(page *mygo.Page, server *localproxy.Server) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.page, s.server = page, server
}
func (s *gatewayVisualizerAPI) setServer(server *localproxy.Server) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.server = server
}
func (s *gatewayVisualizerAPI) caller(ctx context.Context) (*localproxy.Server, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	page := mygo.CallerPage(ctx)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if page == nil || page != s.page {
		return nil, errors.New("Visualizer access is limited to its own window.")
	}
	return s.server, nil
}
func (s *gatewayVisualizerAPI) Poll(ctx context.Context, version string) (localproxy.VisualizerUpdate, error) {
	server, err := s.caller(ctx)
	if err != nil {
		return localproxy.VisualizerUpdate{}, err
	}
	return server.VisualizerPoll(version), nil
}
func (s *gatewayVisualizerAPI) Detail(ctx context.Context, id string) (agentviz.TurnDetail, error) {
	server, err := s.caller(ctx)
	if err != nil {
		return agentviz.TurnDetail{}, err
	}
	return server.VisualizerDetail(id)
}

func (a *app) showGatewayVisualizer() error {
	if a.visualizerWin != nil && !a.visualizerWin.IsDestroyed() {
		a.visualizerWin.Show()
		a.visualizerWin.Focus()
		return nil
	}
	if a.visualizerAPI == nil {
		return errors.New("The visualizer window is not initialized.")
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title: "LLM Gateway Visualizer", Width: 1120, Height: 760,
		MinWidth: 720, MinHeight: 480, StateKey: "gateway-visualizer",
		BackgroundColor: "light-dark(#f3f3f3, #202020)",
	})
	a.visualizerWin = win
	page := win.Page()
	a.visualizerAPI.attach(page, a.forwarder)
	page.SetPermissionHandler(func(mygo.PermissionRequest) bool { return false })
	page.SetWindowOpenHandler(func(mygo.WindowOpenRequest) *mygo.WindowOptions { return nil })
	page.OnWillDownload(func(e *mygo.DownloadEvent) { e.PreventDefault() })
	page.OnDidFailLoad(func(err *mygo.LoadError) { log.Printf("Visualizer page failed: %v", err) })
	page.OnWillNavigate(func(e *mygo.NavigateEvent) {
		if !visualizerNavigation(e.URL) {
			e.PreventDefault()
		}
	})
	win.OnClosed(func() {
		if a.visualizerWin == win {
			a.visualizerWin = nil
			a.visualizerAPI.attach(nil, a.forwarder)
		}
	})
	if err := page.LoadURL(visualizerDocument); err != nil {
		win.Destroy()
		return err
	}
	return nil
}

func visualizerNavigation(raw string) bool {
	u, err := url.Parse(raw)
	// net/http.FileServer canonicalizes /web/index.html to /web/.
	if err != nil || (u.Path != visualizerDocument && u.Path != "/web/") || u.RawQuery != "" {
		return false
	}
	return u.Scheme == "mygo" && u.Host == "localhost" || u.Scheme == "http" && u.Host == "mygo.localhost"
}

func (a *app) closeGatewayVisualizer() {
	if a.visualizerAPI != nil {
		a.visualizerAPI.attach(nil, nil)
	}
	if win := a.visualizerWin; win != nil {
		a.visualizerWin = nil
		win.Destroy()
	}
}
