package desktop

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/fondoger/copilotchat/internal/credentials"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/provider/github"
	"github.com/fondoger/copilotchat/internal/store"
)

type credentialStore interface {
	Get(string) ([]byte, error)
	Set(string, []byte) error
	Delete(string) error
}

type providerRuntime struct {
	client                                provider.Client
	m365Auth                              interface{ Revoke() }
	models                                []provider.Model
	authBusy, modelsBusy, credentialsBusy bool
	authEpoch, modelsEpoch                uint64
	authCode                              github.DeviceCode
	authCancel, modelsCancel              context.CancelFunc
	status, credentialNotice, accountID   string
	modelsLoaded                          bool
	modelsAttemptedAt                     time.Time
}

const providerCatalogRefreshInterval = 24 * time.Hour
const providerCatalogRetryInterval = 15 * time.Minute

func providerCredentialKey(id string) string { return credentials.GitHubKey(id) }

func (a *app) credentials() credentialStore {
	if a.providerSecrets != nil {
		return a.providerSecrets
	}
	return mygo.Secrets
}

func (a *app) provider(id string) *store.Provider {
	for i := range a.state.Settings.Providers {
		if a.state.Settings.Providers[i].ID == id {
			return &a.state.Settings.Providers[i]
		}
	}
	return nil
}

func (a *app) providerState(id string) *providerRuntime {
	if a.providers == nil {
		a.providers = make(map[string]*providerRuntime)
	}
	if a.providers[id] == nil {
		a.providers[id] = &providerRuntime{}
	}
	return a.providers[id]
}

// loadProviders is called only during application startup. Tests inject a fake
// credentialStore; views never read passwords or access the network implicitly.
func (a *app) loadProviders() {
	for _, p := range a.state.Settings.Providers {
		r := a.providerState(p.ID)
		if store.IsWebProvider(p.Kind) {
			continue
		}
		if p.Kind == store.ProviderKindM365 {
			a.loadM365Provider(p, r)
			continue
		}
		token, err := a.credentials().Get(providerCredentialKey(p.ID))
		if errors.Is(err, mygo.ErrSecretNotFound) && p.ID == store.DefaultProviderID && p.Host == "github.com" {
			token, err = a.credentials().Get("github-copilot-token")
			if err == nil {
				if saveErr := a.credentials().Set(providerCredentialKey(p.ID), token); saveErr == nil {
					// Retain the old entry until the new one is durably accepted.
					_ = a.credentials().Delete("github-copilot-token")
				} else {
					r.credentialNotice = "Using the previous sign-in. Could not migrate saved credentials: " + saveErr.Error()
				}
			}
		}
		if err != nil {
			if !errors.Is(err, mygo.ErrSecretNotFound) {
				r.status = "Could not read saved sign-in: " + err.Error()
			}
			continue
		}
		if len(token) == 0 {
			continue
		}
		r.client, err = github.NewClientForHost(string(token), p.Host)
		if err != nil {
			r.client = nil
			r.status = err.Error()
		}
	}
}

func (a *app) selectedProviderID() string {
	if chat := a.current(); chat != nil && chat.ProviderID != "" {
		return chat.ProviderID
	}
	if a.state.Settings.ActiveProviderID != "" {
		return a.state.Settings.ActiveProviderID
	}
	return store.DefaultProviderID
}

func (a *app) selectedClient() provider.Client {
	if a.isWebChat(a.current()) {
		return nil
	}
	return a.clientForProvider(a.selectedProviderID())
}

func (a *app) clientForProvider(id string) provider.Client {
	if p := a.provider(id); p == nil || p.Disabled || store.IsWebProvider(p.Kind) {
		return nil
	}
	if r := a.providers[id]; r != nil {
		return r.client
	}
	return nil
}

func (a *app) selectedModel() string {
	if chat := a.current(); chat != nil && chat.Model != "" {
		return chat.Model
	}
	return a.newChatModel()
}

func (a *app) newChatModel() string {
	if p := a.provider(a.state.Settings.ActiveProviderID); p != nil && store.IsWebProvider(p.Kind) {
		return ""
	}
	if a.state.Settings.Model != "" {
		return a.state.Settings.Model
	}
	if models := a.providerModels(a.state.Settings.ActiveProviderID); len(models) > 0 {
		return models[0].ID
	}
	return ""
}

func (a *app) clientStillConnected(client provider.Client) bool {
	if client == nil {
		return false
	}
	for id, r := range a.providers {
		if p := a.provider(id); p != nil && !p.Disabled && !store.IsWebProvider(p.Kind) && r.client == client {
			return true
		}
	}
	return false
}

func (a *app) titleClient(chat *store.Conversation) (provider.Client, string) {
	if chat == nil || a.isWebChat(chat) {
		return nil, ""
	}
	if a.state.Settings.TitleModel != "" {
		providerID := a.state.Settings.TitleProviderID
		if providerID == "" {
			providerID = chat.ProviderID
		}
		return a.clientForProvider(providerID), a.state.Settings.TitleModel
	}
	return a.clientForProvider(chat.ProviderID), chat.Model
}

func modelAllowed(p *store.Provider, id string) bool {
	return p != nil && !p.Disabled && !store.IsWebProvider(p.Kind) && modelEnabled(p, id)
}

// modelEnabled is a saved model preference, independent of whether its provider
// is currently enabled. Disabled providers keep editable visibility choices.
func modelEnabled(p *store.Provider, id string) bool {
	return p != nil && !slices.Contains(p.DisabledModels, id)
}

func (a *app) setModelEnabled(providerID, modelID string, enabled bool) {
	p, r := a.provider(providerID), a.providers[providerID]
	if p == nil || r == nil || !slices.ContainsFunc(r.models, func(model provider.Model) bool { return model.ID == modelID }) {
		return
	}
	changed := false
	if enabled {
		if slices.Contains(p.DisabledModels, modelID) {
			p.DisabledModels = slices.DeleteFunc(p.DisabledModels, func(id string) bool { return id == modelID })
			changed = true
		}
	} else if !slices.Contains(p.DisabledModels, modelID) {
		p.DisabledModels = append(p.DisabledModels, modelID)
		changed = true
	}
	if changed {
		a.saveSettings()
	}
}

func (a *app) setAllModelsEnabled(providerID string, enabled bool) {
	p, r := a.provider(providerID), a.providers[providerID]
	if p == nil || r == nil || len(r.models) == 0 {
		return
	}
	changed := false
	for _, model := range r.models {
		if enabled {
			if slices.Contains(p.DisabledModels, model.ID) {
				p.DisabledModels = slices.DeleteFunc(p.DisabledModels, func(id string) bool { return id == model.ID })
				changed = true
			}
		} else if !slices.Contains(p.DisabledModels, model.ID) {
			p.DisabledModels = append(p.DisabledModels, model.ID)
			changed = true
		}
	}
	if changed {
		a.saveSettings()
	}
}

func (a *app) providerModels(id string) []provider.Model {
	p, r := a.provider(id), a.providers[id]
	if p == nil || r == nil || store.IsWebProvider(p.Kind) {
		return nil
	}
	var models []provider.Model
	for _, model := range r.models {
		if modelAllowed(p, model.ID) {
			models = append(models, model)
		}
	}
	return models
}

func (a *app) modelForProvider(providerID, modelID string) (provider.Model, bool) {
	if p := a.provider(providerID); p == nil || p.Disabled || store.IsWebProvider(p.Kind) {
		return provider.Model{}, false
	}
	if r := a.providers[providerID]; r != nil {
		for _, model := range r.models {
			if model.ID == modelID {
				return model, true
			}
		}
	}
	return provider.Model{}, false
}

func (a *app) chooseModel(providerID, modelID string) {
	p := a.provider(providerID)
	if p == nil || !modelAllowed(p, modelID) || a.streaming {
		return
	}
	if _, ok := a.modelForProvider(providerID, modelID); !ok {
		return
	}
	a.chooseChatProvider(providerID, modelID)
}

func (a *app) chooseWebProvider(providerID string) {
	p := a.provider(providerID)
	if p == nil || p.Disabled || !store.IsWebProvider(p.Kind) || a.streaming {
		return
	}
	a.chooseChatProvider(providerID, "")
}

func (a *app) chooseChatProvider(providerID, modelID string) {
	a.pendingChat = ""
	a.modelSelection++
	request := a.modelSelection
	chat := a.current()
	if a.isWebChat(chat) && chat.ProviderID != providerID && a.webChat != nil && a.webChat.view != nil {
		p := a.webChat
		id, generation, page := chat.ID, p.generation, p.view.Page()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			snapshot, err := readWebChatSnapshot(ctx, page)
			a.update(func() {
				if a.modelSelection != request || a.state.ActiveID != id || !a.currentWebChatView(p) || p.generation != generation {
					return
				}
				if err == nil {
					a.applyWebChatSnapshot(p, snapshot)
				}
				a.finishChatProvider(providerID, modelID, err == nil && !p.switching && !snapshot.Switching && snapshot.Session == p.session && !p.startedChat && !snapshot.Started && store.WebBlankURL(p.kind, snapshot.URL) && snapshot.URL == page.URL())
			})
		}()
		return
	}
	// No live page means this local draft has not yet opened a web document.
	a.finishChatProvider(providerID, modelID, a.webChat == nil || !a.webChat.startedChat)
}

func (a *app) finishChatProvider(providerID, modelID string, webUnused bool) {
	provider := a.provider(providerID)
	if provider == nil || provider.Disabled || a.streaming {
		return
	}
	web := store.IsWebProvider(provider.Kind)
	if !web {
		if _, ok := a.modelForProvider(providerID, modelID); !ok || !modelAllowed(provider, modelID) {
			return
		}
	}
	chat := a.current()
	wasWeb := a.isWebChat(chat)
	cross := chat != nil && (wasWeb != web || wasWeb && chat.ProviderID != providerID)
	reuse := chat != nil && len(chat.Messages) == 0 && chat.WebURL == "" && (!wasWeb || webUnused)
	a.state.Settings.ActiveProviderID, a.state.Settings.Model = providerID, modelID
	a.state.Settings.ReasoningEffort, a.state.Settings.ThinkingBudgetTokens = "", 0
	if cross && !reuse || chat == nil && web {
		a.newChat()
	} else if chat != nil && (!web || cross) {
		if wasWeb {
			if p := a.webChat; p != nil {
				p.resetDraft = true
				if p.cancel != nil {
					p.cancel()
				}
			}
			a.parkWebChat()
		}
		chat.ProviderID, chat.Model, chat.ReasoningEffort = providerID, modelID, ""
		chat.ThinkingBudgetTokens, chat.WebKind = 0, ""
		if web {
			chat.WebKind = provider.Kind
			chat.WebStarted = chat.WebStarted || a.draft != "" || len(a.attachments) != 0
		} else {
			a.composer.Focus()
		}
		a.rememberDraft()
	}
	a.saveSettings()
}

func (a *app) toggleModelPin(providerID, modelID string) {
	if p := a.provider(providerID); p != nil {
		if at := slices.Index(p.PinnedModels, modelID); at >= 0 {
			p.PinnedModels = slices.Delete(p.PinnedModels, at, at+1)
		} else {
			p.PinnedModels = append(p.PinnedModels, modelID)
		}
		a.saveSettings()
	}
}

func (a *app) addProvider(name, host string) (string, error) {
	return a.addProviderKind(store.ProviderKindGitHub, name, host)
}

func (a *app) addProviderKind(kind, name, host string) (string, error) {
	var err error
	switch kind {
	case store.ProviderKindGitHub:
		host, err = github.NormalizeHost(host)
	case store.ProviderKindM365:
		host = store.M365ProviderHost
	default:
		web, ok := store.WebProviderFor(kind)
		if !ok {
			return "", errors.New("unknown provider type")
		}
		host = web.Host
	}
	if err != nil {
		return "", err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = providerKindName(kind)
	}
	p := store.Provider{ID: newID(), Kind: kind, Name: name, Host: host}
	a.state.Settings.Providers = append(a.state.Settings.Providers, p)
	if active := a.provider(a.state.Settings.ActiveProviderID); active == nil || active.Disabled {
		a.state.Settings.ActiveProviderID, a.state.Settings.Model = p.ID, ""
		a.state.Settings.ReasoningEffort, a.state.Settings.ThinkingBudgetTokens = "", 0
	}
	a.providerSettingsID = p.ID
	a.providerState(p.ID)
	a.saveSettings()
	return p.ID, nil
}

func (a *app) enabledProviderCount() int {
	count := 0
	for _, p := range a.state.Settings.Providers {
		if !p.Disabled {
			count++
		}
	}
	return count
}

func (a *app) resetDefaultProvider() {
	a.state.Settings.ActiveProviderID = ""
	a.state.Settings.Model, a.state.Settings.ReasoningEffort = "", ""
	a.state.Settings.ThinkingBudgetTokens = 0
	for _, p := range a.state.Settings.Providers {
		if p.Disabled {
			continue
		}
		if a.state.Settings.ActiveProviderID == "" {
			a.state.Settings.ActiveProviderID = p.ID
		}
		if models := a.providerModels(p.ID); len(models) > 0 && a.clientForProvider(p.ID) != nil {
			a.state.Settings.ActiveProviderID, a.state.Settings.Model = p.ID, models[0].ID
			break
		}
	}
}

func (a *app) cancelProviderWork(id string) {
	a.closeWebProvider(id)
	a.cancelProviderLogin(id)
	a.cancelProviderModels(id)
	if chat := a.conversation(a.streamChat); chat != nil && chat.ProviderID == id {
		a.stop()
	}
	for chatID, job := range a.titleJobs {
		chat := a.conversation(chatID)
		if (chat != nil && chat.ProviderID == id) || a.state.Settings.TitleProviderID == id {
			job.cancel()
			delete(a.titleJobs, chatID)
		}
	}
}

func (a *app) removeProvider(id string) error {
	index := slices.IndexFunc(a.state.Settings.Providers, func(p store.Provider) bool { return p.ID == id })
	if index < 0 {
		return nil
	}
	r := a.providerState(id)
	if r.credentialsBusy {
		return errors.New("this provider's sign-in is still being saved; try again")
	}
	r.credentialsBusy = true
	err := a.clearProviderCredentials(id)
	r.credentialsBusy = false
	if err != nil {
		if a.state.Settings.Providers[index].Kind == store.ProviderKindM365 {
			// Microsoft sources are revoked before deleting their saved binding.
			// A vault failure must not leave a dead source displayed as connected.
			a.cancelProviderWork(id)
			r.client, r.models, r.modelsLoaded = nil, nil, false
			r.status = "Signed out for this session."
			a.state.Settings.Providers[index].Login = ""
			a.saveSettings()
		}
		r.credentialNotice = "Could not delete provider: its saved sign-in could not be removed. Try again. " + err.Error()
		return err
	}
	a.cancelProviderWork(id)
	a.state.Settings.Providers = slices.Delete(a.state.Settings.Providers, index, index+1)
	delete(a.providers, id)
	if a.state.Settings.ActiveProviderID == id {
		a.resetDefaultProvider()
	}
	if a.state.Settings.TitleProviderID == id {
		a.state.Settings.TitleProviderID, a.state.Settings.TitleModel = "", ""
	}
	if a.providerSettingsID == id {
		a.providerSettingsID = ""
		if len(a.state.Settings.Providers) > 0 {
			a.providerSettingsID = a.state.Settings.Providers[min(index, len(a.state.Settings.Providers)-1)].ID
		}
	}
	a.saveSettings()
	return nil
}

func (a *app) setProviderEnabled(id string, enabled bool) {
	p := a.provider(id)
	if p == nil || p.Disabled == !enabled {
		return
	}
	p.Disabled = !enabled
	if !enabled {
		a.cancelProviderWork(id)
		a.providerState(id).modelsAttemptedAt = time.Time{}
		if a.state.Settings.ActiveProviderID == id {
			a.resetDefaultProvider()
		}
	}
	a.saveSettings()
	if enabled && providerCatalogRefreshDue(*p, a.providers[id], time.Now()) {
		a.refreshProviderModels(id)
	}
}

// refreshDueProviderModels is called on the UI thread at startup and by the
// application's periodic timer. Network work is dispatched by provider; disabled
// providers retain their saved credentials and catalog without making requests.
func (a *app) refreshDueProviderModels(now time.Time) {
	if a.quitting {
		return
	}
	for _, p := range a.state.Settings.Providers {
		if providerCatalogRefreshDue(p, a.providers[p.ID], now) {
			a.refreshProviderModels(p.ID)
		}
	}
}

func providerCatalogRefreshDue(p store.Provider, r *providerRuntime, now time.Time) bool {
	if p.Disabled || store.IsWebProvider(p.Kind) || r == nil || r.client == nil || r.modelsBusy || r.authBusy {
		return false
	}
	if !r.modelsAttemptedAt.IsZero() && now.Sub(r.modelsAttemptedAt) >= 0 && now.Sub(r.modelsAttemptedAt) < providerCatalogRetryInterval {
		return false
	}
	return (!r.modelsLoaded && len(r.models) == 0) || p.ModelsRefreshedAt.IsZero() || now.Before(p.ModelsRefreshedAt) || now.Sub(p.ModelsRefreshedAt) >= providerCatalogRefreshInterval
}

func (a *app) beginProviderLogin(id string) {
	p := a.provider(id)
	if p == nil || p.Disabled || store.IsWebProvider(p.Kind) {
		return
	}
	if p.Kind == store.ProviderKindM365 {
		a.beginM365Login(id)
		return
	}
	r := a.providerState(id)
	if r.authBusy || r.credentialsBusy || a.quitting {
		return
	}
	client, err := github.NewClientForHost("", p.Host)
	if err != nil {
		r.status = err.Error()
		return
	}
	r.authEpoch++
	epoch := r.authEpoch
	r.authBusy, r.authCode, r.status = true, github.DeviceCode{}, "Requesting a device code…"
	ctx, cancel := context.WithCancel(context.Background())
	r.authCancel = cancel
	failed := func(err error) {
		a.update(func() {
			defer cancel()
			if !a.currentProviderLogin(id, epoch, ctx) {
				return
			}
			r.authBusy, r.authCancel, r.authCode = false, nil, github.DeviceCode{}
			r.status = err.Error()
		})
	}
	go func() {
		code, err := client.BeginLogin(ctx)
		if err != nil {
			failed(err)
			return
		}
		a.update(func() {
			if a.currentProviderLogin(id, epoch, ctx) {
				r.authCode = code
				r.status = "Enter this code on GitHub to connect your Copilot account."
			}
		})
		token, err := client.PollLogin(ctx, code)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			failed(err)
			return
		}
		accountCtx, accountCancel := context.WithTimeout(ctx, 20*time.Second)
		account, accountErr := client.Account(accountCtx)
		accountCancel()
		a.update(func() {
			defer cancel()
			if !a.currentProviderLogin(id, epoch, ctx) {
				return
			}
			r.credentialsBusy = true
			defer func() { r.credentialsBusy = false }()
			secretErr := a.credentials().Set(providerCredentialKey(id), []byte(token))
			if !a.currentProviderLogin(id, epoch, ctx) {
				if secretErr == nil {
					_ = a.credentials().Delete(providerCredentialKey(id))
				}
				return
			}
			a.cancelProviderModels(id)
			r.client, r.models = client, nil
			r.modelsLoaded = false
			r.authBusy, r.authCancel, r.authCode = false, nil, github.DeviceCode{}
			r.status = "Connected."
			r.credentialNotice = ""
			a.updateProviderAccount(id, account, accountErr, true)
			if secretErr != nil {
				r.credentialNotice = "Connected for this session. Could not save sign-in: " + secretErr.Error()
			}
			a.saveSettings()
			a.refreshProviderModels(id)
		})
	}()
}

func (a *app) currentProviderLogin(id string, epoch uint64, ctx context.Context) bool {
	r := a.providers[id]
	p := a.provider(id)
	return r != nil && p != nil && !p.Disabled && !a.quitting && r.authBusy && r.authEpoch == epoch && ctx.Err() == nil
}

func (a *app) cancelProviderLogin(id string) {
	if r := a.providers[id]; r != nil {
		r.authEpoch++
		if r.authCancel != nil {
			r.authCancel()
			r.authCancel = nil
		}
		r.authBusy, r.authCode = false, github.DeviceCode{}
	}
}

func (a *app) cancelLogin() {
	for id := range a.providers {
		a.cancelProviderLogin(id)
	}
}

func (a *app) cancelProviderModels(id string) {
	if r := a.providers[id]; r != nil {
		r.modelsEpoch++
		if r.modelsCancel != nil {
			r.modelsCancel()
			r.modelsCancel = nil
		}
		r.modelsBusy = false
	}
}

func (a *app) cancelModelRefresh() {
	for id := range a.providers {
		a.cancelProviderModels(id)
	}
}

func (a *app) signOutProvider(id string) {
	r := a.providerState(id)
	if r.credentialsBusy {
		return
	}
	a.cancelProviderWork(id)
	r.client, r.models = nil, nil
	r.modelsLoaded, r.accountID = false, ""
	if p := a.provider(id); p != nil {
		p.Login = ""
	}
	r.credentialsBusy = true
	defer func() { r.credentialsBusy = false }()
	err := a.clearProviderCredentials(id)
	r.status = "Signed out."
	r.credentialNotice = ""
	if err != nil {
		r.credentialNotice = "Signed out for this session. Could not remove saved sign-in: " + err.Error()
	}
	a.saveSettings()
}

func (a *app) clearProviderCredentials(id string) error {
	if p := a.provider(id); p != nil && store.IsWebProvider(p.Kind) {
		// Browser storage is shared. Removing a provider must not sign out others.
		return nil
	}
	if p := a.provider(id); p != nil && p.Kind == store.ProviderKindM365 {
		return a.clearM365Credentials(id)
	}
	err := a.credentials().Delete(providerCredentialKey(id))
	if errors.Is(err, mygo.ErrSecretNotFound) {
		err = nil
	}
	if id == store.DefaultProviderID {
		if legacyErr := a.credentials().Delete("github-copilot-token"); legacyErr != nil && !errors.Is(legacyErr, mygo.ErrSecretNotFound) && err == nil {
			err = legacyErr
		}
	}
	return err
}

func (a *app) updateProviderAccount(id string, account provider.AccountInfo, err error, newSignIn bool) {
	p := a.provider(id)
	if p == nil {
		return
	}
	r := a.providerState(id)
	if newSignIn {
		// A successful sign-in can replace the account even when /user fails.
		p.Login, r.accountID = "", ""
	}
	if err != nil {
		label := "GitHub username"
		if p.Kind == store.ProviderKindM365 {
			label = "Microsoft account"
		}
		r.status = strings.TrimSuffix(r.status, ".") + ". Could not load " + label + ": " + err.Error()
		return
	}
	p.Login = account.Login
	if account.ID != "" {
		r.accountID = account.ID
	}
}

func (a *app) refreshProviderModels(id string) {
	p := a.provider(id)
	r := a.providerState(id)
	if p == nil || p.Disabled || store.IsWebProvider(p.Kind) || r.client == nil || r.modelsBusy || a.quitting {
		return
	}
	r.modelsEpoch++
	epoch, client := r.modelsEpoch, r.client
	r.modelsBusy = true
	r.modelsAttemptedAt = time.Now()
	timeout := 45 * time.Second
	if p.Kind == store.ProviderKindM365 {
		timeout = 90 * time.Second // Allow the CLI broker to renew before loading the catalog.
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	r.modelsCancel = cancel
	go func() {
		defer cancel()
		models, err := client.Models(ctx)
		var account provider.AccountInfo
		var accountErr error
		if err == nil {
			account, accountErr = client.Account(ctx)
		}
		a.update(func() {
			if r.client != client || r.modelsEpoch != epoch || a.quitting || a.provider(id) == nil || a.provider(id).Disabled {
				return
			}
			r.modelsBusy, r.modelsCancel = false, nil
			if err != nil {
				r.status = err.Error()
				return
			}
			r.models = models
			r.modelsLoaded = true
			a.provider(id).ModelsRefreshedAt = time.Now()
			r.status = fmt.Sprintf("Connected · %d models available", len(models))
			a.updateProviderAccount(id, account, accountErr, false)
			if available := a.providerModels(id); len(available) > 0 && a.state.Settings.ActiveProviderID == id && a.state.Settings.Model == "" {
				a.state.Settings.Model = available[0].ID
			}
			a.saveSettings()
		})
	}()
}
