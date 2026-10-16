package desktop

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/fondoger/copilotchat/internal/store"
)

func (a *app) view(c *ui.Context) {
	a.applicationMenuShortcuts(c)
	t := a.theme(c)
	if c.Shortcut(ui.Cmd, ui.KeyN) {
		a.newChat()
	}
	if c.Shortcut(ui.Cmd, ui.KeyComma) {
		a.openSettings("General")
	}
	if c.Shortcut(ui.Cmd, ui.KeyB) {
		a.sidebarHidden = !a.sidebarHidden
	}
	if c.Shortcut(ui.Cmd, ui.KeyF) {
		a.openChatSearch()
	}
	if a.streaming && !a.searchOpen && c.Shortcut(0, ui.KeyEscape) {
		a.stop()
	}
	ui.Row(c).Fill().AlignItems(ui.Stretch).Background(t.Background).Children(func() {
		a.sidebarLayout(c, func() { a.sidebar(c) }, func() {
			ui.Column(c).Fill().MinWidth(0).Children(func() {
				a.header(c)
				chat := a.current()
				if chat == nil || len(chat.Messages) == 0 {
					a.emptyView(c)
				} else {
					a.messagesView(c, chat)
				}
				if a.status != "" {
					ui.Row(c).Padding(4, 20).Gap(8).Children(func() {
						ui.Text(c, a.status).FontSize(12).TextColor(t.Danger).Grow(1)
						iconButton(c, iClose, "Dismiss message").OnClick(func() { a.status = "" })
					})
				}
				a.composerView(c)
			})
		})
	})
	a.dialogs(c)
	a.chatSearchView(c)
}

func (a *app) header(c *ui.Context) {
	t := c.Theme()
	bar := c.TitleBar()
	ui.Row(c).Height(50).Shrink(0).Padding(0, bar.Right+16, 0, 20).Gap(8).DragWindow().BorderWidth(0, 0, .5, 0).BorderColor(t.Border.Alpha(.6)).Children(func() {
		if !a.sidebarVisible(c) {
			a.applicationMenuButton(c)
			iconButton(c, iPanelClosed, "Show sidebar").OnClick(func() { a.sidebarHidden = false })
			iconButton(c, iNew, "New Chat").OnClick(a.newChat)
		}
		a.modelPicker(c)
		ui.Spacer(c)
		if a.streaming {
			ui.Text(c, "Responding…").FontSize(12).TextColor(t.TextMuted)
		}
	})
}

func (a *app) emptyView(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Grow(1).Center().Gap(12).Padding(28).Children(func() {
		ui.Icon(c, iEmptyChat).FontSize(38).TextColor(t.TextMuted).Margin(0, 0, 5, 0)
		ui.Text(c, "How can I help you today?").FontSize(24).FontWeight(500)
		if a.selectedClient() == nil {
			ui.Text(c, "Connect your GitHub Copilot account to start chatting.").FontSize(13).TextColor(t.TextMuted)
			ui.Button(c, "Connect GitHub Copilot").Margin(8, 0).OnClick(func() { a.openSettings("Providers") })
		} else {
			ui.Text(c, "Start a conversation, or attach a file for context.").FontSize(13).TextColor(t.TextMuted)
		}
	})
}

func (a *app) messagesView(c *ui.Context, chat *store.Conversation) {
	a.md.Theme = &markdown.Theme{FontSize: float32(a.state.Settings.FontSize), Font: c.Theme().Font, LineHeight: 1.5}
	a.list.Key = func(i int) any { return chat.Messages[i].ID }
	a.list.FollowEnd = true
	a.list.FollowAnimation = &ui.Smooth
	ui.List(c.Key(chat.ID), &a.list, len(chat.Messages), func(i int) {
		m := &chat.Messages[i]
		message := ui.Column(c.Key("message:"+m.ID)).FillWidth().MaxWidth(1024).AlignSelf(ui.Center).Padding(12, 20).Gap(7)
		_, _, pointerOver := message.PointerPosition()
		showActions := pointerOver || message.Hovered() || message.FocusWithin()
		message.Children(func() {
			if a.editMessageID == m.ID {
				a.messageEditor(c, m, i < len(chat.Messages)-1)
				return
			}
			if m.Role == "user" {
				ui.Row(c).Children(func() {
					ui.Spacer(c)
					ui.Column(c).MaxWidthPercent(70).Gap(5).Children(func() {
						if m.Content != "" {
							bubble := ui.Hex("#f4f4f5")
							if c.Theme().Dark {
								bubble = ui.RGBA(161, 161, 170, .1)
							}
							ui.Text(c, m.Content).Selectable().FontSize(float32(a.state.Settings.FontSize)).Padding(8, 12).Radius(12).Background(bubble)
						}
						a.messageAttachments(c, m.Attachments)
					})
				})
				actions := ui.Row(c.Key("user-actions")).Height(20).Gap(12).Justify(ui.End)
				if !showActions {
					actions.Opacity(0)
				}
				actions.Children(func() {
					messageActionButton(c, iCopy, "Copy message").OnClick(func() { c.WriteClipboard(m.Content) })
					messageActionButton(c, iRetry, "Regenerate response").Disabled(a.streaming).OnClick(func() { a.regenerateMessage(m.ID, nil) })
					messageActionButton(c, iEditMessage, "Edit message").Disabled(a.streaming).OnClick(func() { a.beginMessageEdit(m) })
				})
			} else {
				streaming := a.streaming && a.streamMessage == m.ID
				a.reasoningView(c, m, streaming)
				if len(m.ToolActivity) > 0 {
					ui.Text(c, "Used tools · "+strings.Join(m.ToolActivity, " · ")).FontSize(12).TextColor(c.Theme().TextMuted)
				}
				if m.Content == "" && streaming {
					ui.Text(c, "Thinking…").TextColor(c.Theme().TextMuted).FontSize(13)
				}
				ui.Column(c).PaddingStart(4).Children(func() {
					a.md.CodeBlock = func(c *ui.Context, b *markdown.CodeBlockView) { a.codeBlock(c, m.ID, b) }
					if streaming {
						markdown.Stream(c, m.Content, &a.md)
					} else {
						markdown.View(c, m.Content, &a.md)
					}
				})
				if m.Error != "" {
					ui.Text(c, m.Error).FontSize(13).TextColor(c.Theme().Danger)
				}
				if !streaming {
					actions := ui.Row(c).Gap(12).Height(20).PaddingStart(0)
					if !showActions {
						actions.Opacity(0)
					}
					actions.Children(func() {
						messageActionButton(c, iCopy, "Copy response").OnClick(func() { c.WriteClipboard(m.Content) })
						messageActionButton(c, iEditMessage, "Edit response").Disabled(a.streaming).OnClick(func() { a.beginMessageEdit(m) })
						messageActionButton(c, iRetry, "Regenerate response").Disabled(a.streaming).OnClick(func() { a.regenerateMessage(m.ID, nil) })
						if messageStatsButton(c, m) {
							actions.Opacity(1)
						}
						ui.Spacer(c)
						if !m.CreatedAt.IsZero() {
							ui.Text(c, m.CreatedAt.Local().Format("15:04")).Tooltip(m.CreatedAt.Local().Format("2006-01-02 15:04:05")).FontSize(12).FontWeight(500).TextColor(ui.Hex("#a1a1aa"))
						}
					})
				}
			}
		})
	}).Grow(1).Padding(0).Gap(0)
}

func (a *app) composerView(c *ui.Context) {
	t := c.Theme()
	bg := ui.Hex("#fafafa")
	if t.Dark {
		bg = ui.Hex("#1c1c1c")
	}
	ui.Column(c).FillWidth().MaxWidth(1024).AlignSelf(ui.Center).Shrink(0).Padding(12, 20).Children(func() {
		ui.Column(c).Label("Message composer").Radius(16).Border(1, t.Border).Background(bg).Children(func() {
			a.composerAttachments(c)
			placeholder := "Enter a message here, press ⏎ to send"
			if !a.state.Settings.SendWithEnter {
				placeholder = "Enter a message here, press Ctrl+Enter to send"
			}
			services := c.Services()
			ui.TextAreaBase(c.Key("composer"), &a.draft).Bind(&a.composer).Label("Message").Placeholder(placeholder).Lines(1, 12).MaxHeight(300).MinHeight(48).Padding(12, 14, 3, 14).FontSize(float32(a.state.Settings.FontSize)).AutoFocus().OnPaste(func(text string) bool { return a.pasteAttachment(text, services) }).OnTextKey(func(k ui.TextKey) bool {
				send := (a.state.Settings.SendWithEnter && k.Mods == 0) || (!a.state.Settings.SendWithEnter && k.Mods == ui.Cmd)
				if k.Key == ui.KeyEnter && send {
					a.draft = k.Text()
					if a.send() {
						k.SetText("")
					}
					return true
				}
				return false
			})
			ui.Row(c).Padding(8, 12).Gap(8).Children(func() {
				composerIconButton(c, iAttach, "Add attachment").OnClick(a.pickAttachments)
				a.reasoningControl(c)
				ui.Spacer(c)
				if a.streaming {
					composerSendButton(c, iStop, "Stop response").OnClick(a.stop)
				} else {
					composerSendButton(c, iArrow, "Send message").Disabled(strings.TrimSpace(a.draft) == "" && len(a.attachments) == 0).OnClick(func() { a.send() })
				}
			})
		})
	})
}

func (a *app) dialogs(c *ui.Context) {
	ui.Modal(c, &a.renameOpen, func() {
		ui.Text(c, "Rename conversation").FontSize(18).Bold()
		ui.TextInput(c, &a.renameText).Label("Conversation name").Width(350).AutoFocus()
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			ui.Button(c, "Cancel").OnClick(func() { a.renameOpen = false })
			ui.PrimaryButton(c, "Save name").Disabled(strings.TrimSpace(a.renameText) == "").OnClick(func() {
				if chat := a.conversation(a.renameID); chat != nil {
					chat.Title = strings.TrimSpace(a.renameText)
					a.save()
				}
				a.renameOpen = false
			})
		})
	})
	switch ui.AlertDialog(c, &a.deleteOpen, "Delete conversation?", "This conversation and its attachments will be removed from this device.", "Cancel", "Delete") {
	case 1:
		delete(a.drafts, a.deleteID)
		if a.preview != nil && a.preview.chatID == a.deleteID {
			a.closePreview()
		}
		for i, chat := range a.state.Conversations {
			if chat.ID == a.deleteID {
				a.state.Conversations = append(a.state.Conversations[:i], a.state.Conversations[i+1:]...)
				break
			}
		}
		if a.state.ActiveID == a.deleteID {
			a.state.ActiveID = ""
			a.draft = ""
			a.attachments = nil
		}
		a.save()
	}
	if a.approval != nil {
		p := a.approval
		modal := ui.Modal(c, &a.approvalOpen, func() {
			ui.Text(c, "Allow tool call?").FontSize(18).Bold()
			ui.Text(c, p.Server+" / "+p.Name).FontWeight(500)
			ui.Text(c, "This MCP server will receive these arguments and execute the tool.").FontSize(12).TextColor(c.Theme().TextMuted).MaxWidth(500)
			ui.Scroll(c).Height(170).Width(500).Children(func() { ui.Text(c, p.Arguments).Selectable().FontSize(12) })
			ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
				ui.Button(c, "Deny").OnClick(func() { a.answerTool(false) })
				ui.PrimaryButton(c, "Allow once").OnClick(func() { a.answerTool(true) })
			})
		})
		if modal.Dismissed() {
			a.answerTool(false)
		}
	}
}

func (a *app) answerTool(allow bool) {
	if a.approval != nil {
		select {
		case a.approval.Reply <- allow:
		default:
		}
		a.approval = nil
	}
	a.approvalOpen = false
}
