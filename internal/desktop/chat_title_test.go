package desktop

import (
	"testing"

	"github.com/fondoger/copilotchat/internal/store"
)

func TestAutoTitlePersistsAndPreservesUserAndProviderState(t *testing.T) {
	for _, scenario := range []string{"accepted", "manual-same-name", "renamed", "disabled", "disconnected", "deleted", "quitting", "setting-off", "multiline"} {
		t.Run(scenario, func(t *testing.T) {
			a := providerTestApp(t)
			client := a.clientForProvider(store.DefaultProviderID)
			a.state.Conversations = []store.Conversation{{ID: "chat", Title: "New Chat", ProviderID: store.DefaultProviderID}}
			title := "Autumn leaves"
			switch scenario {
			case "manual-same-name":
				a.state.Conversations[0].TitleManual = true
			case "renamed":
				a.state.Conversations[0].Title = "User title"
			case "disabled":
				a.state.Settings.Providers[0].Disabled = true
			case "disconnected":
				a.providers[store.DefaultProviderID].client = nil
			case "deleted":
				a.state.Conversations = nil
			case "quitting":
				a.quitting = true
			case "setting-off":
				a.state.Settings.ChatTitle = false
			case "multiline":
				title += "\nmore"
			}
			accepted := a.setAutoTitle(client, "chat", "New Chat", title)
			if accepted != (scenario == "accepted") {
				t.Fatalf("title accepted=%v for %s", accepted, scenario)
			}
			if accepted {
				saved, err := a.storage.Load()
				if err != nil || len(saved.Conversations) != 1 || saved.Conversations[0].Title != title {
					t.Fatalf("title was not persisted: %v", err)
				}
			} else if chat := a.conversation("chat"); chat != nil && chat.Title == title {
				t.Fatal("late automatic title overwrote protected state")
			}
		})
	}
}
