package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/credentials"
	"github.com/fondoger/copilotchat/internal/plugininstall"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/store"
	"github.com/fondoger/copilotchat/internal/wam"
)

const fixtureResource = "00000000-0000-4000-8000-000000000002"

func examplePlugin(t *testing.T, authenticated bool) plugininstall.Entry {
	t.Helper()
	dir := t.TempDir()
	auth := ""
	script := `export function catalog(){throw Error("offline fixture")} export function models(){return []} export function inference(){return {}}`
	if authenticated {
		auth = `,"wamResource":"` + fixtureResource + `"`
		script += ` export function signIn(){return host.wam.signIn()}`
	}
	manifest := `{"id":"example","name":"Example Models","version":"1.0","main":"index.js","network":["https://example.invalid"]` + auth + `}`
	for name, contents := range map[string]string{"plugin.json": manifest, "index.js": script} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return plugininstall.Entry{ID: "example", Name: "Example Models", Source: dir, Revision: "revision-one", Dir: dir}
}

func TestPluginRegistrationRequiresExplicitPerInstanceApproval(t *testing.T) {
	a := providerTestApp(t)
	a.installedPlugins = []plugininstall.Entry{examplePlugin(t, true)}
	a.settingsPage = "Providers"
	tester := ui.NewTester(a.settingsView, 1000, 1000)
	for i := 0; i < 2; i++ {
		settingsClick(t, tester, "Add provider")
		settingsClick(t, tester, "Add Example Models")
		if tester.HasText("Sign in with GitHub") || !tester.HasText("Sign in") {
			t.Fatal("plugin account has the wrong sign-in method")
		}
	}
	ps := settingsSavedState(t, a).Settings.Providers
	if len(ps) != 4 || ps[2].Kind != "plugin:example" || ps[2].Kind != ps[3].Kind || ps[2].ID == ps[3].ID || ps[2].PluginSource != a.installedPlugins[0].Source || ps[3].Name != "Example Models 2" || a.clientForProvider("work") == nil {
		t.Fatalf("account setup: %+v", ps)
	}
	enter := func(label, value string) {
		t.Helper()
		settingsClick(t, tester, label)
		tester.Command("selectAll")
		tester.Type(value)
	}
	const clientID = "00000000-0000-4000-8000-000000000001"
	enter("Client ID", clientID)
	settingsClick(t, tester, "Save registration")
	if a.provider(ps[3].ID).WAMGrant != nil {
		t.Fatal("the manifest supplied its own audience or trusted origins")
	}
	enter("Resource ID", fixtureResource)
	enter("Trusted HTTPS origins", "https://other.invalid")
	settingsClick(t, tester, "Save registration")
	if a.provider(ps[3].ID).WAMGrant != nil {
		t.Fatal("saved registration with unapproved destinations")
	}
	enter("Trusted HTTPS origins", "https://example.invalid\nhttps://other.invalid")
	if settingsSavedState(t, a).Settings.Providers[3].WAMGrant != nil {
		t.Fatal("typing saved a registration without approval")
	}
	settingsClick(t, tester, "Save registration")
	ps = settingsSavedState(t, a).Settings.Providers
	if ps[3].WAMGrant == nil || ps[3].WAMGrant.ClientID != clientID || ps[3].WAMGrant.Resource != fixtureResource || len(ps[3].WAMGrant.Origins) != 2 || ps[3].WAMGrant.Origins[0] != "https://example.invalid" || ps[3].WAMGrant.Origins[1] != "https://other.invalid" || ps[2].WAMGrant != nil || a.clientForProvider(ps[3].ID) != nil {
		t.Fatal("saving a grant must authorize just this instance and leave it signed out", a.providers[ps[3].ID].status)
	}
	// A valid saved grant must not make Sign in use an unsaved edit. With no
	// native window, an enabled handler would report that sign-in needs one.
	enter("Resource ID", clientID)
	status := a.providers[ps[3].ID].status
	settingsClick(t, tester, "Sign in")
	if a.providers[ps[3].ID].status != status {
		t.Fatal("sign-in ran with an unsaved edit")
	}
	settingsClick(t, tester, "Save registration")
	if g := settingsSavedState(t, a).Settings.Providers[3].WAMGrant; g == nil || g.Resource != fixtureResource {
		t.Fatal("an unapproved edit replaced the saved registration")
	}
	settingsClick(t, tester, "Clear registration")
	if settingsSavedState(t, a).Settings.Providers[3].WAMGrant != nil {
		t.Fatal("cleared grant persisted")
	}
}

func TestPluginRevisionAndRemovalRevokeClientsButKeepHistory(t *testing.T) {
	a := providerTestApp(t)
	entry := examplePlugin(t, false)
	a.installedPlugins = []plugininstall.Entry{entry}
	id, err := a.addProviderKind("plugin:example", "Offline fixture", "")
	if err != nil {
		t.Fatal(err)
	}
	r := a.providerState(id)
	old := r.client
	if old == nil || old.Host() != "example.invalid" {
		t.Fatal("anonymous plugin did not connect", r.credentialNotice, r.status)
	}
	a.state.ActiveID = "existing"
	a.state.Conversations = []store.Conversation{{ID: "existing", ProviderID: id, Model: "offered", Messages: []store.Message{{ID: "m", Role: "user", Content: "preserve this"}}}}
	a.applyProviderModels(id, old, r.modelsEpoch, []provider.Model{{ID: "offered", Endpoint: "/responses"}}, provider.AccountInfo{}, nil, nil)
	state := settingsSavedState(t, a)
	restart := &app{state: state, installedPlugins: []plugininstall.Entry{entry}, storage: a.storage, providerSecrets: a.providerSecrets}
	restart.loadProviders()
	if m := restart.providerModels(id); len(m) != 1 || m[0].ID != "offered" || !providerCatalogRefreshDue(*restart.provider(id), restart.providers[id], time.Now()) {
		t.Fatal("restart lost cached models or skipped the initial refresh", m)
	}
	old = restart.clientForProvider(id)
	epoch := restart.providers[id].modelsEpoch
	restart.installedPlugins[0].Revision = "revision-two"
	restart.reloadPluginProviders(entry.ID)
	if restart.clientForProvider(id) == nil || restart.clientForProvider(id) == old || len(restart.providerModels(id)) != 0 {
		t.Fatal("new revision retained old code or catalog")
	}
	if _, err := old.Account(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatal("old client was not revoked", err)
	}
	restart.applyProviderModels(id, old, epoch, []provider.Model{{ID: "stale"}}, provider.AccountInfo{}, nil, nil)
	if len(restart.providerModels(id)) != 0 {
		t.Fatal("old worker repopulated the new catalog")
	}
	restart.installedPlugins = nil
	restart.reloadPluginProviders(entry.ID)
	if restart.clientForProvider(id) != nil || restart.current().Messages[0].Content != "preserve this" || restart.current().ProviderID != id {
		t.Fatal("uninstall lost history or retained a route")
	}
	other := entry
	other.Source = "another-source"
	restart.installedPlugins = []plugininstall.Entry{other}
	restart.reloadPluginProviders(entry.ID)
	if restart.clientForProvider(id) != nil {
		t.Fatal("old history rebound to a different source")
	}
	other.Source = entry.Source
	other.RepoID = entry.RepoID + 1
	restart.installedPlugins = []plugininstall.Entry{other}
	restart.reloadPluginProviders(entry.ID)
	if restart.clientForProvider(id) != nil {
		t.Fatal("old history rebound to a different repository at the same address")
	}
	restart.installedPlugins = []plugininstall.Entry{entry}
	restart.reloadPluginProviders(entry.ID)
	if restart.clientForProvider(id) == nil || restart.current().ProviderID != id {
		t.Fatal("same-source reinstall did not recover the instance")
	}
}

// Use a synthetic persisted account reference: restoring, changing grants and
// forgetting it must all work offline, without invoking a system account picker.
func TestPluginAccountRestoreRejectsChangedHostGrant(t *testing.T) {
	a := providerTestApp(t)
	entry := examplePlugin(t, true)
	a.installedPlugins = []plugininstall.Entry{entry}
	grant := &wam.Profile{ClientID: "00000000-0000-4000-8000-000000000001", Resource: fixtureResource, Origins: []string{"https://example.invalid"}}
	id, err := a.addProviderKind("plugin:example", "My account", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.setPluginGrant(id, grant); err != nil {
		t.Fatal(err)
	}
	b := pluginAccountBinding{Plugin: entry.ID, Source: entry.Source, Instance: id, RepoID: entry.RepoID}
	profile, _ := json.Marshal(grant)
	saved := []byte(fmt.Sprintf(`{"plugin":"example","reference":{"id":"opaque-wam-account","profileHash":"%x","account":{"id":"00000000-0000-4000-8000-000000000003","tenant_id":"00000000-0000-4000-8000-000000000004","username":"account@example.test"}}}`, sha256.Sum256(profile)))
	r := a.providerState(id)
	r.authBusy = true
	a.finishPluginLogin(id, r.authEpoch, t.Context(), saved, nil)
	state := settingsSavedState(t, a)
	data, _ := json.Marshal(state)
	if strings.Contains(string(data), "opaque-wam-account") || strings.Contains(string(data), "profileHash") {
		t.Fatal("account reference leaked into settings")
	}
	a = &app{state: state, installedPlugins: []plugininstall.Entry{entry}, storage: a.storage, providerSecrets: a.providerSecrets}
	a.loadProviders()
	r = a.providerState(id)
	if r.client == nil || a.provider(id).Login != "account@example.test" || r.modelsLoaded {
		t.Fatal("pinned account not shown before model discovery", r.status, r.credentialNotice)
	}
	old := r.client
	account, err := old.Account(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a.applyProviderModels(id, old, r.modelsEpoch, []provider.Model{{ID: "cached"}}, account, nil, nil)
	cache, err := a.storage.LoadProviderCatalog(id)
	if err != nil {
		t.Fatal(err)
	}
	if cache.AccountID == account.ID || strings.Contains(cache.Binding, "opaque-wam-account") {
		t.Fatal("cache exposed the saved account reference")
	}
	changed := *grant
	changed.Origins = []string{"https://example.invalid", "https://extra.invalid"}
	if err := a.setPluginGrant(id, &changed); err == nil {
		t.Fatal("changed a connected instance's registration")
	}
	// A settings snapshot restored separately from its vault must be checked
	// even when the updated grant still covers everything the plugin requests.
	a.provider(id).WAMGrant = &changed
	a.reloadPluginProviders(entry.ID)
	if r.client != nil || a.provider(id).Login != "" || len(a.providerModels(id)) != 0 || r.credentialNotice == "" {
		t.Fatal("changed grant inherited old login or models")
	}
	if _, err := old.Account(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatal("previous grant was not revoked")
	}
	// Re-approving even the original registration must forget the stale vault
	// reference. Cancel a pending picker and deliver its result after reapproval.
	if err := a.setPluginGrant(id, grant); err != nil {
		t.Fatal(err)
	}
	if _, err := b.load(a.credentials()); !errors.Is(err, mygo.ErrSecretNotFound) {
		t.Fatal("restoring the original grant revived its account", err)
	}
	r.authBusy = true
	epoch := r.authEpoch
	if err := a.setPluginGrant(id, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.setPluginGrant(id, grant); err != nil {
		t.Fatal(err)
	}
	r.authBusy = true
	a.finishPluginLogin(id, epoch, t.Context(), saved, nil)
	if r.client != nil || !r.authBusy {
		t.Fatal("stale picker result changed the new login")
	}
	if _, err := b.load(a.credentials()); !errors.Is(err, mygo.ErrSecretNotFound) {
		t.Fatal("stale picker saved its reference", err)
	}
	a.finishPluginLogin(id, r.authEpoch, t.Context(), saved, nil)
	if r.client == nil {
		t.Fatal("fresh sign-in failed", r.status)
	}
	a.signOutProvider(id)
	if _, err := b.load(a.credentials()); !errors.Is(err, mygo.ErrSecretNotFound) || r.client != nil || a.provider(id).WAMGrant == nil {
		t.Fatal("sign-out should retain the registration only", err)
	}
	anon := examplePlugin(t, false)
	anon.Source, anon.Revision = entry.Source, "now-anonymous"
	a.installedPlugins = []plugininstall.Entry{anon}
	a.reloadPluginProviders(entry.ID)
	if r.client != nil {
		t.Fatal("anonymous update inherited account authority")
	}
	if err := a.setPluginGrant(id, nil); err != nil || r.client == nil || a.provider(id).Login != "" {
		t.Fatal("clearing the registration did not recover anonymous access", err)
	}
}

// A legacy snapshot may name a retired provider and even reuse the default ID.
// No settings, sign-in or removal action may touch the GitHub vault in that case.
func TestUnknownProviderNeverUsesGitHubCredentials(t *testing.T) {
	a := providerTestApp(t)
	id := store.DefaultProviderID
	a.state.Settings.Providers = []store.Provider{{ID: id, Kind: "retired-kind", Name: "Old account", Host: "github.com"}}
	a.state.Conversations = []store.Conversation{{ID: "kept", ProviderID: id, Messages: []store.Message{{Role: "user", Content: "history"}}}}
	a.saveSettings()
	a.state = settingsSavedState(t, a)
	a.providers = nil
	vault := &noVaultAccess{t}
	a.providerSecrets = vault
	a.loadProviders()
	a.beginProviderLogin(id)
	if a.clientForProvider(id) != nil || a.providerState(id).authBusy {
		t.Fatal("unknown kind was activated")
	}
	a.signOutProvider(id)
	if err := a.removeProvider(id); err != nil {
		t.Fatal(err)
	}
	state := settingsSavedState(t, a)
	if len(state.Conversations) != 1 || state.Conversations[0].ProviderID != id || state.Conversations[0].Messages[0].Content != "history" {
		t.Fatal("retired provider history was lost")
	}
}

type noVaultAccess struct{ t *testing.T }

func (v *noVaultAccess) Get(string) ([]byte, error) {
	v.t.Fatal("unexpected vault read")
	return nil, nil
}
func (v *noVaultAccess) Set(string, []byte) error { v.t.Fatal("unexpected vault write"); return nil }
func (v *noVaultAccess) Delete(string) error      { v.t.Fatal("unexpected vault delete"); return nil }

type boundedPluginVault struct {
	memoryCredentials
	failKey string
}

func (v *boundedPluginVault) Set(key string, data []byte) error {
	if key == v.failKey || len(data) > 2560 {
		return errors.New("vault write refused")
	}
	return v.memoryCredentials.Set(key, data)
}

func TestPluginVaultRotationAndAccountIsolation(t *testing.T) {
	vault := &boundedPluginVault{memoryCredentials: memoryCredentials{entries: map[string][]byte{}}}
	account := pluginAccountBinding{Plugin: "example", Instance: "provider-1", Source: "fixture/repository", RepoID: 42}
	large := []byte(strings.Repeat("opaque-account-reference", 500))
	if err := account.save(vault, large); err != nil {
		t.Fatal(err)
	}
	other := account
	other.Instance = "provider-2"
	if err := other.save(vault, []byte("second")); err != nil {
		t.Fatal(err)
	}
	key := credentials.PluginKey(account.Plugin, account.Instance)
	count := len(vault.entries)
	vault.failKey = key
	if err := account.save(vault, []byte("replacement")); err == nil {
		t.Fatal("failed rotation was hidden")
	}
	if got, err := account.load(vault); err != nil || string(got) != string(large) || len(vault.entries) != count {
		t.Fatal("failed vault commit damaged the saved account", err)
	}
	vault.failKey = ""
	for _, alter := range []func(*pluginAccountBinding){func(b *pluginAccountBinding) { b.Source = "other/repository" }, func(b *pluginAccountBinding) { b.RepoID++ }, func(b *pluginAccountBinding) { b.Plugin = "different" }, func(b *pluginAccountBinding) { b.Instance = "absent" }} {
		changed := account
		alter(&changed)
		if got, err := changed.load(vault); err == nil || len(got) != 0 {
			t.Fatal("credential crossed its binding")
		}
	}
	if err := credentials.Delete(vault, key); err != nil {
		t.Fatal(err)
	}
	if _, err := account.load(vault); !errors.Is(err, mygo.ErrSecretNotFound) {
		t.Fatal("deleted credential survived", err)
	}
	if got, err := other.load(vault); err != nil || string(got) != "second" {
		t.Fatal("removed the wrong credential", err)
	}
}
