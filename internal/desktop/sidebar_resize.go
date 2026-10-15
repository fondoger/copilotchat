package desktop

import (
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/store"
)

const (
	minimumMainWidth    float32 = 360
	sidebarDividerWidth float32 = 1
	sidebarSaveDelay            = 250 * time.Millisecond
)

type sidebarResizePersistence struct {
	pending bool
	due     time.Time
}

// sidebarLayout uses MyGo's native split control, including its captured drag,
// resize cursor, keyboard arrows, and accessible divider. Both callbacks must
// build content that fills its pane; neither should set a fixed pane width or
// draw another separator. Call every frame, including while the sidebar is
// hidden, so a pending preference save can complete.
func (a *app) sidebarLayout(c *ui.Context, sidebar, main func()) {
	persistence := ui.Local(c.Root(), "sidebar-resize-persistence", func() sidebarResizePersistence {
		return sidebarResizePersistence{}
	})
	if persistence.pending {
		if !c.Now().Before(persistence.due) {
			persistence.pending = false
			a.save()
		} else {
			c.After(persistence.due.Sub(c.Now()))
		}
	}
	viewportWidth, _ := c.Size()
	width := sidebarWidthForViewport(a.state.Settings.SidebarWidth, viewportWidth)
	if a.sidebarHidden || width == 0 {
		main()
		return
	}
	before := width
	split := ui.Split(c.Key("conversation-sidebar-split"), &width, sidebar, main).Fill()
	if split.Changed() {
		width = sidebarWidthForViewport(width, viewportWidth)
		if width != before {
			a.state.Settings.SidebarWidth = width
			// Keep dragging responsive even with large saved attachments. State
			// changes immediately; disk writes wait until input has settled.
			persistence.pending = true
			persistence.due = c.Now().Add(sidebarSaveDelay)
			c.After(sidebarSaveDelay)
		}
	}
}

// sidebarVisible includes the narrow-window fallback. Header controls can use
// this alongside sidebarLayout when deciding whether to show a sidebar toggle.
func (a *app) sidebarVisible(c *ui.Context) bool {
	width, _ := c.Size()
	return !a.sidebarHidden && sidebarWidthForViewport(a.state.Settings.SidebarWidth, width) > 0
}

func sidebarWidthForViewport(preferred, viewportWidth float32) float32 {
	available := viewportWidth - minimumMainWidth - sidebarDividerWidth
	if available < store.MinSidebarWidth {
		// Below the supported two-pane minimum, let the conversation use the
		// entire window. The saved preference restores when room is available.
		return 0
	}
	return min(store.ClampSidebarWidth(preferred), available)
}
