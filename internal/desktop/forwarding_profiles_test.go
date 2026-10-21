package desktop

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/localproxy"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/provider/github"
	"github.com/fondoger/copilotchat/internal/store"
)

func TestProfileCodexCatalogTracksEditsAndDeletion(t *testing.T) {
	a := profileTestApp(t)
	a.forwarder = localproxy.New(localproxy.DefaultKey)
	t.Cleanup(a.stopForwarding)
	a.syncForwardingProviders()
	p := a.selectedForwardProfile()
	path := a.storage.AgentCatalogPath(p.ID)
	readAliases := func() []string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var catalog struct{ Models []struct{ Slug string } }
		if json.Unmarshal(data, &catalog) != nil {
			t.Fatal("invalid persisted catalog")
		}
		aliases := []string{}
		for _, model := range catalog.Models {
			aliases = append(aliases, model.Slug)
		}
		return aliases
	}
	if !slices.Equal(readAliases(), []string{"github-copilot/gpt-5.5", "m365-copilot/gpt-5.5"}) {
		t.Fatal("initial export differs")
	}
	p.Models[0].Enabled = false
	a.saveSettings()
	if !slices.Equal(readAliases(), []string{"m365-copilot/gpt-5.5"}) {
		t.Fatal("catalog did not track edits")
	}
	a.renameForwardProfile(p, "codex-profile")
	if path != a.storage.AgentCatalogPath(p.ID) {
		t.Fatal("rename broke catalog path")
	}
	a.deleteForwardProfile(p.ID)
	if len(readAliases()) != 0 {
		t.Fatal("deleted profile left a usable catalog")
	}
}

func profileTestApp(t *testing.T) *app {
	a := newSettingsTestApp(t)
	a.state.Settings.ForwardProfiles[0].Models = nil
	m := provider.Model{ID: "gpt-5.5", Name: "GPT 5.5", Endpoints: []string{"/responses", "/v1/messages"}}
	a.providers[store.DefaultProviderID].models = []provider.Model{m}
	a.state.Settings.Providers = append(a.state.Settings.Providers, store.Provider{ID: "work", Name: "Microsoft 365", Kind: store.ProviderKindM365})
	a.providers["work"] = &providerRuntime{client: github.NewClient("test-token"), models: []provider.Model{m}}
	a.saveSettings()
	return a
}

func TestForwardProfilesKeepIndependentSelections(t *testing.T) {
	a := profileTestApp(t)
	p := a.selectedForwardProfile()
	original := p.ID
	a.setForwardEnabled(p, 0, false)
	a.addForwardProfile(true)
	copy := a.selectedForwardProfile()
	if copy.ID == original || copy.Models[0].Enabled {
		t.Fatal("profile choices not duplicated")
	}
	a.setForwardEnabled(copy, 0, true)
	a.state = settingsSavedState(t, a)
	a.chooseForwardProfile(original)
	p = a.selectedForwardProfile()
	if p.Models[0].Enabled {
		t.Fatal("duplicate choices leaked into original")
	}
	for _, invalid := range []string{"../escape", "a/b", "name?query", "with spaces"} {
		if a.renameForwardProfile(p, invalid) == "" {
			t.Fatal("invalid URL accepted", invalid)
		}
	}
	a.provider("work").Disabled = true
	a.providers["work"].models = nil
	a.syncForwardingProviders()
	if len(a.forwardingModels(p)) != 0 {
		t.Fatal("disabled model still routable")
	}
	a.provider("work").Disabled = false
	a.providers["work"].models = []provider.Model{{ID: "gpt-5.5", Endpoint: "/responses"}, {ID: "new-model", Endpoint: "/responses"}}
	a.saveSettings()
	if len(p.Models) != 3 || p.Models[0].Enabled || !p.Models[2].Enabled {
		t.Fatal("catalog refresh reset preferences")
	}
	a.deleteForwardProfile(original)
	a.deleteForwardProfile(a.selectedForwardProfile().ID)
	a.state = settingsSavedState(t, a)
	if a.selectedForwardProfile() != nil {
		t.Fatal("deleted last profile reappeared")
	}
}

func TestForwardProfileControls(t *testing.T) {
	a := profileTestApp(t)
	view := func(c *ui.Context) {
		ui.Column(c).Fill().Gap(10).Children(func() {
			a.forwardingProfileSettings(c)
			if p := a.selectedForwardProfile(); p != nil {
				a.forwardingModelSettings(c, p)
			}
		})
	}
	tt := ui.NewTester(view, 760, 1000)
	if err := tt.RightClick("Profile default"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Rename"); err != nil {
		t.Fatal(err)
	}
	settingsClick(t, tt, "Profile name")
	tt.Command("selectAll")
	tt.Type("my-claude-code-profile")
	settingsClick(t, tt, "Save profile name")
	if a.selectedForwardProfile().Name != "my-claude-code-profile" || !tt.HasText("http://127.0.0.1:39321/my-claude-code-profile") {
		t.Fatal("URL did not follow name")
	}
	settingsClick(t, tt, "Share github.com gpt-5.5")
	if a.selectedForwardProfile().Models[0].Enabled {
		t.Fatal("selection not saved")
	}
	settingsClick(t, tt, "Profile actions my-claude-code-profile")
	if err := tt.ChooseMenuItem("Duplicate"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("my-claude-code-profile-copy") || a.selectedForwardProfile().Models[0].Enabled {
		t.Fatal("duplicate UI failed")
	}
	settingsClick(t, tt, "Deselect all")
	if len(a.forwardingModels(a.selectedForwardProfile())) != 0 {
		t.Fatal("deselect failed")
	}
	settingsClick(t, tt, "Select all")
	if len(a.forwardingModels(a.selectedForwardProfile())) != 2 {
		t.Fatal("select failed")
	}
	a.confirmForwarding = func(string, string, string) bool { return true }
	if err := tt.RightClick("Profile my-claude-code-profile-copy"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Delete"); err != nil {
		t.Fatal(err)
	}
	if a.selectedForwardProfile().Name != "my-claude-code-profile" {
		t.Fatal("delete UI failed")
	}
	settingsClick(t, tt, "New profile")
	if len(a.state.Settings.ForwardProfiles) != 2 || a.selectedForwardProfile().Name != "profile" {
		t.Fatal("create UI failed")
	}
}
