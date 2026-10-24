package desktop

import (
	"errors"
	"testing"
	"time"

	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/provider/github"
	"github.com/fondoger/copilotchat/internal/store"
)

var integratedWebKinds = []string{
	store.ProviderKindDeepSeekWeb,
	store.ProviderKindClaudeWeb,
	store.ProviderKindGeminiWeb,
	store.ProviderKindChatGPTWeb,
}

func TestWebProviderIntegrationDraftSwitches(t *testing.T) {
	for _, from := range integratedWebKinds {
		for _, to := range integratedWebKinds {
			if from == to {
				continue
			}
			t.Run(from+"/"+to, func(t *testing.T) {
				a := providerTestApp(t)
				first, err := a.addProviderKind(from, "First", "ignored.invalid")
				if err != nil {
					t.Fatal(err)
				}
				second, err := a.addProviderKind(to, "Second", "ignored.invalid")
				if err != nil {
					t.Fatal(err)
				}
				a.newChat()
				id := a.state.ActiveID
				a.draft = "Keep my unsent draft"
				a.attachments = []store.Attachment{{Name: "notes.txt", Data: "ZmlsZQ=="}}
				for _, selection := range []struct{ id, kind string }{{first, from}, {second, to}, {first, from}} {
					a.chooseWebProvider(selection.id)
					chat := a.current()
					if chat.ID != id || chat.ProviderID != selection.id || chat.WebKind != selection.kind || chat.Model != "" || !a.isWebChat(chat) {
						t.Fatalf("unused draft was not converted to selected web provider: %#v", chat)
					}
					if a.newChatModel() != "" || a.draft != "Keep my unsent draft" || len(a.attachments) != 1 {
						t.Fatal("web switch lost draft or retained API model")
					}
				}
				a.chooseModel(store.DefaultProviderID, "same")
				if a.current().ID != id || a.current().WebKind != "" || a.isWebChat(a.current()) || len(a.state.Conversations) != 1 || a.draft != "Keep my unsent draft" || len(a.attachments) != 1 {
					t.Fatal("returning to API duplicated the chat or lost its draft")
				}
			})
		}
	}
}

func TestWebProviderIntegrationPreservesEstablishedAndOrphanedChats(t *testing.T) {
	for index, kind := range integratedWebKinds {
		t.Run(kind, func(t *testing.T) {
			a := providerTestApp(t)
			id, err := a.addProviderKind(kind, "Web", "")
			if err != nil {
				t.Fatal(err)
			}
			nextKind := integratedWebKinds[(index+1)%len(integratedWebKinds)]
			next, err := a.addProviderKind(nextKind, "Next", "")
			if err != nil {
				t.Fatal(err)
			}
			a.chooseWebProvider(id)
			webID := a.state.ActiveID
			a.current().WebURL = "https://example.invalid/preserved-session"
			a.current().Title = "Existing session"
			a.chooseWebProvider(next)
			if a.state.ActiveID == webID || a.current().WebKind != nextKind {
				t.Fatal("switching web kinds reused an established session")
			}
			if chat := a.conversation(webID); chat.ProviderID != id || chat.WebKind != kind || chat.WebURL != "https://example.invalid/preserved-session" {
				t.Fatalf("established session changed: %#v", chat)
			}
			if err := a.removeProvider(id); err != nil {
				t.Fatal(err)
			}
			chat := a.conversation(webID)
			if !a.isWebChat(chat) || a.webChatKind(chat) != kind {
				t.Fatal("orphaned conversation lost its web identity")
			}
			a.selectChat(webID)
			a.draft = "Must not be sent through the API"
			if a.send() || len(a.current().Messages) != 0 {
				t.Fatal("orphaned web session entered native send pipeline")
			}
		})
	}
}

func TestWebProviderIntegrationStartedSessionCannotConvert(t *testing.T) {
	for index, kind := range integratedWebKinds {
		t.Run(kind, func(t *testing.T) {
			a := providerTestApp(t)
			id, err := a.addProviderKind(kind, "Web", "")
			if err != nil {
				t.Fatal(err)
			}
			nextKind := integratedWebKinds[(index+1)%len(integratedWebKinds)]
			next, err := a.addProviderKind(nextKind, "Next", "")
			if err != nil {
				t.Fatal(err)
			}
			a.chooseWebProvider(id)
			webID := a.state.ActiveID
			a.webChat = &webChatView{chatID: webID, providerID: id, kind: kind, startedChat: true}
			a.chooseWebProvider(next)
			if a.state.ActiveID == webID || a.current().WebKind != nextKind || a.conversation(webID).WebKind != kind {
				t.Fatal("converted a web session whose first message was sent before URL binding")
			}
			destroyWebChatView(a.webParked)
		})
	}
}

func TestWebProviderIntegrationExcludesAPIPipeline(t *testing.T) {
	for _, kind := range integratedWebKinds {
		t.Run(kind, func(t *testing.T) {
			a := providerTestApp(t)
			id, err := a.addProviderKind(kind, "Web", "github.com")
			if err != nil {
				t.Fatal(err)
			}
			web, _ := store.WebProviderFor(kind)
			if a.provider(id).Host != web.Host {
				t.Fatal("provider accepted a non-registry host")
			}
			form := &providerForm{Name: "Renamed", Host: "attacker.invalid"}
			a.autosaveProviderForm(id, form)
			if form.Error != "" || a.provider(id).Name != "Renamed" || a.provider(id).Host != web.Host {
				t.Fatal("web settings did not preserve registry host while renaming")
			}
			secrets := a.providerSecrets.(*memoryCredentials)
			secrets.entries[providerCredentialKey(id)] = []byte("not-an-api-token")
			secrets.deleteErrors = map[string]error{providerCredentialKey(id): errors.New("locked vault")}
			a.loadProviders()
			if a.providerState(id).client != nil {
				t.Fatal("web provider loaded API credentials")
			}
			// Even stale runtime data must not expose API models, clients, or jobs.
			r := a.providerState(id)
			r.client = github.NewClient("stale")
			r.models = []provider.Model{{ID: "stale"}}
			if a.clientForProvider(id) != nil || a.clientStillConnected(r.client) || len(a.providerModels(id)) != 0 || modelAllowed(a.provider(id), "stale") {
				t.Fatal("web provider exposed stale API runtime")
			}
			if _, ok := a.modelForProvider(id, "stale"); ok {
				t.Fatal("web provider exposed an API model")
			}
			if providerCatalogRefreshDue(*a.provider(id), r, time.Now()) {
				t.Fatal("web provider scheduled API catalog refresh")
			}
			a.refreshProviderModels(id)
			a.beginProviderLogin(id)
			if r.modelsBusy || r.authBusy {
				t.Fatal("web provider started API refresh or device-code login")
			}
			if err := a.removeProvider(id); err != nil {
				t.Fatalf("web provider removal accessed credential vault: %v", err)
			}
			if string(secrets.entries[providerCredentialKey(id)]) != "not-an-api-token" {
				t.Fatal("web provider modified API credentials")
			}
		})
	}
}
