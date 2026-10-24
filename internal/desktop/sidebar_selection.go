package desktop

import (
	"slices"
	"time"

	"github.com/fondoger/copilotchat/internal/store"
)

// Sidebar selection is independent of the open conversation: extending a range
// or opening its context menu must not navigate away from the current draft.
type sidebarSelection struct {
	anchor string
	ids    map[string]bool
}

func (s *sidebarSelection) selectOne(id string) {
	s.anchor = id
	s.ids = map[string]bool{id: true}
}

func (s *sidebarSelection) prune(visible []string) {
	shown := make(map[string]bool, len(visible))
	for _, id := range visible {
		shown[id] = true
	}
	for id := range s.ids {
		if !shown[id] {
			delete(s.ids, id)
		}
	}
	if !shown[s.anchor] {
		s.anchor = ""
	}
}

func (s *sidebarSelection) selectRange(id string, visible []string) bool {
	start, end := slices.Index(visible, s.anchor), slices.Index(visible, id)
	if start < 0 || end < 0 {
		return false
	}
	s.ids = make(map[string]bool)
	for _, id := range visible[min(start, end) : max(start, end)+1] {
		s.ids[id] = true
	}
	return true
}

func (a *app) visibleSidebarIDs(chats []*store.Conversation, now time.Time) []string {
	ids := make([]string, 0, len(chats))
	for _, chat := range chats {
		if !a.state.Settings.CollapsedGroups[chatGroup(chat, now)] {
			ids = append(ids, chat.ID)
		}
	}
	return ids
}

func (a *app) selectSidebarChat(id string, extend bool, visible []string) {
	if extend && a.sidebarSelection.selectRange(id, visible) {
		return
	}
	a.selectChat(id)
}

func (a *app) sidebarContextIDs(id string) []string {
	visible := a.visibleSidebarIDs(a.sidebarChats(""), time.Now())
	a.sidebarSelection.prune(visible)
	if !a.sidebarSelection.ids[id] {
		a.sidebarSelection.selectOne(id)
	}
	ids := make([]string, 0, len(a.sidebarSelection.ids))
	for _, id := range visible {
		if a.sidebarSelection.ids[id] {
			ids = append(ids, id)
		}
	}
	return ids
}

func (a *app) canDeleteChats(ids []string) bool {
	return len(ids) > 0 && !(a.streaming && slices.Contains(ids, a.streamChat))
}

func (a *app) requestDeleteChats(ids []string) {
	if a.canDeleteChats(ids) {
		a.deleteIDs = slices.Clone(ids)
		a.deleteOpen = true
	}
}

// Delete local records only; web conversations on the provider are untouched.
// Recheck streaming at confirmation time, so the batch is all-or-nothing even
// when generation started after the context menu or confirmation opened.
func (a *app) deleteChats(ids []string) {
	if !a.canDeleteChats(ids) {
		return
	}
	removed := make(map[string]bool, len(ids))
	for _, id := range ids {
		removed[id] = true
		delete(a.drafts, id)
		delete(a.sidebarSelection.ids, id)
		if job := a.titleJobs[id]; job != nil {
			job.cancel()
			delete(a.titleJobs, id)
		}
	}
	if removed[a.sidebarSelection.anchor] {
		a.sidebarSelection.anchor = ""
	}
	if a.webParked != nil && removed[a.webParked.chatID] {
		destroyWebChatView(a.webParked)
		a.webParked = nil
	}
	if a.webChat != nil && removed[a.webChat.chatID] {
		a.closeWebChat()
	}
	if a.preview != nil && removed[a.preview.chatID] {
		a.closePreview()
	}
	// Invalidate deferred web departure/navigation before it can reopen a
	// removed conversation or cache that conversation's draft again.
	if removed[a.state.ActiveID] || removed[a.pendingChat] {
		a.modelSelection++
		a.pendingChat = ""
	}
	if removed[a.state.ActiveID] {
		a.state.ActiveID = ""
		a.draft, a.editMessageID, a.editText = "", "", ""
		a.attachments = nil
	}
	a.state.Conversations = slices.DeleteFunc(a.state.Conversations, func(chat store.Conversation) bool {
		return removed[chat.ID]
	})
	a.save()
}
