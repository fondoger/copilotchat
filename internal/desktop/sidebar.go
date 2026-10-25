package desktop

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/appupdate"
	"github.com/fondoger/copilotchat/internal/store"
)

func (a *app) sidebar(c *ui.Context) {
	t := c.Theme()
	bg := ui.Hex("#e3e2e6")
	selected, hovered := ui.RGBA(0, 0, 0, .08), ui.RGBA(0, 0, 0, .03)
	if t.Dark {
		bg = ui.Hex("#201f21")
		selected, hovered = ui.Hex("#3f3f46").Alpha(.8), ui.Hex("#3f3f46").Alpha(.3)
	}
	container := ui.Column(c.Key("sidebar-content")).Fill().Background(bg)
	_, _, sidebarOver := container.PointerPosition()
	container.Children(func() {
		bar := c.TitleBar()
		ui.Row(c).Height(44).Shrink(0).Padding(12, 16, 4, 16).PaddingStart(max(float32(16), bar.Left)).Gap(2).DragWindow().Children(func() {
			a.applicationMenuButton(c)
			iconButton(c, iPanel, "Toggle sidebar").OnClick(func() { a.sidebarHidden = true })
			iconButton(c, iNew, "New Chat").OnClick(a.newChat)
		})
		ui.Scroll(c).Grow(1).Padding(4, 12, 12, 12).Gap(0).Children(func() {
			a.sidebarConversations(c, selected, hovered, sidebarOver)
		})
		ui.Row(c).Height(48).PaddingX(12).Children(func() {
			a.sidebarSettingsButton(c)
			ui.Spacer(c)
			iconButton(c, iSearch, "Search chats").TextColor(t.TextMuted).OnClick(a.openChatSearch)
		})
	})
}

func (a *app) sidebarChats(query string) []*store.Conversation {
	chats := make([]*store.Conversation, 0, len(a.state.Conversations))
	for i := range a.state.Conversations {
		chat := &a.state.Conversations[i]
		if matchesChat(chat, query) {
			chats = append(chats, chat)
		}
	}
	sort.SliceStable(chats, func(i, j int) bool {
		if chats[i].Favorite != chats[j].Favorite {
			return chats[i].Favorite
		}
		return chats[i].UpdatedAt.After(chats[j].UpdatedAt)
	})
	return chats
}

func (a *app) openChatSearch() {
	a.query, a.searchOpen = "", true
}

type chatSearchState struct {
	query              string
	selected           int
	revealed           int
	pointerX, pointerY float32
	wasOpen            bool
}

// Search is a window dialog, independent of whether the sidebar is shown.
func (a *app) chatSearchView(c *ui.Context) {
	state := ui.Local(c.Root(), "chat-search-state", func() chatSearchState { return chatSearchState{} })
	if !a.searchOpen {
		state.wasOpen = false
		return
	}
	if !state.wasOpen || state.query != a.query {
		state.selected, state.query = 0, a.query
		state.revealed = -1
	}
	state.wasOpen = true
	chats := a.sidebarChats(a.query)
	state.selected = max(0, min(state.selected, len(chats)-1))
	width, height := c.Size()
	background, border := ui.Hex("#f2edec"), ui.Hex("#e4e4e7")
	if c.Theme().Dark {
		background, border = ui.Hex("#27272a"), ui.Hex("#3f3f46")
	}
	choose := func(index int) {
		if index >= 0 && index < len(chats) {
			a.selectChat(chats[index].ID)
			a.searchOpen, a.query = false, ""
		}
	}
	dialog := ui.DialogBase(c.Key("chat-search-dialog"), &a.searchOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.Transparent)
		panel.Label("Search conversations").Column().Width(min(float32(512), width-32)).MaxHeight(height*.6).Radius(8).Border(1, border).Background(background).Shadow(0, 5, 12, 6, ui.Hex("#8b8b8b").Alpha(.38)).Clip()
		pointerX, pointerY, _ := panel.PointerPosition()
		pointerMoved := pointerX != state.pointerX || pointerY != state.pointerY
		state.pointerX, state.pointerY = pointerX, pointerY
		ui.Row(c).Height(49).Shrink(0).Padding(8, 12).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(border).Children(func() {
			ui.Icon(c, iSearchModal).Size(16, 16).Opacity(.5).Shrink(0)
			input := ui.TextInputBase(c.Key("chat-search-input"), &a.query).Label("Search conversations input").Placeholder("Search").FontSize(14).Height(32).Grow(1).AutoFocus()
			input.OnShortcut(0, ui.KeyDown, func() { state.selected = max(0, min(state.selected+1, len(chats)-1)) })
			input.OnShortcut(0, ui.KeyUp, func() { state.selected = max(0, state.selected-1) })
			input.OnSubmit(func() { choose(state.selected) })
			if a.query != "" {
				ui.ButtonBase(c).Label("Clear search").Size(16, 16).Padding(0).Opacity(.5).Children(func() {
					ui.Icon(c, iSearchClear).Size(16, 16)
				}).OnClick(func() { a.query = ""; input.Focus() })
			}
		})
		ui.Scroll(c).FillWidth().MaxHeight(height*.6 - 49).Padding(6).Gap(0).Children(func() {
			for i, chat := range chats {
				row := ui.ButtonBase(c.Key(chat.ID)).Label("Open "+chat.Title).FillWidth().Padding(8, 12).Gap(8).Radius(6).Justify(ui.Start).AlignItems(ui.Start)
				if pointerMoved && row.Hovered() {
					state.selected = i
				}
				selected := i == state.selected
				ink, muted := c.Theme().Text, ui.Hex("#a1a1aa")
				if c.Theme().Dark {
					muted = ui.Hex("#71717a")
				}
				if selected {
					row.Background(c.Theme().Accent)
					ink, muted = ui.Hex("#ffffff"), ui.Hex("#ffffff").Alpha(.7)
				}
				row.TextColor(ink).Children(func() {
					ui.Icon(c, iSearchResult).Size(14, 14).Shrink(0).Margin(3, 0, 0, 0).Opacity(.6)
					ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						ui.RichText(c, chatSearchSpans(chat.Title, a.query, selected, c.Theme().Dark)...).FontSize(14).SingleLine()
						if snippet := chatSearchSnippet(chat, a.query); snippet != "" {
							ui.RichText(c, chatSearchSpans(snippet, a.query, selected, c.Theme().Dark)...).FontSize(12).TextColor(muted).SingleLine()
						}
					})
				}).OnClick(func() { choose(i) })
				if selected && state.revealed != i {
					row.ScrollIntoView()
					state.revealed = i
				}
			}
			if len(chats) == 0 {
				ui.Text(c, "No results").FontSize(14).TextColor(c.Theme().TextMuted).PaddingY(32).AlignSelf(ui.Center)
			}
		})
	})
	if dialog.Dismissed() {
		a.query = ""
	}
}

func chatSearchSpans(text, query string, selected, dark bool) []ui.Span {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return []ui.Span{{Text: text}}
	}
	fill, ink := ui.Hex("#fde047").Alpha(.8), ui.Hex("#71717a")
	if dark {
		fill, ink = ui.Hex("#eab308").Alpha(.4), ui.Hex("#e4e4e7")
	}
	if selected {
		fill, ink = ui.Hex("#facc15"), ui.Hex("#ffffff")
		if dark {
			fill = ui.Hex("#a16207")
		}
	}
	var spans []ui.Span
	for text != "" {
		index := strings.Index(strings.ToLower(text), query)
		if index < 0 {
			spans = append(spans, ui.Span{Text: text})
			break
		}
		if index > 0 {
			spans = append(spans, ui.Span{Text: text[:index]})
		}
		end := index + len(query)
		spans = append(spans, ui.Span{Text: text[index:end], Background: fill, Color: ink, Radius: 2})
		text = text[end:]
	}
	return spans
}

func chatSearchSnippet(chat *store.Conversation, query string) string {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return ""
	}
	for _, message := range chat.Messages {
		text := []rune(strings.Join(strings.Fields(message.Content), " "))
		index := strings.Index(strings.ToLower(string(text)), query)
		if index < 0 {
			continue
		}
		start := max(0, len([]rune(string(text)[:index]))-30)
		end := min(len(text), start+80)
		result := string(text[start:end])
		if start > 0 {
			result = "…" + result
		}
		if end < len(text) {
			result += "…"
		}
		return result
	}
	return ""
}

func (a *app) renameChat(chat *store.Conversation) {
	a.renameID, a.renameText, a.renameOpen = chat.ID, chat.Title, true
}

func (a *app) sidebarSettingsButton(c *ui.Context) {
	open := ui.Local(c.Root(), "sidebar-settings-open", func() bool { return false })
	button := ui.ButtonBase(c.Key("sidebar-settings")).Label("Settings").Height(28).PaddingX(4).Gap(4).Radius(6).Justify(ui.Start)
	if button.Hovered() || *open {
		fill := ui.RGBA(0, 0, 0, .08)
		if c.Theme().Dark {
			fill = ui.Hex("#3f3f46").Alpha(.7)
		}
		button.Background(fill)
	}
	button.Children(func() {
		ui.Icon(c, iSettings).Size(16, 16)
		ui.Text(c, "Settings").FontSize(12).FontWeight(500)
		if a.updateSnapshot().Phase == appupdate.Ready {
			ui.Text(c, "Update ready").FontSize(10).TextColor(c.Theme().Accent)
		}
	}).OnClick(func() { *open = !*open })
	ui.PopoverBase(c, button, open, func(panel ui.Element) {
		panel.Width(220).Margin(4, 0, 0, 0).Padding(4).Gap(2).Radius(8).Border(1, c.Theme().Border).Background(c.Theme().Background)
		item := sidebarPopupItem(c, iSidebarSun, "Appearance")
		item.Menu(func(m *ui.Menu) {
			for _, theme := range []struct{ label, value string }{{"Light", "light"}, {"Dark", "dark"}, {"System", "system"}} {
				if m.Item(theme.label).Checked(a.state.Settings.Theme == theme.value).Chosen() {
					a.state.Settings.Theme = theme.value
					if a.win != nil || a.settingsWin != nil {
						a.applyAppearance()
					}
					a.saveSettings()
					*open = false
				}
			}
		})
		ui.Divider(c).MarginY(3)
		updateLabel := "Check for updates"
		if a.updateSnapshot().Phase == appupdate.Ready {
			updateLabel = "Update ready…"
		}
		sidebarPopupItem(c, iRetry, updateLabel).OnClick(func() {
			*open = false
			a.openUpdateSettings()
		})
		sidebarPopupItem(c, iSettings, "Settings").Label("Open Settings").OnClick(func() {
			*open = false
			a.openSettings("General")
		})
	})
}

func sidebarPopupItem(c *ui.Context, icon *ui.SVG, label string) ui.Element {
	item := ui.ButtonBase(c).Label(label).FillWidth().Height(32).Padding(6, 8).Gap(8).Radius(6).Justify(ui.Start)
	if item.Hovered() {
		item.Background(c.Theme().SurfaceHover)
	}
	return item.Children(func() {
		ui.Icon(c, icon).Size(16, 16)
		ui.Text(c, label).FontSize(14).Grow(1)
		if label == "Appearance" {
			ui.Icon(c, iChevron).Size(14, 14)
		}
	})
}

func (a *app) modelPickerToggle(c *ui.Context) func() {
	picker := ui.Local(c.Root(), "top-model-picker", func() modelPickerState { return modelPickerState{} })
	return func() {
		if !a.streaming {
			picker.Open = !picker.Open
		}
	}
}

// Call at the start of the window view. Context.OnShortcut inside a toolbar
// only covers that toolbar's focus subtree, not the composer or popup search.
func (a *app) applicationMenuShortcuts(c *ui.Context) {
	c.OnShortcut(ui.Cmd, ui.KeyL, func() {
		if a.isWebChat(a.current()) {
			a.focusWebChat()
			return
		}
		if !a.composer.Focused(c) {
			a.composer.Focus(c)
		}
	})
	c.OnShortcut(ui.Cmd, ui.KeySlash, a.modelPickerToggle(c))
	// The native composer keeps focus while this lightweight popup is open.
	// Route Escape at the window root as well as the overlay's own handler.
	picker := ui.Local(c.Root(), "top-model-picker", func() modelPickerState { return modelPickerState{} })
	if a.webModelToggle {
		a.webModelToggle = false
		if !a.streaming {
			picker.Open = !picker.Open
		}
	}
	if picker.Open {
		c.OnShortcut(0, ui.KeyEscape, func() { picker.Open = false })
	}
	c.OnShortcut(ui.Cmd|ui.Shift, ui.KeyF, a.openChatSearch)
	c.OnShortcut(ui.Ctrl, ui.KeyTab, func() { a.cycleChat(1) })
	c.OnShortcut(ui.Ctrl|ui.Shift, ui.KeyTab, func() { a.cycleChat(-1) })
	c.OnShortcut(ui.Ctrl, ui.KeyEscape, a.stop)
	if a.win != nil {
		c.OnShortcut(ui.Cmd, ui.KeyM, a.win.Minimize)
		c.OnShortcut(ui.Cmd, ui.KeyW, a.win.Close)
	}
}

// applicationMenuButton uses native menu roles so Edit acts on the focused
// input or selected message. KeepFocus preserves that target while opening it.
func (a *app) applicationMenuButton(c *ui.Context) {
	toggleModel := a.modelPickerToggle(c)
	button := iconButton(c, iMenu, "Menu").KeepFocus()
	if a.win == nil {
		// The headless surface cannot route native editing roles to a window.
		// Keep its real app commands testable without inventing Edit actions.
		button.Menu(func(m *ui.Menu) { appendHeadlessApplicationMenu(m, a.applicationMenuItems(toggleModel)) })
		return
	}
	button.OnClick(func() {
		bounds := button.Bounds()
		menu, window := mygo.NewMenu(a.applicationMenuItems(toggleModel)), a.win
		// A native popup has its own event loop; open after this UI frame.
		go menu.PopupAt(window, int(bounds.X), int(bounds.Y+bounds.H+4))
	})
}

func (a *app) applicationMenuItems(toggleModel func()) []*mygo.MenuItem {
	action := func(fn func()) func(*mygo.MenuItem, *mygo.Window) {
		return func(_ *mygo.MenuItem, _ *mygo.Window) {
			fn()
			if a.win != nil {
				a.win.Invalidate()
			}
		}
	}
	return []*mygo.MenuItem{
		{Label: "File", Submenu: []*mygo.MenuItem{
			{Label: "New Chat", Accelerator: "CmdOrCtrl+N", Click: action(a.newChat)},
			mygo.Separator(),
			{Label: "Quit", Role: mygo.RoleQuit},
		}},
		{Label: "Edit", Role: mygo.RoleEditMenu, Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleUndo}, {Role: mygo.RoleRedo}, mygo.Separator(),
			{Role: mygo.RoleCut}, {Role: mygo.RoleCopy}, {Role: mygo.RolePaste},
			{Role: mygo.RoleDelete}, mygo.Separator(), {Role: mygo.RoleSelectAll},
		}},
		{Label: "View", Submenu: []*mygo.MenuItem{
			{Label: "Toggle Model Dropdown", Accelerator: "CmdOrCtrl+/", Disabled: a.streaming || toggleModel == nil, Click: action(func() {
				if toggleModel != nil {
					toggleModel()
				}
			})},
			{Label: "Search", Accelerator: "CmdOrCtrl+Shift+F", Click: action(a.openChatSearch)},
			{Label: "Toggle Sidebar", Accelerator: "CmdOrCtrl+B", Click: action(func() { a.sidebarHidden = !a.sidebarHidden })},
			mygo.Separator(),
			{Label: "Abort Generation", Accelerator: "Ctrl+Escape", Disabled: !a.streaming, Click: action(a.stop)},
		}},
		{Label: "Window", Submenu: []*mygo.MenuItem{
			{Label: "Minimize", Role: mygo.RoleMinimize, Disabled: a.win == nil},
			{Label: "Zoom", Role: mygo.RoleZoom, Disabled: a.win == nil},
			mygo.Separator(),
			{Label: "Next Chat", Accelerator: "Ctrl+Tab", Disabled: len(a.state.Conversations) < 2, Click: action(func() { a.cycleChat(1) })},
			{Label: "Previous Chat", Accelerator: "Ctrl+Shift+Tab", Disabled: len(a.state.Conversations) < 2, Click: action(func() { a.cycleChat(-1) })},
			{Label: "Close", Role: mygo.RoleClose, Disabled: a.win == nil},
		}},
		{Label: "Help", Submenu: []*mygo.MenuItem{
			{Label: "Documentation", Disabled: a.win == nil, Click: action(a.showDocumentation)},
			{Label: "About Copilot Chat", Disabled: a.win == nil, Click: action(a.showAbout)},
			{Label: "Check for updates", Click: action(a.openUpdateSettings)},
		}},
		mygo.Separator(),
		{Label: "Settings", Accelerator: "CmdOrCtrl+,", Click: action(func() { a.openSettings("General") })},
	}
}

func appendHeadlessApplicationMenu(menu *ui.Menu, items []*mygo.MenuItem) {
	for _, item := range items {
		if item.Role == mygo.RoleEditMenu {
			continue
		}
		if item.Type == mygo.MenuItemSeparator {
			menu.Separator()
		} else if item.Submenu != nil {
			menu.Submenu(item.Label, func(submenu *ui.Menu) { appendHeadlessApplicationMenu(submenu, item.Submenu) })
		} else if menu.Item(item.Label).Disabled(item.Disabled || item.Click == nil).Chosen() {
			item.Click(item, nil)
		}
	}
}

// Documentation stays available in packaged and offline builds. This project
// has no published feedback destination; do not send its bugs to ChatWise.
const applicationHelp = "Connect: Settings > Providers > Add provider, then choose GitHub Copilot or Microsoft 365 Copilot and sign in.\n\n" +
	"Chat: choose a model in the top bar, type a message, and press Enter. Shift+Enter inserts a new line. Attach files with the paperclip; supported formats depend on the model.\n\n" +
	"Conversations: use New Chat, open Search, or right-click a conversation to rename, favorite, export, or delete it. Click a date group to collapse it.\n\n" +
	"Settings: configure appearance, Copilot providers, MCP servers, chat titles, and optional system-tray residence.\n\n" +
	"Shortcuts: Ctrl+N new chat; Ctrl+, settings; Ctrl+/ model picker; Ctrl+Shift+F search; Ctrl+B sidebar; Ctrl+Tab / Ctrl+Shift+Tab next / previous chat; Escape stops generation."

func (a *app) showDocumentation() {
	parent := a.win
	go func() {
		_, _ = mygo.Dialog.Message(mygo.MessageOptions{Parent: parent, Type: mygo.MessageInfo, Title: "Copilot Chat Documentation", Message: "Using Copilot Chat", Detail: applicationHelp})
	}()
}

func (a *app) showAbout() {
	parent := a.win
	detail := fmt.Sprintf("Version %s\n%s/%s\n\nNative Go + MyGo chat client for GitHub Copilot and Microsoft 365 Copilot.\nInterface reference: ChatWise, used with the owner's permission.", version, runtime.GOOS, runtime.GOARCH)
	go func() {
		_, _ = mygo.Dialog.Message(mygo.MessageOptions{Parent: parent, Type: mygo.MessageInfo, Title: "About Copilot Chat", Message: "Copilot Chat", Detail: detail})
	}()
}

func (a *app) cycleChat(step int) {
	chats := a.sidebarChats("")
	if len(chats) == 0 {
		return
	}
	index := 0
	active := a.state.ActiveID
	if a.pendingChat != "" {
		active = a.pendingChat
	}
	for i, chat := range chats {
		if chat.ID == active {
			index = (i + step + len(chats)) % len(chats)
			break
		}
	}
	a.selectChat(chats[index].ID)
}

func matchesChat(chat *store.Conversation, q string) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" || strings.Contains(strings.ToLower(chat.Title), q) {
		return true
	}
	for _, m := range chat.Messages {
		if strings.Contains(strings.ToLower(m.Content), q) {
			return true
		}
	}
	return false
}

func chatGroup(chat *store.Conversation, now time.Time) string {
	if chat.Favorite {
		return "Favorites"
	}
	d := chat.UpdatedAt.In(now.Location())
	if d.Format("2006-01-02") == now.Format("2006-01-02") {
		return "Today"
	}
	if d.Format("2006-01-02") == now.AddDate(0, 0, -1).Format("2006-01-02") {
		return "Yesterday"
	}
	if d.Year() == now.Year() && d.Month() == now.Month() {
		return "This Month"
	}
	return "Older"
}

func (a *app) chatMenu(m *ui.Menu, chat *store.Conversation) {
	ids := a.sidebarContextIDs(chat.ID)
	if len(ids) > 1 {
		if m.Item(fmt.Sprintf("Delete %d conversations", len(ids))).Disabled(!a.canDeleteChats(ids)).Chosen() {
			a.requestDeleteChats(ids)
		}
		return
	}
	if m.Item("Rename").Chosen() {
		a.renameChat(chat)
	}
	favorite := "Favorite"
	if chat.Favorite {
		favorite = "Unfavorite"
	}
	if m.Item(favorite).Chosen() {
		chat.Favorite = !chat.Favorite
		a.save()
	}
	m.Separator()
	if m.Item("Export Markdown…").Disabled(a.isWebChat(chat)).Chosen() {
		a.exportChat(chat.ID)
	}
	m.Separator()
	if m.Item("Delete conversation").Disabled(a.streaming && a.streamChat == chat.ID).Chosen() {
		a.requestDeleteChats([]string{chat.ID})
	}
}
