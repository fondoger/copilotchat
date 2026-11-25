package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/fondoger/copilotchat/internal/credentials"
	"github.com/fondoger/copilotchat/internal/plugin"
	"github.com/fondoger/copilotchat/internal/plugininstall"
	"github.com/fondoger/copilotchat/internal/store"
	"github.com/fondoger/copilotchat/internal/wam"
)

func (a *app) installedPlugin(id string) *plugininstall.Entry {
	for i := range a.installedPlugins {
		if a.installedPlugins[i].ID == id && id != "" {
			return &a.installedPlugins[i]
		}
	}
	return nil
}

func (a *app) providerName(kind string) string {
	if entry := a.installedPlugin(store.PluginID(kind)); entry != nil {
		return entry.Name
	}
	return providerKindName(kind)
}

func (r *providerRuntime) revokePluginClient() {
	if c, ok := r.client.(*plugin.Client); ok {
		c.Revoke()
		r.client = nil
	}
}

// Source is the canonical repository identity recorded by the installer. A
// different repository may publish the same manifest ID after an uninstall.
type pluginAccountBinding struct {
	Plugin, Source, Instance string
	RepoID                   int64
}

type pluginSignIn struct {
	Binding pluginAccountBinding
	Saved   []byte
}

func (b pluginAccountBinding) save(secrets credentials.Store, saved []byte) error {
	if len(saved) == 0 {
		return errors.New("plugin returned an empty sign-in")
	}
	data, err := json.Marshal(pluginSignIn{Binding: b, Saved: saved})
	if err != nil {
		return err
	}
	return credentials.Save(secrets, credentials.PluginKey(b.Plugin, b.Instance), data)
}

func (b pluginAccountBinding) load(secrets credentials.Store) ([]byte, error) {
	data, err := credentials.Load(secrets, credentials.PluginKey(b.Plugin, b.Instance))
	if err != nil {
		return nil, err
	}
	var signIn pluginSignIn
	if json.Unmarshal(data, &signIn) != nil || signIn.Binding != b || len(signIn.Saved) == 0 {
		return nil, errors.New("saved sign-in does not match this plugin account")
	}
	return signIn.Saved, nil
}

// Only this digest is persisted with the models. A different install revision,
// provider instance or saved account must get a fresh catalog.
func (b pluginAccountBinding) catalog(revision string, saved []byte) string {
	data, _ := json.Marshal([]any{b, revision, sha256.Sum256(saved)})
	return fmt.Sprintf("plugin:%x", sha256.Sum256(data))
}

func (a *app) loadPluginProvider(p store.Provider, pluginID string) {
	a.provider(p.ID).Login = ""
	r := a.providerState(p.ID)
	entry := a.installedPlugin(pluginID)
	if entry == nil {
		r.status = "Install the provider plugin to use this account."
		return
	}
	if entry.Source == "" || p.PluginSource != entry.Source || p.PluginRepoID != entry.RepoID {
		r.status = "This plugin has a different installation source. Add a new provider to use it."
		return
	}
	plug, err := plugin.Load(entry.Dir)
	if err != nil {
		r.status = "Could not load plugin: " + err.Error()
		return
	}
	if plug.Manifest.ID != pluginID {
		r.status = "Installed plugin identity does not match this provider."
		return
	}
	binding := pluginAccountBinding{Plugin: pluginID, Source: entry.Source, Instance: p.ID, RepoID: entry.RepoID}
	r.plugin, r.pluginRevision = plug, entry.Revision
	a.provider(p.ID).Host = strings.TrimPrefix(plug.Manifest.Network[0], "https://")
	if plug.Manifest.WAMResource != "" && p.WAMGrant == nil {
		return // A plugin's requested resource and network are never a host grant.
	}
	if plug.Manifest.WAMResource == "" && p.WAMGrant != nil {
		r.credentialNotice = "This plugin no longer requests Windows sign-in. Clear the registration to connect."
		return
	}
	if err := plug.ValidateGrant(p.WAMGrant); err != nil {
		r.credentialNotice = err.Error()
		return
	}
	saved, err := binding.load(a.credentials())
	if errors.Is(err, mygo.ErrSecretNotFound) {
		if plug.Manifest.WAMResource != "" {
			return
		}
		saved = nil
	} else if err != nil {
		r.credentialNotice = "Could not read saved sign-in: " + err.Error()
		return
	}
	client, err := plug.Client(saved, p.WAMGrant)
	if err != nil {
		r.credentialNotice = err.Error()
		return
	}
	a.connectPluginClient(p.ID, client, binding.catalog(entry.Revision, saved))
}

// The plugin account is local metadata from the verified sign-in. Publish it
// immediately, even when the upstream model service is offline.
func (a *app) connectPluginClient(id string, client *plugin.Client, binding string) {
	r := a.providerState(id)
	r.client = client
	a.restoreProviderCatalog(id, binding)
	account, err := client.Account(context.Background())
	a.updateProviderAccount(id, account, err, true)
}

// Called on the UI thread after the installed list has been replaced. Keep the
// configured instances, their vault entries and conversation bindings intact.
func (a *app) reloadPluginProviders(pluginID string) {
	for _, p := range a.state.Settings.Providers {
		if p.Kind != store.PluginKindPrefix+pluginID {
			continue
		}
		a.cancelProviderWork(p.ID)
		r := a.providerState(p.ID)
		r.revokePluginClient()
		a.invalidateProviderCatalog(p.ID)
		r.plugin, r.pluginRevision, r.status, r.credentialNotice = nil, "", "", ""
		a.loadPluginProvider(p, pluginID)
		if providerCatalogRefreshDue(p, r, time.Now()) {
			a.refreshProviderModels(p.ID)
		}
	}
	a.saveSettings() // Also updates Gateway targets and cancels their old requests.
}

// Grants are immutable once published. The UI edits separate draft strings,
// and a login worker holds its registration snapshot until its epoch ends.
func (a *app) setPluginGrant(id string, grant *wam.Profile) error {
	p, r := a.provider(id), a.providerState(id)
	if p == nil || store.PluginID(p.Kind) == "" || r.credentialsBusy || r.client != nil {
		return errors.New("sign out before changing this registration")
	}
	if grant != nil {
		if r.plugin == nil {
			return errors.New("install the original plugin before saving a registration")
		}
		if err := r.plugin.ValidateGrant(grant); err != nil {
			return err
		}
		approved := *grant
		approved.Origins = slices.Clone(grant.Origins)
		grant = &approved
	}
	a.cancelProviderWork(id)
	r.credentialsBusy = true
	defer func() { r.credentialsBusy = false }()
	// Delete even on first registration: a vault entry may have survived a
	// failed sign-out or a restored snapshot. It must never regain authority.
	if err := a.clearProviderCredentials(id); err != nil {
		return fmt.Errorf("could not remove the previous sign-in: %w", err)
	}
	a.invalidateProviderCatalog(id)
	previous := p.WAMGrant
	p.WAMGrant, p.Login = grant, ""
	r.credentialNotice = ""
	if r.plugin != nil {
		r.status = ""
	}
	if err := a.saveState(); err != nil {
		p.WAMGrant = previous
		return fmt.Errorf("could not save registration: %w", err)
	}
	a.reconnectAnonymousPlugin(id)
	return nil
}

func (a *app) reconnectAnonymousPlugin(id string) {
	p, r := a.provider(id), a.providerState(id)
	if p == nil || p.WAMGrant != nil || r.plugin == nil || r.plugin.Manifest.WAMResource != "" {
		return
	}
	r.status = ""
	a.loadPluginProvider(*p, store.PluginID(p.Kind))
	a.syncForwardingProviders()
	if providerCatalogRefreshDue(*p, r, time.Now()) {
		a.refreshProviderModels(id)
	}
}

func (a *app) beginPluginLogin(id string) {
	p, r := a.provider(id), a.providerState(id)
	if p == nil || p.Disabled || r.plugin == nil || r.plugin.Manifest.WAMResource == "" || r.client != nil || r.authBusy || r.credentialsBusy || a.quitting {
		return
	}
	plug, grant := r.plugin, p.WAMGrant
	if err := plug.ValidateGrant(grant); err != nil {
		r.status = err.Error()
		return
	}
	owner := a.settingsWin
	if owner == nil {
		owner = a.win
	}
	if owner == nil {
		r.status = "Sign-in requires an application window."
		return
	}
	hwnd := owner.NativeHandle()
	r.authEpoch++
	epoch := r.authEpoch
	ctx, cancel := context.WithCancel(context.Background())
	r.authCancel, r.authBusy, r.status = cancel, true, "Waiting for account sign-in…"
	go func() {
		loginCtx, done := context.WithTimeout(ctx, 10*time.Minute)
		saved, err := plug.SignIn(loginCtx, hwnd, grant)
		done()
		a.update(func() {
			defer cancel()
			if a.providers[id] == r && r.plugin == plug {
				a.finishPluginLogin(id, epoch, ctx, saved, err)
			}
		})
	}()
}

func (a *app) finishPluginLogin(id string, epoch uint64, ctx context.Context, saved []byte, err error) {
	// Registration changes, sign-out and installer reloads all cancel this epoch.
	if !a.currentProviderLogin(id, epoch, ctx) {
		return
	}
	p, r := a.provider(id), a.providerState(id)
	r.authBusy, r.authCancel = false, nil
	if err != nil {
		r.status = err.Error()
		return
	}
	client, err := r.plugin.Client(saved, p.WAMGrant)
	if err != nil {
		r.status = err.Error()
		return
	}
	r.credentialsBusy = true
	defer func() { r.credentialsBusy = false }()
	a.cancelProviderModels(id)
	a.invalidateProviderCatalog(id)
	r.status, r.credentialNotice = "Connected.", ""
	binding := pluginAccountBinding{Plugin: r.plugin.Manifest.ID, Source: p.PluginSource, Instance: id, RepoID: p.PluginRepoID}
	a.connectPluginClient(id, client, binding.catalog(r.pluginRevision, saved))
	if err := binding.save(a.credentials(), saved); err != nil {
		r.credentialNotice = "Could not save sign-in: " + err.Error()
	}
	a.saveSettings()
	a.refreshProviderModels(id)
}
