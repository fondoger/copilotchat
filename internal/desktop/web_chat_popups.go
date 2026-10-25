package desktop

import "github.com/egoist/mygo"

// Keep authentication in related windows: navigating the opener instead breaks
// OAuth flows that use postMessage or wait for the popup to close.
func (a *app) setupWebChatPopups(p *webChatView, page *mygo.Page) {
	popups := make(map[*mygo.Window]bool)
	pending := make(map[*mygo.Window]int)
	allowed := func(raw string) bool {
		// Some login SDKs open an empty window before assigning its location.
		return raw == "" || raw == "about:blank" || webNavigationURL(p.kind, raw)
	}
	open := func(parent *mygo.Window) func(mygo.WindowOpenRequest) *mygo.WindowOptions {
		return func(request mygo.WindowOpenRequest) *mygo.WindowOptions {
			if !a.currentWebChatView(p) || !allowed(request.URL) {
				return nil
			}
			pending[parent]++
			return &mygo.WindowOptions{
				Title: p.siteName() + " — Sign in", Width: 520, Height: 720,
				Parent: parent, BackgroundColor: "#ffffff",
				Page: mygo.PageOptions{DevTools: mygo.DevToolsDisabled},
			}
		}
	}
	// MyGo creates the related window after the open handler returns. Register
	// its guards synchronously, before the popup's first navigation.
	off := mygo.App.OnWindowCreated(func(window *mygo.Window) {
		parent := window.Parent()
		if pending[parent] == 0 {
			return
		}
		pending[parent]--
		popup := window.Page()
		if popup == nil {
			return
		}
		popups[window] = true
		window.OnClosed(func() { delete(popups, window) })
		popup.SetPermissionHandler(func(mygo.PermissionRequest) bool { return false })
		popup.OnWillDownload(func(e *mygo.DownloadEvent) { e.PreventDefault() })
		popup.OnWillNavigate(func(e *mygo.NavigateEvent) {
			if !a.currentWebChatView(p) || !allowed(e.URL) {
				e.PreventDefault()
			}
		})
		popup.SetWindowOpenHandler(open(window))
	})
	p.off = append(p.off, off, func() {
		for window := range popups {
			window.Destroy()
		}
	})
	page.SetWindowOpenHandler(open(p.owner))
}
