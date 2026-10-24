package desktop

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/store"
)

func sidebarSelectedIDs(a *app) []string {
	var ids []string
	for _, chat := range a.sidebarChats("") {
		if a.sidebarSelection.ids[chat.ID] {
			ids = append(ids, chat.ID)
		}
	}
	return ids
}

func TestSidebarShiftRangeAndContextDelete(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "forward", true: "reverse"}[reverse], func(t *testing.T) {
			a := newSidebarTestApp(t)
			a.draft = "Unsent draft"
			tt := ui.NewTester(func(c *ui.Context) { a.sidebar(c); a.dialogs(c) }, 900, 620)
			first, last := "Current conversation", "Yesterday conversation"
			if reverse {
				first, last = last, first
			}
			settingsClick(t, tt, first)
			active := a.state.ActiveID
			if err := tt.ClickWith(ui.Shift, last); err != nil {
				t.Fatal(err)
			}
			want := []string{"current", "second", "past"}
			if got := sidebarSelectedIDs(a); !reflect.DeepEqual(got, want) || a.state.ActiveID != active {
				t.Fatalf("range = %v, active = %q; want %v without navigation", got, a.state.ActiveID, want)
			}
			if err := tt.RightClick("Another conversation"); err != nil {
				t.Fatal(err)
			}
			if err := tt.ChooseMenuItem("Delete 3 conversations"); err != nil {
				t.Fatal(err)
			}
			if !a.deleteOpen || !reflect.DeepEqual(a.deleteIDs, want) || len(a.state.Conversations) != 3 {
				t.Fatal("context deletion did not defer the whole range to confirmation")
			}
			settingsClick(t, tt, "Cancel")
			if len(a.state.Conversations) != 3 || a.deleteIDs != nil {
				t.Fatal("cancel removed conversations or retained pending targets")
			}
			if err := tt.RightClick("Another conversation"); err != nil {
				t.Fatal(err)
			}
			if err := tt.ChooseMenuItem("Delete 3 conversations"); err != nil {
				t.Fatal(err)
			}
			settingsClick(t, tt, "Delete")
			if len(a.state.Conversations) != 0 || a.state.ActiveID != "" || a.draft != "" || len(a.drafts) != 0 {
				t.Fatal("confirmed range deletion left conversations or drafts behind")
			}
		})
	}
}

func TestSidebarRangeSkipsCollapsedGroupsAndResetsHiddenAnchor(t *testing.T) {
	a := newSidebarTestApp(t)
	a.state.Conversations = append(a.state.Conversations, store.Conversation{ID: "favorite", Title: "Favorite chat", Favorite: true, UpdatedAt: time.Now()})
	tt := ui.NewTester(a.sidebar, 300, 600)
	settingsClick(t, tt, "Favorite chat")
	settingsClick(t, tt, "Toggle Today")
	if err := tt.ClickWith(ui.Shift, "Yesterday conversation"); err != nil {
		t.Fatal(err)
	}
	if got := sidebarSelectedIDs(a); !reflect.DeepEqual(got, []string{"favorite", "past"}) {
		t.Fatalf("collapsed chats entered range: %v", got)
	}
	settingsClick(t, tt, "Toggle Favorites")
	if err := tt.ClickWith(ui.Shift, "Yesterday conversation"); err != nil {
		t.Fatal(err)
	}
	if got := sidebarSelectedIDs(a); !reflect.DeepEqual(got, []string{"past"}) || a.state.ActiveID != "past" {
		t.Fatalf("hidden anchor did not fall back to ordinary selection: %v", got)
	}
}

func TestSidebarOrdinaryClickAndOutsideContextResetRange(t *testing.T) {
	a := newSidebarTestApp(t)
	tt := ui.NewTester(a.sidebar, 300, 600)
	settingsClick(t, tt, "Current conversation")
	if err := tt.ClickWith(ui.Shift, "Another conversation"); err != nil {
		t.Fatal(err)
	}
	if err := tt.RightClick("Yesterday conversation"); err != nil {
		t.Fatal(err)
	}
	if a.state.ActiveID != "current" || !reflect.DeepEqual(sidebarSelectedIDs(a), []string{"past"}) {
		t.Fatal("right click outside range must retarget without navigating")
	}
	if err := tt.ChooseMenuItem("Delete conversation"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.deleteIDs, []string{"past"}) {
		t.Fatalf("single context target = %v", a.deleteIDs)
	}
	a.deleteOpen = false
	settingsClick(t, tt, "Another conversation")
	if a.state.ActiveID != "second" || !reflect.DeepEqual(sidebarSelectedIDs(a), []string{"second"}) {
		t.Fatal("ordinary click did not reset selection and navigate")
	}
	a.cycleChat(1)
	if !reflect.DeepEqual(sidebarSelectedIDs(a), []string{"past"}) {
		t.Fatal("keyboard navigation retained a stale range")
	}
}

func TestSidebarBulkDeleteProtectsStreamingAtMenuAndConfirmation(t *testing.T) {
	a := newSidebarTestApp(t)
	tt := ui.NewTester(a.sidebar, 300, 600)
	settingsClick(t, tt, "Current conversation")
	if err := tt.ClickWith(ui.Shift, "Another conversation"); err != nil {
		t.Fatal(err)
	}
	a.streaming, a.streamChat = true, "second"
	if err := tt.RightClick("Current conversation"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Delete 2 conversations"); err == nil || a.deleteOpen {
		t.Fatal("streaming range deletion was enabled")
	}
	a.streaming = false
	a.requestDeleteChats([]string{"current", "second"})
	a.streaming = true
	dialog := ui.NewTester(a.dialogs, 900, 620)
	settingsClick(t, dialog, "Delete")
	if len(a.state.Conversations) != 3 {
		t.Fatal("stream started after opening confirmation: batch must stay intact")
	}
	a.deleteChats([]string{"past"})
	if a.conversation("past") != nil || !a.streaming || a.conversation("second") == nil {
		t.Fatal("deleting unrelated conversation disrupted active stream")
	}
}

func TestBulkDeleteCleansViewsDraftsJobsAndPendingNavigation(t *testing.T) {
	a := newSidebarTestApp(t)
	a.state.Conversations[0].WebKind = store.ProviderKindDoubao
	a.state.Conversations[0].WebURL = "https://www.doubao.com/chat/local-only"
	a.drafts = map[string]chatDraft{"current": {Text: "first"}, "second": {Text: "second"}, "past": {Text: "keep"}}
	a.draft = "active draft"
	a.attachments = []store.Attachment{{Name: "draft.txt"}}
	a.editMessageID, a.editText = "edit", "editing"
	a.pendingChat = "second"
	ctx, cancel := context.WithCancel(context.Background())
	parkedCtx, parkedCancel := context.WithCancel(context.Background())
	titleCtx, titleCancel := context.WithCancel(context.Background())
	defer cancel()
	defer parkedCancel()
	defer titleCancel()
	a.webChat = &webChatView{chatID: "current", cancel: cancel}
	a.webParked = &webChatView{chatID: "second", cancel: parkedCancel}
	a.preview = &artifactPreview{chatID: "second"}
	a.titleJobs = map[string]*titleJob{"second": {cancel: titleCancel}}
	a.sidebarSelection.selectOne("current")
	a.sidebarSelection.ids["second"] = true
	generation := a.modelSelection
	a.deleteChats([]string{"current", "second"})
	if ctx.Err() == nil || parkedCtx.Err() == nil || titleCtx.Err() == nil || a.webChat != nil || a.webParked != nil || a.preview != nil || len(a.titleJobs) != 0 {
		t.Fatal("deleted chat retained a view or background job")
	}
	if a.pendingChat != "" || a.modelSelection <= generation || a.state.ActiveID != "" || a.draft != "" || len(a.attachments) != 0 || a.editMessageID != "" || a.editText != "" {
		t.Fatal("deleted active chat retained composer or deferred navigation state")
	}
	if len(a.drafts) != 1 || a.drafts["past"].Text != "keep" || len(a.sidebarSelection.ids) != 0 || a.sidebarSelection.anchor != "" {
		t.Fatal("cleanup left deleted drafts/selection or removed unrelated draft")
	}
	saved := settingsSavedState(t, a)
	if len(saved.Conversations) != 1 || saved.Conversations[0].ID != "past" || saved.ActiveID != "" {
		t.Fatal("batch deletion was not persisted")
	}
}
