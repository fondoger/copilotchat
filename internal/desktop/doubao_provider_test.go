package desktop

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/provider/doubao"
	"github.com/fondoger/copilotchat/internal/store"
)

func TestDoubaoAPIModeKeepsHistoryIdentityAndGatewayPrivate(t *testing.T) {
	a := providerTestApp(t)
	id, err := a.addProviderKind(store.ProviderKindDoubao, "Doubao", "")
	if err != nil {
		t.Fatal(err)
	}
	a.chooseWebProvider(id)
	webID := a.state.ActiveID
	a.current().WebURL = "https://www.doubao.com/chat/1234567890123456789"
	a.current().WebStarted = true
	a.setExperimentalAPIMode(id, true)
	if !a.isWebChat(a.conversation(webID)) {
		t.Fatal("existing web history changed rendering mode")
	}
	if a.clientForProvider(id) == nil {
		t.Fatal("API mode has no Native client")
	}
	a.chooseModel(id, doubao.ModelID)
	nativeID := a.state.ActiveID
	if nativeID == webID || a.isWebChat(a.current()) || a.current().Mode != "native" || a.chatProviderKind(a.current()) != store.ProviderKindDoubao {
		t.Fatal("new native conversation was not independent")
	}
	a.current().Messages = []store.Message{{Role: "user", Content: "keep local history"}}
	a.syncForwardingProviders()
	for _, profile := range a.state.Settings.ForwardProfiles {
		for _, m := range profile.Models {
			if m.ProviderID == id {
				t.Fatal("Doubao leaked into Gateway profile")
			}
		}
		for _, m := range a.forwardingModels(&profile) {
			if m.ProviderID == id {
				t.Fatal("Doubao leaked into Gateway catalog")
			}
		}
	}
	a.setExperimentalAPIMode(id, false)
	if a.isWebChat(a.conversation(nativeID)) || a.clientForProvider(id) != nil {
		t.Fatal("disabled API mode changed native history or retained inference")
	}
	state, err := a.storage.Load()
	if err != nil {
		t.Fatal(err)
	}
	loaded := &app{state: state}
	if loaded.isWebChat(loaded.conversation(nativeID)) || !loaded.isWebChat(loaded.conversation(webID)) {
		t.Fatal("chat identities changed after reload")
	}
	a.newChat()
	if !a.isWebChat(a.current()) {
		t.Fatal("disabled mode did not restore web new chats")
	}
}

func TestDoubaoCatalogAndSettingsAreLazy(t *testing.T) {
	a := providerTestApp(t)
	id, err := a.addProviderKind(store.ProviderKindDoubao, "Doubao", "")
	if err != nil {
		t.Fatal(err)
	}
	if a.provider(id).ExperimentalAPIMode || a.clientForProvider(id) != nil {
		t.Fatal("experimental mode enabled by default")
	}
	a.setExperimentalAPIMode(id, true)
	models, err := a.clientForProvider(id).Models(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != doubao.ModelID {
		t.Fatalf("unexpected local catalog: %v %v", models, err)
	}
	closed := false
	a.webSettings = &webChatView{providerID: "previous-provider", cancel: func() { closed = true }}
	a.providerSettingsID = id
	tester := ui.NewTester(a.providerSettings, 640, 500)
	tester.Frame()
	if !closed || a.webSettings != nil || len(a.webAPISessions) != 0 {
		t.Fatal("settings retained a previous login page or created a WebView")
	}
	if a.providerSecrets.(*memoryCredentials).entries[providerCredentialKey(id)] != nil {
		t.Fatal("web credentials were copied to API vault")
	}
}
