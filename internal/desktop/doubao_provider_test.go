package desktop

import (
	"errors"
	"testing"

	"github.com/fondoger/copilotchat/internal/store"
)

func TestRestartPrunesOnlyUntouchedWebPlaceholders(t *testing.T) {
	s, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &app{storage: s, state: store.DefaultState()}
	a.state.Conversations = []store.Conversation{
		{ID: "empty", WebKind: store.ProviderKindDoubao, Title: "New Chat"},
		{ID: "existing", WebKind: store.ProviderKindDoubao, Title: "Existing", WebURL: store.DoubaoChatURL + "/123"},
		{ID: "typed", WebKind: store.ProviderKindDoubao, Title: "New Chat", WebStarted: true},
		{ID: "renamed", WebKind: store.ProviderKindDoubao, Title: "New Chat", TitleManual: true},
		{ID: "favorite", WebKind: store.ProviderKindDoubao, Title: "New Chat", Favorite: true},
		{ID: "native", Title: "New Chat"},
	}
	a.state.ActiveID = "existing"
	a.save()
	state, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	a.state = state
	a.pruneEmptyWebChats()
	if a.conversation("empty") != nil || len(a.state.Conversations) != 5 || a.state.ActiveID != "existing" {
		t.Fatal("restart removed meaningful records or changed the selected existing chat")
	}
	state, err = s.Load()
	if err != nil || len(state.Conversations) != 5 {
		t.Fatalf("cleanup not persisted: %v", err)
	}
	a.state.Conversations = append(a.state.Conversations, store.Conversation{ID: "active-empty", WebKind: store.ProviderKindDoubao, Title: "New Chat"})
	a.state.ActiveID = "active-empty"
	a.pruneEmptyWebChats()
	if a.state.ActiveID != "" {
		t.Fatal("removed placeholder remained selected")
	}
}

func TestDoubaoSelectionPreservesNativeAndWebHistory(t *testing.T) {
	a := providerTestApp(t)
	id, err := a.addProviderKind(store.ProviderKindDoubao, "豆包", "")
	if err != nil {
		t.Fatal(err)
	}
	a.state.Conversations = []store.Conversation{{ID: "native", ProviderID: store.DefaultProviderID, Title: "Existing", Model: "same", Messages: []store.Message{{Role: "user", Content: "Keep this"}}}}
	a.state.ActiveID = "native"
	a.chooseDoubao(id)
	webID := a.state.ActiveID
	if webID == "native" || !a.isDoubaoChat(a.current()) || a.current().Model != "" {
		t.Fatalf("did not create web conversation: %#v", a.current())
	}
	if chat := a.conversation("native"); chat.ProviderID != store.DefaultProviderID || len(chat.Messages) != 1 {
		t.Fatalf("native history mutated: %#v", chat)
	}
	// An unused web chat is reused, but a bound one needs a fresh local ID.
	a.newChat()
	if a.state.ActiveID != webID {
		t.Fatal("duplicated unused web chat")
	}
	a.current().WebURL = store.DoubaoChatURL + "/38447877386550786"
	a.newChat()
	if a.state.ActiveID == webID {
		t.Fatal("reused an established web conversation")
	}
	a.selectChat(webID)
	a.chooseModel(store.DefaultProviderID, "same")
	if a.state.ActiveID == webID || a.isDoubaoChat(a.current()) {
		t.Fatal("API selection retained web conversation")
	}
	if chat := a.conversation(webID); chat.WebURL == "" || chat.ProviderID != id {
		t.Fatalf("web history mutated: %#v", chat)
	}
	if err := a.removeProvider(id); err != nil {
		t.Fatal(err)
	}
	if !a.isDoubaoChat(a.conversation(webID)) {
		t.Fatal("removed provider caused web chat to become native")
	}
}

func TestUnsentProviderSwitchReusesConversationAndDraft(t *testing.T) {
	a := providerTestApp(t)
	id, err := a.addProviderKind(store.ProviderKindDoubao, "豆包", "")
	if err != nil {
		t.Fatal(err)
	}
	a.newChat()
	chatID := a.state.ActiveID
	a.draft = "草稿\nsecond line 🙂"
	a.attachments = []store.Attachment{{Name: "notes.txt", Data: "ZmlsZQ=="}}
	for range 4 {
		a.chooseDoubao(id)
		if a.state.ActiveID != chatID || !a.isDoubaoChat(a.current()) {
			t.Fatal("web selection did not convert the unused chat")
		}
		a.chooseModel(store.DefaultProviderID, "same")
		if a.state.ActiveID != chatID || a.isDoubaoChat(a.current()) || len(a.state.Conversations) != 1 || a.draft != "草稿\nsecond line 🙂" || len(a.attachments) != 1 {
			t.Fatal("round trip duplicated the chat or discarded its draft")
		}
	}
	a.newChat()
	if a.state.ActiveID == chatID || a.drafts[chatID].Text != "草稿\nsecond line 🙂" {
		t.Fatal("explicit New Chat did not preserve the separate draft")
	}
}

func TestStartedWebChatCannotConvertBeforeBinding(t *testing.T) {
	a := providerTestApp(t)
	id, _ := a.addProviderKind(store.ProviderKindDoubao, "豆包", "")
	a.chooseDoubao(id)
	webID := a.state.ActiveID
	a.doubaoChat = &doubaoView{chatID: webID, providerID: id, startedChat: true}
	a.chooseModel(store.DefaultProviderID, "same")
	if a.state.ActiveID == webID || !a.isDoubaoChat(a.conversation(webID)) {
		t.Fatal("converted a web chat whose first message was already sent")
	}
	destroyDoubao(a.doubaoParked)
}

func TestDoubaoDoesNotUseAPICredentialsOrModels(t *testing.T) {
	a := providerTestApp(t)
	id, err := a.addProviderKind(store.ProviderKindDoubao, "豆包", "")
	if err != nil {
		t.Fatal(err)
	}
	secrets := a.providerSecrets.(*memoryCredentials)
	secrets.entries[providerCredentialKey(id)] = []byte("not-an-api-token")
	secrets.deleteErrors = map[string]error{providerCredentialKey(id): errors.New("locked vault")}
	a.loadProviders()
	if a.clientForProvider(id) != nil || providerCatalogRefreshDue(*a.provider(id), a.providerState(id), a.provider(id).ModelsRefreshedAt) {
		t.Fatal("web provider entered API pipeline")
	}
	a.beginProviderLogin(id)
	if a.providerState(id).authBusy {
		t.Fatal("web provider started device-code login")
	}
	if err := a.removeProvider(id); err != nil {
		t.Fatalf("web provider removal used credential vault: %v", err)
	}
	if string(secrets.entries[providerCredentialKey(id)]) != "not-an-api-token" {
		t.Fatal("web provider modified unrelated API credentials")
	}
}
