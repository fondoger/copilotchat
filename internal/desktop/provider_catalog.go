package desktop

import (
	"crypto/sha256"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/store"
)

func githubCatalogBinding(token []byte) string {
	if len(token) == 0 {
		return ""
	}
	return fmt.Sprintf("github:%x", sha256.Sum256(token))
}

func microsoftCatalogBinding(entry m365SavedSignIn) string {
	var tenant, account string
	switch {
	case entry.Mode == "windows-wam" && entry.WAM != nil:
		tenant, account = entry.WAM.Account.TenantID, entry.WAM.Account.ID
	case (entry.Mode == "copilot-cli" || entry.Mode == "copilot-broker") && entry.CLI != nil:
		tenant, account = entry.CLI.Account.TenantID, entry.CLI.Account.ID
	}
	if tenant == "" || account == "" {
		return ""
	}
	return fmt.Sprintf("microsoft:%x", sha256.Sum256([]byte(strings.ToLower(tenant+"\x00"+account))))
}

// Disk snapshots populate the picker only. The inference clients still validate
// their own live catalog before sending, and startup still refreshes once.
func (a *app) restoreProviderCatalog(id, binding string) {
	r := a.providerState(id)
	r.catalogBinding = binding
	r.models, r.modelsLoaded, r.accountID = nil, false, ""
	p := a.provider(id)
	if a.storage == nil || p == nil || r.client == nil || binding == "" {
		return
	}
	catalog, err := a.storage.LoadProviderCatalog(id)
	if err != nil || catalog.ProviderID != id || catalog.Kind != p.Kind || catalog.Host != p.Host || catalog.Binding != binding {
		return
	}
	r.models, r.accountID = catalog.Models, catalog.AccountID
	if p.Login == "" {
		p.Login = catalog.Login
	}
}

func (a *app) saveProviderCatalog(id string, fetched time.Time) {
	p, r := a.provider(id), a.providers[id]
	if a.storage == nil || p == nil || r == nil || r.catalogBinding == "" {
		return
	}
	catalog := store.ProviderCatalog{ProviderID: id, Kind: p.Kind, Host: p.Host, Binding: r.catalogBinding,
		AccountID: r.accountID, Login: p.Login, FetchedAt: fetched, Models: r.models}
	if err := a.storage.SaveProviderCatalog(catalog); err != nil {
		log.Printf("model catalog cache: %v", err)
	}
}

func (a *app) removeProviderCatalog(id string) {
	if a.storage != nil {
		if err := a.storage.DeleteProviderCatalog(id); err != nil {
			log.Printf("remove model catalog cache: %v", err)
		}
	}
}

func (a *app) invalidateProviderCatalog(id string) {
	if r := a.providers[id]; r != nil {
		r.catalogBinding, r.accountID = "", ""
		r.models, r.modelsLoaded = nil, false
		r.modelsAttemptedAt = time.Time{}
	}
	if p := a.provider(id); p != nil {
		p.ModelsRefreshedAt = time.Time{}
	}
	a.removeProviderCatalog(id)
}

// Keep acceptance and persistence behind the same identity/epoch guard, so a
// canceled request cannot repopulate a signed-out or replaced account on disk.
func (a *app) applyProviderModels(id string, client provider.Client, epoch uint64, models []provider.Model, account provider.AccountInfo, modelErr, accountErr error) {
	p, r := a.provider(id), a.providers[id]
	if p == nil || p.Disabled || r == nil || r.client != client || r.modelsEpoch != epoch || a.quitting {
		return
	}
	r.modelsBusy, r.modelsCancel = false, nil
	if modelErr != nil {
		r.status = modelErr.Error()
		return
	}
	if accountErr == nil && account.ID != "" && r.accountID != "" && account.ID != r.accountID {
		a.removeProviderCatalog(id)
	}
	if accountErr == nil {
		a.updateProviderAccount(id, account, nil, false)
	}
	r.models, r.modelsLoaded = models, true
	p.ModelsRefreshedAt = time.Now()
	r.status = fmt.Sprintf("Connected · %d models available", len(models))
	if accountErr != nil {
		a.updateProviderAccount(id, account, accountErr, false)
	}
	if available := a.providerModels(id); len(available) > 0 && a.state.Settings.ActiveProviderID == id && a.state.Settings.Model == "" {
		a.state.Settings.Model = available[0].ID
	}
	a.saveProviderCatalog(id, p.ModelsRefreshedAt)
	a.saveSettings()
}
