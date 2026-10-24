package desktop

import (
	"errors"
	"testing"

	"github.com/fondoger/copilotchat/internal/store"
)

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
