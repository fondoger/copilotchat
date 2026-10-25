package desktop

import (
	"errors"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/store"
)

func messageIndex(chat *store.Conversation, id string) int {
	if chat != nil {
		for i := range chat.Messages {
			if chat.Messages[i].ID == id {
				return i
			}
		}
	}
	return -1
}

// Build and validate the replacement history before discarding any later turn.
func (a *app) regenerationHistory(chat *store.Conversation, id string, edited *string) ([]store.Message, error) {
	i := messageIndex(chat, id)
	if i < 0 {
		return nil, errors.New("This message is no longer available.")
	}
	for i >= 0 && chat.Messages[i].Role != "user" {
		i--
	}
	if i < 0 {
		return nil, errors.New("There is no user message to regenerate from.")
	}
	messages := append([]store.Message(nil), chat.Messages[:i+1]...)
	if edited != nil {
		text := strings.TrimSpace(*edited)
		if text == "" && len(messages[i].Attachments) == 0 {
			return nil, errors.New("Enter a message or keep an attachment before saving.")
		}
		messages[i].Content = text
		messages[i].ProviderMessages = nil
	}
	if err := a.replyConfigurationError(chat.ProviderID, chat.Model, chat.ReasoningEffort, chat.ThinkingBudgetTokens); err != nil {
		return nil, err
	}
	candidate := *chat
	candidate.Messages = messages
	if err := a.validateHistoryAttachments(&candidate, chat.ProviderID, chat.Model); err != nil {
		return nil, err
	}
	return messages, nil
}

func (a *app) regenerateMessage(id string, edited *string) bool {
	chat := a.current()
	if a.streaming || chat == nil {
		return false
	}
	messages, err := a.regenerationHistory(chat, id, edited)
	if err != nil {
		a.status = err.Error()
		return false
	}
	a.closePreview()
	chat.Messages = messages
	chat.UpdatedAt = time.Now()
	a.editMessageID, a.editText, a.status = "", "", ""
	a.startReply(chat)
	return true
}

func (a *app) beginMessageEdit(m *store.Message) {
	if !a.streaming {
		a.editMessageID, a.editText = m.ID, m.Content
	}
}

func (a *app) saveMessageEdit() bool {
	chat := a.current()
	i := messageIndex(chat, a.editMessageID)
	if a.streaming || i < 0 {
		return false
	}
	m := &chat.Messages[i]
	if m.Role == "user" {
		return a.regenerateMessage(m.ID, &a.editText)
	}
	if strings.TrimSpace(a.editText) == "" {
		a.status = "Enter response text before saving."
		return false
	}
	a.closePreview()
	m.Content = a.editText
	// The edited answer is the source of truth for future requests. The old
	// provider transcript, signed reasoning and measurements describe old text.
	m.ProviderMessages, m.Stats, m.ToolActivity, m.Citations = nil, nil, nil, nil
	m.Reasoning, m.Error = "", ""
	chat.UpdatedAt = time.Now()
	a.editMessageID, a.editText, a.status = "", "", ""
	a.save()
	return true
}

func (a *app) messageEditor(c *ui.Context, m *store.Message, hasLater bool) {
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.TextArea(c.Key("edit:"+m.ID), &a.editText).Label("Edit message content").Lines(3, 18).FillWidth().AutoFocus()
		ui.Row(c).Gap(8).Children(func() {
			if m.Role == "user" && hasLater {
				ui.Text(c, "Later replies will be replaced.").FontSize(12).TextColor(c.Theme().TextMuted)
			}
			ui.Spacer(c)
			ui.Button(c, "Cancel edit").OnClick(func() { a.editMessageID, a.editText = "", "" })
			label := "Save changes"
			if m.Role == "user" {
				label = "Save & regenerate"
			}
			ui.PrimaryButton(c, label).Disabled(a.streaming).OnClick(func() { a.saveMessageEdit() })
		})
	})
}

func messageActionButton(c *ui.Context, icon *ui.SVG, label string) ui.Element {
	ink := ui.Hex("#71717a")
	if c.Theme().Dark {
		ink = ui.Hex("#a1a1aa")
	}
	b := ui.ButtonBase(c).Label(label).Tooltip(label).Size(16, 20).Padding(0).Background(ui.Transparent).TextColor(ink)
	if b.Hovered() {
		b.TextColor(c.Theme().Text)
	}
	return b.Children(func() { ui.Icon(c, icon).Size(16, 16) })
}
