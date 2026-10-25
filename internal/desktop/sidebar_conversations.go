package desktop

import (
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/store"
)

func (a *app) sidebarConversations(c *ui.Context, selected, hovered ui.Color, sidebarOver bool) {
	chats := a.sidebarChats("")
	now := time.Now()
	visible := a.visibleSidebarIDs(chats, now)
	a.sidebarSelection.prune(visible)
	multi := len(a.sidebarSelection.ids) > 1
	last := ""
	previousSelected := false
	visibleIndex := 0
	for _, chat := range chats {
		group := chatGroup(chat, now)
		collapsed := a.state.Settings.CollapsedGroups[group]
		if group != last {
			heading := ui.Column(c.Key("group-container-" + group)).FillWidth().Transition(sidebarMoveTransition())
			if multi && previousSelected && visibleIndex < len(visible) && a.sidebarSelection.ids[visible[visibleIndex]] {
				heading.Background(selected)
			}
			heading.Children(func() { a.sidebarGroup(c, group, collapsed, sidebarOver) })
			last = group
		}
		if collapsed {
			continue
		}
		isSelected := a.sidebarSelection.ids[chat.ID]
		nextSelected := visibleIndex+1 < len(visible) && a.sidebarSelection.ids[visible[visibleIndex+1]]
		row := ui.Row(c.Key(chat.ID)).Label(chat.Title).FillWidth().Height(32).Padding(6, 8).Gap(8).Background(ui.Transparent).Radius(8).Clip().Transition(sidebarMoveTransition())
		if multi && isSelected {
			top, bottom := float32(8), float32(8)
			if previousSelected {
				top = 0
			}
			if nextSelected {
				bottom = 0
			}
			row.Radius(top, top, bottom, bottom)
		}
		previousSelected = multi && isSelected
		visibleIndex++
		_, _, over := row.PointerPosition()
		if isSelected || !multi && chat.ID == a.state.ActiveID {
			row.Background(selected)
		} else if over {
			row.Background(hovered)
		}
		row.Children(func() {
			if a.isWebChat(chat) {
				providerIcon(c, a.webChatKind(chat), 16)
			} else {
				ui.Icon(c, iChat).Size(16, 16).Shrink(0)
			}
			ui.Text(c, chat.Title).Font(chatTitleFontFamily()).FontSize(14).FontWeight(500).SingleLine().Grow(1)
			a.sidebarChatActions(c, chat, over)
		})
		row.OnClick(func() { a.selectSidebarChat(chat.ID, row.ClickModifiers()&ui.Shift != 0, visible) })
		if row.DoubleClicked() && row.ClickModifiers()&ui.Shift == 0 && chat.ID == a.state.ActiveID {
			a.renameChat(chat)
		}
		row.ContextMenu(func(m *ui.Menu) { a.chatMenu(m, chat) })
	}
	if len(chats) == 0 {
		ui.Text(c, "No conversations yet").FontSize(12).TextColor(c.Theme().TextMuted).Margin(12, 8)
	}
}

func sidebarMoveTransition() ui.ElementTransition {
	return ui.ElementTransition{Position: true, Animation: &ui.Smooth}
}

func (a *app) sidebarGroup(c *ui.Context, group string, collapsed, sidebarOver bool) {
	button := ui.ButtonBase(c.Key("group-"+group)).Label("Toggle "+group).Height(16).Margin(8, 0, 4, 0).PaddingX(8).Gap(3).Justify(ui.Start).TextColor(c.Theme().TextMuted)
	if button.Hovered() {
		button.TextColor(c.Theme().Text)
	}
	button.Children(func() {
		ui.Text(c, group).FontSize(12).FontWeight(600)
		arrow := ui.Icon(c, iChevron).Size(12, 12)
		if !collapsed {
			arrow.Rotate(90)
			if !sidebarOver {
				arrow.Opacity(0)
			}
		}
	}).OnClick(func() {
		if a.state.Settings.CollapsedGroups == nil {
			a.state.Settings.CollapsedGroups = make(map[string]bool)
		}
		a.state.Settings.CollapsedGroups[group] = !a.state.Settings.CollapsedGroups[group]
		a.save()
	})
}

func (a *app) sidebarChatActions(c *ui.Context, chat *store.Conversation, over bool) {
	if len(a.sidebarSelection.ids) > 1 {
		return
	}
	fill := ui.Hex("#d4d4d8")
	if c.Theme().Dark {
		fill = ui.Hex("#27272a")
	}
	bar := ui.Row(c.Key("chat-actions")).Absolute().Right(-4).Top(0).Height(32).Padding(8, 8, 8, 32).Gap(0).
		LinearGradient(ui.LinearGradient{From: fill.Alpha(0), To: fill, Angle: 90, End: .5})
	if !over {
		bar.Invisible()
	}
	bar.Children(func() {
		sidebarAction(c, iSidebarDelete, "Delete "+chat.Title).TextColor(ui.Hex("#ef4444")).Disabled(a.streaming && a.streamChat == chat.ID).OnClick(func() {
			a.requestDeleteChats([]string{chat.ID})
		})
		star := iStar
		favoriteLabel := "Favorite " + chat.Title
		if chat.Favorite {
			star = iSidebarStarFilled
			favoriteLabel = "Unfavorite " + chat.Title
		}
		sidebarAction(c, star, favoriteLabel).TextColor(ui.Hex("#f97316")).OnClick(func() {
			chat.Favorite = !chat.Favorite
			a.save()
		})
	})
}

func sidebarAction(c *ui.Context, icon *ui.SVG, label string) ui.Element {
	b := ui.ButtonBase(c).Label(label).Tooltip(label).Size(20, 16).Padding(0).Opacity(.75)
	if b.Hovered() {
		b.Opacity(1)
	}
	return b.Children(func() { ui.Icon(c, icon).Size(14, 14) })
}
