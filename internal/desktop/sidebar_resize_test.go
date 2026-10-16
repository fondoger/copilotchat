package desktop

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/store"
)

func sidebarTestView(a *app) func(*ui.Context) {
	return func(c *ui.Context) {
		a.sidebarLayout(c,
			func() {
				ui.Column(c).Fill().Label("Sidebar pane").Children(func() { ui.Text(c, "Saved conversations") })
			},
			func() { ui.Column(c).Fill().Label("Conversation pane").Children(func() { ui.Text(c, "Conversation") }) },
		)
	}
}

func TestNativeSidebarDragKeyboardAndPersistence(t *testing.T) {
	disk, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &app{state: store.DefaultState(), storage: disk}
	tt := ui.NewTester(sidebarTestView(a), 1060, 720)
	// MyGo's divider has a six-DIP hit target around the one-DIP line.
	tt.Press(241, 300)
	tt.Move(321, 300)
	tt.Release(321, 300)
	if a.state.Settings.SidebarWidth != 320 {
		t.Fatalf("drag width: %v", a.state.Settings.SidebarWidth)
	}
	main, ok := tt.Find("Conversation pane")
	if !ok || main.X != 321 {
		t.Fatalf("conversation did not follow divider: %#v %v", main, ok)
	}
	tt.Key(0, ui.KeyRight)
	if a.state.Settings.SidebarWidth != 330 {
		t.Fatalf("keyboard divider width: %v", a.state.Settings.SidebarWidth)
	}
	// Persistence is debounced so dragging does not repeatedly serialize
	// every attachment. Advance MyGo's deterministic UI clock to flush it.
	tt.Advance(sidebarSaveDelay)
	tt.Frame()
	loaded, err := disk.Load()
	if err != nil || loaded.Settings.SidebarWidth != 330 {
		t.Fatalf("persisted width: %v, %v", loaded.Settings.SidebarWidth, err)
	}
}

func TestSidebarBoundsAndNarrowWindowPreservePreference(t *testing.T) {
	a := &app{state: store.DefaultState()}
	a.state.Settings.SidebarWidth = 480
	tt := ui.NewTester(sidebarTestView(a), 1060, 720)
	tt.Press(481, 300)
	tt.Move(900, 300)
	tt.Release(900, 300)
	if a.state.Settings.SidebarWidth != 500 {
		t.Fatalf("upper bound: %v", a.state.Settings.SidebarWidth)
	}
	tt.SetSize(720, 480)
	main, ok := tt.Find("Conversation pane")
	if !ok || main.W < minimumMainWidth || main.X != 360 {
		t.Fatalf("narrow main pane: %#v %v", main, ok)
	}
	if a.state.Settings.SidebarWidth != 500 {
		t.Fatal("window resizing overwrote the preferred width")
	}
	tt.SetSize(1060, 720)
	main, _ = tt.Find("Conversation pane")
	if main.X != 501 {
		t.Fatalf("preferred width did not restore: %#v", main)
	}
	tt.Press(501, 300)
	tt.Move(20, 300)
	tt.Release(20, 300)
	if a.state.Settings.SidebarWidth != 240 {
		t.Fatalf("lower bound: %v", a.state.Settings.SidebarWidth)
	}
	tt.SetSize(580, 480)
	if _, ok := tt.Find("Sidebar pane"); ok {
		t.Fatal("sidebar should yield space below the two-pane minimum")
	}
	main, _ = tt.Find("Conversation pane")
	if main.X != 0 || main.W != 580 {
		t.Fatalf("collapsed main pane: %#v", main)
	}
	if a.state.Settings.SidebarWidth != 240 {
		t.Fatal("automatic collapse overwrote the preference")
	}
}
