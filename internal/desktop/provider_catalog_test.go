package desktop

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/provider/microsoft/cli"
	"github.com/fondoger/copilotchat/internal/provider/microsoft/entra"
	"github.com/fondoger/copilotchat/internal/store"
)

func cachedProviderApp(t *testing.T) *app {
	t.Helper()
	a := providerTestApp(t)
	r := a.providerState(store.DefaultProviderID)
	r.catalogBinding = githubCatalogBinding([]byte("saved-token"))
	r.accountID = "account-one"
	r.models = []provider.Model{{ID: "gpt-cached", Name: "Cached model", Endpoint: "/responses", Vision: true, PDF: true,
		NativeSearch: true, NativeSearchKnown: true, Capabilities: json.RawMessage(`{"server_tools":{"web_search":{"supported":true}}}`)}}
	a.state.Settings.Model = "gpt-cached"
	a.provider(store.DefaultProviderID).ModelsRefreshedAt = time.Now()
	if err := a.credentials().Set(providerCredentialKey(store.DefaultProviderID), []byte("saved-token")); err != nil {
		t.Fatal(err)
	}
	a.saveProviderCatalog(store.DefaultProviderID, a.provider(store.DefaultProviderID).ModelsRefreshedAt)
	a.saveSettings()
	return a
}

func TestCachedModelsAvailableImmediatelyAfterRestartAndRefreshStillDue(t *testing.T) {
	first := cachedProviderApp(t)
	state, err := first.storage.Load()
	if err != nil {
		t.Fatal(err)
	}
	a := &app{state: state, storage: first.storage, providerSecrets: first.providerSecrets}
	a.loadProviders() // Restores credentials and snapshots, without making any model request.
	models := a.providerModels(store.DefaultProviderID)
	if len(models) != 1 || models[0].ID != "gpt-cached" || !models[0].Vision || !models[0].PDF || !provider.SupportsNativeSearch(models[0]) {
		t.Fatalf("restart did not restore full picker metadata: %+v", models)
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() { a.modelPicker(c) })
	}, 720, 600)
	settingsClick(t, tt, "Choose model")
	settingsClick(t, tt, "Choose Cached model from Personal")
	if a.state.Settings.Model != "gpt-cached" {
		t.Fatal("cached model could not be selected before a network refresh")
	}
	r := a.providers[store.DefaultProviderID]
	if r.modelsLoaded || !providerCatalogRefreshDue(*a.provider(store.DefaultProviderID), r, time.Now()) {
		t.Fatal("recent disk cache suppressed the startup network refresh")
	}
	r.modelsBusy = true
	if len(a.providerModels(store.DefaultProviderID)) != 1 || providerCatalogRefreshDue(*a.provider(store.DefaultProviderID), r, time.Now()) {
		t.Fatal("background refresh hid models or allowed a duplicate request")
	}
}

func TestCatalogRefreshRetainsOfflineModelsAndPersistsEmptySuccess(t *testing.T) {
	a := cachedProviderApp(t)
	id := store.DefaultProviderID
	r := a.providers[id]
	r.modelsLoaded, r.modelsBusy = false, true
	r.modelsAttemptedAt = time.Now()
	oldTime := a.provider(id).ModelsRefreshedAt
	a.applyProviderModels(id, r.client, r.modelsEpoch, nil, provider.AccountInfo{}, errors.New("offline"), nil)
	cached, err := a.storage.LoadProviderCatalog(id)
	if err != nil || len(r.models) != 1 || len(cached.Models) != 1 || r.modelsLoaded || !a.provider(id).ModelsRefreshedAt.Equal(oldTime) {
		t.Fatalf("offline refresh discarded the cached catalog: %+v %v", cached, err)
	}
	if providerCatalogRefreshDue(*a.provider(id), r, time.Now()) {
		t.Fatal("offline refresh was not throttled")
	}
	a.applyProviderModels(id, r.client, r.modelsEpoch, nil, provider.AccountInfo{ID: "account-one", Login: "account"}, nil, nil)
	cached, err = a.storage.LoadProviderCatalog(id)
	if err != nil || !r.modelsLoaded || len(r.models) != 0 || cached.Models == nil || len(cached.Models) != 0 {
		t.Fatalf("successful empty catalog retained old models: %+v %v", cached, err)
	}
}

func TestProviderCatalogRestorationRejectsOtherIdentityAndHost(t *testing.T) {
	for _, change := range []string{"credential", "host", "kind", "missing-credential"} {
		t.Run(change, func(t *testing.T) {
			a := cachedProviderApp(t)
			id := store.DefaultProviderID
			binding := a.providers[id].catalogBinding
			switch change {
			case "credential":
				binding = githubCatalogBinding([]byte("another-account"))
			case "host":
				a.provider(id).Host = "other.ghe.com"
			case "kind":
				a.provider(id).Kind = store.ProviderKindM365
			case "missing-credential":
				a.providers[id].client = nil
			}
			a.restoreProviderCatalog(id, binding)
			if len(a.providers[id].models) != 0 || a.providers[id].modelsLoaded {
				t.Fatal("catalog crossed an identity/host boundary")
			}
		})
	}
}

func TestStaleProviderRefreshCannotOverwriteCachedModels(t *testing.T) {
	for _, stale := range []string{"epoch", "client", "disabled", "removed", "quitting"} {
		t.Run(stale, func(t *testing.T) {
			a := cachedProviderApp(t)
			id := store.DefaultProviderID
			r := a.providers[id]
			client, epoch := r.client, r.modelsEpoch
			switch stale {
			case "epoch":
				r.modelsEpoch++
			case "client":
				r.client = nil
			case "disabled":
				a.provider(id).Disabled = true
			case "removed":
				a.state.Settings.Providers = nil
			case "quitting":
				a.quitting = true
			}
			a.applyProviderModels(id, client, epoch, []provider.Model{{ID: "stale-result"}}, provider.AccountInfo{ID: "other"}, nil, nil)
			cached, err := a.storage.LoadProviderCatalog(id)
			if err != nil || len(cached.Models) != 1 || cached.Models[0].ID != "gpt-cached" || r.models[0].ID != "gpt-cached" {
				t.Fatalf("stale result changed cached models: %+v %v", cached, err)
			}
		})
	}
}

func TestProviderCatalogInvalidatedOnSignOutRemovalAndHostChange(t *testing.T) {
	for _, action := range []string{"signout", "failed-signout", "remove", "host"} {
		t.Run(action, func(t *testing.T) {
			a := cachedProviderApp(t)
			id := store.DefaultProviderID
			switch action {
			case "signout", "failed-signout":
				if action == "failed-signout" {
					a.providerSecrets.(*memoryCredentials).deleteErrors = map[string]error{providerCredentialKey(id): errors.New("vault locked")}
				}
				a.signOutProvider(id)
			case "remove":
				if err := a.removeProvider(id); err != nil {
					t.Fatal(err)
				}
			case "host":
				a.providers[id].client = nil
				form := &providerForm{Name: "Renamed", Host: "another.ghe.com"}
				a.autosaveProviderForm(id, form)
				if form.Error != "" {
					t.Fatal(form.Error)
				}
			}
			if _, err := a.storage.LoadProviderCatalog(id); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("old catalog survived %s: %v", action, err)
			}
			if r := a.providers[id]; r != nil && (len(r.models) != 0 || r.catalogBinding != "") {
				t.Fatal("invalidated catalog remained available in memory")
			}
		})
	}
}

func TestDisabledProviderRetainsCatalogButCannotAdvertiseOrRefresh(t *testing.T) {
	a := cachedProviderApp(t)
	id := store.DefaultProviderID
	a.setProviderEnabled(id, false)
	if _, err := a.storage.LoadProviderCatalog(id); err != nil {
		t.Fatal("disabling discarded the reusable catalog", err)
	}
	if len(a.providerModels(id)) != 0 || providerCatalogRefreshDue(*a.provider(id), a.providers[id], time.Now()) {
		t.Fatal("disabled provider exposed cached models or requested refresh")
	}
}

func TestMicrosoftCatalogBindingUsesPinnedAccountRatherThanLoginMode(t *testing.T) {
	entry := m365SavedSignIn{Mode: "copilot-cli", CLI: &cli.Reference{Account: entra.Account{TenantID: "TENANT", ID: "ACCOUNT"}}}
	binding := microsoftCatalogBinding(entry)
	entry.Mode = "copilot-broker"
	if binding == "" || microsoftCatalogBinding(entry) != binding {
		t.Fatal("same pinned account got a different catalog identity")
	}
	entry.CLI.Account.ID = "other"
	if microsoftCatalogBinding(entry) == binding {
		t.Fatal("different Microsoft identities shared a catalog")
	}
}
