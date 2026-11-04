package desktop

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/provider/webchat"
	"github.com/fondoger/copilotchat/internal/store"
)

func TestNativeWebModesAreLazyAndStayOutsideGateway(t *testing.T) {
	for _, kind := range []string{store.ProviderKindChatGPTWeb, store.ProviderKindClaudeWeb} {
		t.Run(kind, func(t *testing.T) {
			a := providerTestApp(t)
			id, err := a.addProviderKind(kind, "Website", "")
			if err != nil {
				t.Fatal(err)
			}
			if a.provider(id).ExperimentalAPIMode || a.clientForProvider(id) != nil {
				t.Fatal("web API mode enabled by default")
			}
			a.chooseWebProvider(id)
			webID := a.state.ActiveID
			a.current().WebStarted = true
			if kind == store.ProviderKindClaudeWeb {
				a.current().WebURL = "https://claude.ai/chat/00000000-0000-4000-8000-000000000001"
			} else {
				a.current().WebURL = "https://chatgpt.com/c/00000000-0000-4000-8000-000000000001"
			}
			a.setExperimentalAPIMode(id, true)
			client := a.clientForProvider(id)
			if client == nil {
				t.Fatal("enabled mode has no native client")
			}
			if _, ok := client.(provider.RawClient); ok {
				t.Fatal("web client gained raw Gateway access")
			}
			r := a.providerState(id)
			if r.modelsLoaded || len(r.models) > 0 || len(a.webAPISessions) > 0 {
				t.Fatal("enabling mode opened a page or fabricated an account catalog")
			}
			a.providerSettingsID = id
			closed := false
			a.webSettings = &webChatView{providerID: "previous", cancel: func() { closed = true }}
			ui.NewTester(a.providerSettings, 640, 500).Frame()
			if !closed || a.webSettings != nil || len(a.webAPISessions) > 0 {
				t.Fatal("API settings render retained or created a web page")
			}
			r.models = []provider.Model{{ID: "observed-model", Name: "Observed"}}
			a.chooseModel(id, "observed-model")
			nativeID := a.state.ActiveID
			if nativeID == webID || a.isWebChat(a.current()) || !a.isWebChat(a.conversation(webID)) {
				t.Fatal("switching modes changed an existing conversation identity")
			}
			a.syncForwardingProviders()
			for _, p := range a.state.Settings.ForwardProfiles {
				for _, m := range p.Models {
					if m.ProviderID == id {
						t.Fatal("web model entered a Gateway profile")
					}
				}
				for _, m := range a.forwardingModels(&p) {
					if m.ProviderID == id {
						t.Fatal("web model entered the Gateway catalog")
					}
				}
			}
			a.setExperimentalAPIMode(id, false)
			if a.clientForProvider(id) != nil || a.isWebChat(a.conversation(nativeID)) {
				t.Fatal("disabling mode erased native identity or retained inference")
			}
		})
	}
}

func TestWebRecoveryLinksDoNotCrossSitesOrSurviveKindChanges(t *testing.T) {
	a := providerTestApp(t)
	chatgpt, err := a.addProviderKind(store.ProviderKindChatGPTWeb, "ChatGPT", "")
	if err != nil {
		t.Fatal(err)
	}
	claude, err := a.addProviderKind(store.ProviderKindClaudeWeb, "Claude", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ id, url string }{{chatgpt, webchat.ChatGPTRecoveryURL}, {claude, webchat.ClaudeRecoveryURL}} {
		if a.appLinkAction(item.id, item.url) == nil {
			t.Fatal("own recovery link unavailable")
		}
		for _, suffix := range []string{"?provider=other", "#fragment", "/../open"} {
			if a.appLinkAction(item.id, item.url+suffix) != nil {
				t.Fatal("nonexact recovery link accepted")
			}
		}
	}
	if a.appLinkAction(chatgpt, webchat.ClaudeRecoveryURL) != nil || a.appLinkAction(claude, webchat.ChatGPTRecoveryURL) != nil {
		t.Fatal("recovery action crossed website identity")
	}
	action := a.appLinkAction(chatgpt, webchat.ChatGPTRecoveryURL)
	a.provider(chatgpt).Kind = store.ProviderKindClaudeWeb
	action()
	if a.webAPILoginID != "" || a.settingsWin != nil {
		t.Fatal("stale recovery action opened a different website")
	}
}
