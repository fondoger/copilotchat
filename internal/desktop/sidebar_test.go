package desktop

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/store"
)

func newSidebarTestApp(t *testing.T) *app {
	t.Helper()
	a := newSettingsTestApp(t)
	now := time.Now()
	a.state.Conversations = []store.Conversation{
		{ID: "current", Title: "Current conversation", UpdatedAt: now, Messages: []store.Message{{Role: "user", Content: "A message"}}},
		{ID: "second", Title: "Another conversation", UpdatedAt: now.Add(-time.Minute)},
		{ID: "past", Title: "Yesterday conversation", UpdatedAt: now.AddDate(0, 0, -1)},
	}
	a.state.ActiveID = "current"
	a.save()
	return a
}

func TestSidebarGroupCollapsePersistsAndSearchDoesNotChangeSidebar(t *testing.T) {
	a := newSidebarTestApp(t)
	tt := ui.NewTester(a.sidebar, 300, 600)
	settingsClick(t, tt, "Toggle Today")
	if !a.state.Settings.CollapsedGroups["Today"] || tt.HasText("Current conversation") {
		t.Fatal("Today group did not collapse")
	}
	if !tt.HasText("Yesterday conversation") || !settingsSavedState(t, a).Settings.CollapsedGroups["Today"] {
		t.Fatal("collapse affected another group or did not persist")
	}
	a.query = "Current"
	tt.Frame()
	if tt.HasText("Current conversation") {
		t.Fatal("modal search query changed the sidebar")
	}
	settingsClick(t, tt, "Toggle Today")
	if a.state.Settings.CollapsedGroups["Today"] || !tt.HasText("Current conversation") {
		t.Fatal("group did not expand again")
	}
}

func TestSidebarHoverActionsRemainClickableOverChildren(t *testing.T) {
	a := newSidebarTestApp(t)
	tt := ui.NewTester(a.sidebar, 300, 600)
	if _, found := tt.Find("Favorite Another conversation"); found {
		t.Fatal("hover actions visible before hover")
	}
	row, ok := tt.Find("Another conversation")
	if !ok {
		t.Fatal("missing chat row")
	}
	tt.Move(row.X+48, row.Y+row.H/2)
	favorite, ok := tt.Find("Favorite Another conversation")
	if !ok {
		t.Fatalf("hovering title did not expose actions: %q", tt.Texts())
	}
	if _, ok := tt.Find("Delete Another conversation"); !ok {
		t.Fatal("hovering title did not expose delete action")
	}
	tt.Move(favorite.X+favorite.W/2, favorite.Y+favorite.H/2)
	settingsClick(t, tt, "Favorite Another conversation")
	if !a.conversation("second").Favorite || a.state.ActiveID != "current" {
		t.Fatal("favorite click was lost or selected the chat")
	}
	row, _ = tt.Find("Another conversation")
	tt.Move(row.X+48, row.Y+row.H/2)
	settingsClick(t, tt, "Delete Another conversation")
	if !a.deleteOpen || !reflect.DeepEqual(a.deleteIDs, []string{"second"}) || a.state.ActiveID != "current" {
		t.Fatal("delete did not target hovered chat")
	}
	a.deleteOpen = false
	if err := tt.RightClick("Another conversation"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Rename"); err != nil {
		t.Fatal(err)
	}
	if !a.renameOpen || a.renameID != "second" {
		t.Fatal("context rename targeted wrong chat")
	}
}

func TestSidebarApplicationMenuActions(t *testing.T) {
	a := newSidebarTestApp(t)
	tt := ui.NewTester(a.sidebar, 300, 600)
	settingsClick(t, tt, "Menu")
	if want := []string{"File", "View", "Window", "Help", "-", "Settings"}; !reflect.DeepEqual(tt.Menu(), want) {
		t.Fatalf("application menu = %q, want %q", tt.Menu(), want)
	}
	if err := tt.ChooseMenuItem("View", "Search"); err != nil {
		t.Fatal(err)
	}
	if !a.searchOpen {
		t.Fatal("View > Search did not open search")
	}
	settingsClick(t, tt, "Menu")
	if err := tt.ChooseMenuItem("Window", "Next Chat"); err != nil {
		t.Fatal(err)
	}
	if a.state.ActiveID != "second" {
		t.Fatalf("Next Chat selected %q", a.state.ActiveID)
	}
	settingsClick(t, tt, "Menu")
	if err := tt.ChooseMenuItem("Window", "Previous Chat"); err != nil {
		t.Fatal(err)
	}
	settingsClick(t, tt, "Menu")
	if err := tt.ChooseMenuItem("File", "New Chat"); err != nil {
		t.Fatal(err)
	}
	if len(a.state.Conversations) != 4 || a.current().Title != "New Chat" {
		t.Fatal("File > New Chat did not create a conversation")
	}
}

func TestNativeApplicationMenuStructure(t *testing.T) {
	a := newSidebarTestApp(t)
	toggled := false
	items := a.applicationMenuItems(func() { toggled = true })
	var labels []string
	for _, item := range items {
		if item.Type == mygo.MenuItemSeparator {
			labels = append(labels, "-")
		} else {
			labels = append(labels, item.Label)
		}
	}
	if want := []string{"File", "Edit", "View", "Window", "Help", "-", "Settings"}; !reflect.DeepEqual(labels, want) {
		t.Fatalf("native menu = %q, want %q", labels, want)
	}
	var editRoles []mygo.MenuRole
	for _, item := range items[1].Submenu {
		if item.Type != mygo.MenuItemSeparator {
			editRoles = append(editRoles, item.Role)
			if item.Click != nil {
				t.Fatal("Edit must use the focused control's native role, not a replacement callback")
			}
		}
	}
	wantRoles := []mygo.MenuRole{mygo.RoleUndo, mygo.RoleRedo, mygo.RoleCut, mygo.RoleCopy, mygo.RolePaste, mygo.RoleDelete, mygo.RoleSelectAll}
	if !reflect.DeepEqual(editRoles, wantRoles) {
		t.Fatalf("Edit roles = %q, want %q", editRoles, wantRoles)
	}
	model := items[2].Submenu[0]
	model.Click(model, nil)
	if !toggled {
		t.Fatal("model menu did not invoke the shared picker action")
	}
	a.streaming = true
	if !a.applicationMenuItems(func() {})[2].Submenu[0].Disabled {
		t.Fatal("model menu must be disabled during generation")
	}
	if help := items[4].Submenu; help[0].Label != "Documentation" || help[0].Click == nil || help[1].Label != "About Copilot Chat" || help[1].Click == nil {
		t.Fatalf("missing application help actions: %+v", help)
	}
}

func TestApplicationMenuPreservesTextFocusAndOpensModelPicker(t *testing.T) {
	a := newSidebarTestApp(t)
	text := ""
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() {
			a.applicationMenuButton(c)
			ui.TextInput(c, &text).Label("Edit target")
			a.modelPicker(c)
		})
	}, 640, 480)
	settingsClick(t, tt, "Edit target")
	tt.Type("before")
	settingsClick(t, tt, "Menu")
	tt.CloseMenu()
	tt.Type(" after")
	if text != "before after" {
		t.Fatalf("opening the menu lost the edit target: %q", text)
	}
	settingsClick(t, tt, "Menu")
	if err := tt.ChooseMenuItem("View", "Toggle Model Dropdown"); err != nil {
		t.Fatal(err)
	}
	if !hasModelOptions(tt) {
		t.Fatal("model menu did not open the actual shared picker")
	}
}

func TestModelPickerShortcutFromComposer(t *testing.T) {
	for _, sidebarHidden := range []bool{false, true} {
		t.Run(fmt.Sprintf("sidebarHidden=%t", sidebarHidden), func(t *testing.T) {
			a := newSidebarTestApp(t)
			a.sidebarHidden = sidebarHidden
			var picker *modelPickerState
			var composerFocused bool
			tt := ui.NewTester(func(c *ui.Context) {
				a.view(c)
				picker = ui.Local(c.Root(), "top-model-picker", func() modelPickerState { return modelPickerState{} })
				composerFocused = a.composer.Focused(c)
			}, 1060, 720)
			settingsClick(t, tt, "Message")
			tt.Type("Keep this draft")
			if !composerFocused {
				t.Fatal("test requires the composer to have keyboard focus")
			}
			for _, wantOpen := range []bool{true, false, true} {
				tt.Key(ui.Cmd, ui.KeySlash) // Ctrl on Windows/Linux; Command on macOS.
				if picker.Open != wantOpen || hasModelOptions(tt) != wantOpen {
					t.Fatalf("Ctrl+/ picker open=%t, visible=%t, want %t", picker.Open, hasModelOptions(tt), wantOpen)
				}
				// Later frames must not replay the same key or toggle the shared
				// state again when the other control reads it.
				tt.Frame()
				if picker.Open != wantOpen {
					t.Fatalf("a later frame toggled picker state again: got %t, want %t", picker.Open, wantOpen)
				}
			}
			if a.draft != "Keep this draft" {
				t.Fatalf("shortcut changed the composer draft: %q", a.draft)
			}
		})
	}
}

func TestSidebarSettingsDropdown(t *testing.T) {
	a := newSidebarTestApp(t)
	tt := ui.NewTester(a.sidebar, 300, 600)
	settingsClick(t, tt, "Settings")
	if !tt.HasText("Appearance") || !tt.HasText("Open Settings") || a.settingsWin != nil {
		t.Fatalf("footer did not open its dropdown: %q", tt.Texts())
	}
	settingsClick(t, tt, "Appearance")
	if want := []string{"Light", "Dark", "System"}; !reflect.DeepEqual(tt.Menu(), want) {
		t.Fatalf("appearance menu = %q, want %q", tt.Menu(), want)
	}
	if err := tt.ChooseMenuItem("Dark"); err != nil {
		t.Fatal(err)
	}
	if a.state.Settings.Theme != "dark" || settingsSavedState(t, a).Settings.Theme != "dark" {
		t.Fatal("theme choice did not persist")
	}
	if tt.HasText("Appearance") {
		t.Fatal("theme choice left the settings dropdown open")
	}
}

func TestModelPopupEscapeAndSearchDoNotLeaveStackedMenus(t *testing.T) {
	a := newSidebarTestApp(t)
	tt := ui.NewTester(a.view, 1060, 720)
	settingsClick(t, tt, "Message")
	tt.Key(ui.Cmd, ui.KeySlash)
	if !hasModelOptions(tt) {
		t.Fatal("model popup did not open")
	}
	tt.Key(0, ui.KeyEscape)
	if hasModelOptions(tt) {
		t.Fatal("Escape was consumed before reaching the model popup")
	}
	tt.Key(ui.Cmd, ui.KeySlash)
	tt.Key(ui.Cmd, ui.KeyF)
	if !a.searchOpen || hasModelOptions(tt) {
		t.Fatal("search left the model popup underneath")
	}
	tt.Key(0, ui.KeyEscape)
	if a.searchOpen {
		t.Fatal("Escape did not close search")
	}
}

func TestSidebarScreenshots(t *testing.T) {
	dir := os.Getenv("COPILOT_CHAT_SCREENSHOTS")
	if dir == "" {
		t.Skip("set COPILOT_CHAT_SCREENSHOTS to render sidebar images")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, theme := range []string{"light", "dark"} {
		a := newSidebarTestApp(t)
		a.state.Settings.Theme = theme
		tt := ui.NewTester(func(c *ui.Context) { a.theme(c); a.sidebar(c) }, 280, 600)
		row, _ := tt.Find("Current conversation")
		tt.Move(row.X+48, row.Y+row.H/2)
		for _, state := range []string{"hover", "settings"} {
			if state == "settings" {
				settingsClick(t, tt, "Settings")
			}
			file, err := os.Create(filepath.Join(dir, "sidebar-"+theme+"-"+state+".png"))
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(file, tt.Image()); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func hasModelOptions(tt *ui.Tester) bool { _, found := tt.Find("Model options"); return found }

func TestChatSearchDialogKeyboardAndMessageMatches(t *testing.T) {
	a := newSidebarTestApp(t)
	a.sidebarHidden = true
	tt := ui.NewTester(a.view, 1060, 720)
	settingsClick(t, tt, "Message")
	tt.Type("keep draft")
	tt.Key(ui.Cmd, ui.KeyF)
	if _, ok := tt.Find("Search conversations"); !ok {
		t.Fatal("search shortcut did not open the dialog")
	}
	if !a.sidebarHidden {
		t.Fatal("opening search forced sidebar visible")
	}
	tt.Type("A message")
	if !tt.HasText("Current conversation") || !tt.HasText("A message") || tt.HasText("Yesterday conversation") {
		t.Fatalf("wrong message search: %q", tt.Texts())
	}
	tt.Key(0, ui.KeyEnter)
	if a.searchOpen || a.state.ActiveID != "current" || a.query != "" {
		t.Fatal("Enter did not select and close search")
	}
	a.openChatSearch()
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyEnter)
	if a.state.ActiveID != "second" || a.searchOpen {
		t.Fatal("arrow navigation did not select the second chat")
	}
	a.openChatSearch()
	tt.Frame()
	tt.Type("no such conversation")
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyEnter)
	if !a.searchOpen || !tt.HasText("No results") {
		t.Fatal("empty search should stay open")
	}
	tt.Key(0, ui.KeyEscape)
	if a.searchOpen || a.query != "" {
		t.Fatal("Escape did not clear and close search")
	}
}
