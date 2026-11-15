package desktop

import (
	"testing"

	"github.com/fondoger/copilotchat/internal/store"
)

func TestSavedComposerRestoresWithoutAnActiveConversation(t *testing.T) {
	a := newSettingsTestApp(t)
	a.draft = "Unsent text before the first chat\n🙂"
	a.attachments = []store.Attachment{{Name: "notes.txt", Data: "ZmlsZQ=="}}
	a.save()
	restarted := &app{state: settingsSavedState(t, a), storage: a.storage}
	restarted.restoreComposer()
	if restarted.draft != a.draft || len(restarted.attachments) != 1 || restarted.attachments[0].Data != "ZmlsZQ==" {
		t.Fatal("saving before a normal quit lost the unbound draft")
	}
	restarted.attachments[0].Name = "changed.txt"
	if restarted.state.Drafts[""].Attachments[0].Name != "notes.txt" {
		t.Fatal("restored composer shares mutable attachment snapshots")
	}
}

func TestRestoringComposerDropsUnavailableMessageEdit(t *testing.T) {
	for _, chatID := range []string{"current", "other"} {
		t.Run(chatID, func(t *testing.T) {
			a := &app{state: store.DefaultState()}
			a.state.ActiveID = "current"
			a.state.Conversations = []store.Conversation{{ID: "current"}, {ID: "other", Messages: []store.Message{{ID: "missing"}}}}
			a.state.MessageEdit = &store.MessageEdit{ChatID: chatID, MessageID: "missing", Text: "Stale edit"}
			a.restoreComposer()
			if a.editMessageID != "" || a.editText != "" || a.state.MessageEdit != nil {
				t.Fatal("restored an edit for a missing message or another chat")
			}
		})
	}
}
