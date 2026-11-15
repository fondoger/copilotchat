package desktop

import "github.com/fondoger/copilotchat/internal/store"

func (a *app) rememberDraft() {
	if a.draft == "" && len(a.attachments) == 0 {
		delete(a.state.Drafts, a.state.ActiveID)
		return
	}
	if a.state.Drafts == nil {
		a.state.Drafts = make(map[string]store.Draft)
	}
	a.state.Drafts[a.state.ActiveID] = store.Draft{Text: a.draft, Attachments: append([]store.Attachment(nil), a.attachments...)}
}

// Restore before any startup save can replace the persisted composer buffers.
func (a *app) restoreComposer() {
	draft := a.state.Drafts[a.state.ActiveID]
	a.draft, a.attachments = draft.Text, append([]store.Attachment(nil), draft.Attachments...)
	a.editMessageID, a.editText = "", ""
	if edit := a.state.MessageEdit; edit != nil {
		if edit.ChatID == a.state.ActiveID && messageIndex(a.current(), edit.MessageID) >= 0 {
			a.editMessageID, a.editText = edit.MessageID, edit.Text
		} else {
			a.state.MessageEdit = nil
		}
	}
}
