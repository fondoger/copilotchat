package desktop

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/provider/github"
	"github.com/fondoger/copilotchat/internal/store"
)

type memoryCredentials struct {
	entries      map[string][]byte
	failSet      bool
	deleteErrors map[string]error
}

func (s *memoryCredentials) Get(key string) ([]byte, error) {
	if value, ok := s.entries[key]; ok {
		return value, nil
	}
	return nil, mygo.ErrSecretNotFound
}
func (s *memoryCredentials) Set(key string, value []byte) error {
	if s.failSet {
		return errors.New("locked credential store")
	}
	s.entries[key] = append([]byte(nil), value...)
	return nil
}
func (s *memoryCredentials) Delete(key string) error {
	if err := s.deleteErrors[key]; err != nil {
		return err
	}
	delete(s.entries, key)
	return nil
}

func providerTestApp(t *testing.T) *app {
	t.Helper()
	disk, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := store.DefaultState()
	state.Settings.Providers[0].Name = "Personal"
	state.Settings.Providers = append(state.Settings.Providers, store.Provider{ID: "work", Name: "Work", Host: "example.ghe.com", Login: "work-user"})
	work, err := github.NewClientForHost("test-work", "example.ghe.com")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{state: state, storage: disk, providerSecrets: &memoryCredentials{entries: map[string][]byte{}}, providers: map[string]*providerRuntime{
		store.DefaultProviderID: {client: github.NewClient("test-personal"), models: []provider.Model{{ID: "same", Name: "Shared model", Vision: true}, {ID: "personal-only", Name: "Personal model"}}},
		"work":                  {client: work, models: []provider.Model{{ID: "same", Name: "Shared model", Reasoning: true}, {ID: "work-only", Name: "Work model"}}},
	}}
	return a
}

func TestLegacyCredentialsMigrateWithoutLosingSignIn(t *testing.T) {
	for _, failSet := range []bool{false, true} {
		a := &app{state: store.DefaultState()}
		secrets := &memoryCredentials{entries: map[string][]byte{"github-copilot-token": []byte("legacy-fixture")}, failSet: failSet}
		a.providerSecrets = secrets
		a.loadProviders()
		if a.selectedClient() == nil || a.selectedClient().Host() != "github.com" {
			t.Fatal("legacy login lost")
		}
		_, oldExists := secrets.entries["github-copilot-token"]
		_, newExists := secrets.entries[providerCredentialKey(store.DefaultProviderID)]
		if failSet && (!oldExists || newExists) {
			t.Fatal("failed migration removed previous credential")
		}
		if failSet && !strings.Contains(a.providerState(store.DefaultProviderID).credentialNotice, "Could not migrate") {
			t.Fatal("failed migration did not retain a credential warning")
		}
		if !failSet && (oldExists || !newExists) {
			t.Fatal("migration did not atomically adopt the new credential key")
		}
	}
}

func TestProviderModelVisibilityTableSelectionAndBulkActionsPersist(t *testing.T) {
	a := providerTestApp(t)
	a.providerSettingsID = "work"
	tt := ui.NewTester(func(c *ui.Context) { ui.Column(c).Fill().Children(func() { a.providerSettings(c) }) }, 600, 1000)
	for _, label := range []string{"Model / ID", "Enabled", "same", "work-only", "Enable Shared model", "Enable Work model", "Select all", "Deselect all"} {
		if _, found := tt.Find(label); !found {
			t.Fatalf("model visibility table lacks %q", label)
		}
	}
	for _, label := range []string{"All", "Whitelist", "Blacklist", "Visibility", "Shown", "Hidden"} {
		if _, found := tt.Find(label); found {
			t.Fatalf("obsolete model visibility control remains: %q", label)
		}
	}
	if len(a.providerModels("work")) != 2 {
		t.Fatal("models are not all enabled by default")
	}
	settingsClick(t, tt, "Enable Work model")
	if models := a.providerModels("work"); len(models) != 1 || models[0].ID != "same" {
		t.Fatalf("unchecking an enabled row did not hide its model: %#v", models)
	}
	if _, found := tt.Find("Enable Work model"); !found {
		t.Fatal("disabled model disappeared from the visibility table")
	}
	state := settingsSavedState(t, a)
	if !reflect.DeepEqual(state.Settings.Providers[1].DisabledModels, []string{"work-only"}) || len(state.Settings.Providers[0].DisabledModels) != 0 {
		t.Fatal("model visibility edit was not saved to the selected provider")
	}
	settingsClick(t, tt, "Deselect all")
	if len(a.providerModels("work")) != 0 || len(a.providerModels(store.DefaultProviderID)) != 2 {
		t.Fatal("deselect all did not hide all selected provider models in isolation")
	}
	state = settingsSavedState(t, a)
	if modelAllowed(&state.Settings.Providers[1], "same") || modelAllowed(&state.Settings.Providers[1], "work-only") || len(state.Settings.Providers[0].DisabledModels) != 0 {
		t.Fatal("deselect all was not persisted to only the selected provider")
	}
	settingsClick(t, tt, "Select all")
	if len(a.providerModels("work")) != 2 || len(a.providerModels(store.DefaultProviderID)) != 2 {
		t.Fatal("select all did not restore the selected provider's models")
	}
	state = settingsSavedState(t, a)
	if len(state.Settings.Providers[1].DisabledModels) != 0 || len(state.Settings.Providers[0].DisabledModels) != 0 {
		t.Fatal("select all was not persisted")
	}
}

func TestProviderDomainChangeRequiresOldCredentialRemoval(t *testing.T) {
	a := providerTestApp(t)
	a.providerSettingsID = "work"
	a.providerState("work").client = nil
	secrets := a.providerSecrets.(*memoryCredentials)
	secrets.entries[providerCredentialKey("work")] = []byte("old-work-token")
	secrets.deleteErrors = map[string]error{providerCredentialKey("work"): errors.New("locked credential store")}
	tt := ui.NewTester(func(c *ui.Context) { ui.Column(c).Fill().Children(func() { a.providerSettings(c) }) }, 600, 1000)
	settingsClick(t, tt, "GitHub domain input")
	tt.Command("selectAll")
	tt.Type("other.ghe.com")
	if a.provider("work").Host != "example.ghe.com" {
		t.Fatal("provider changed domains while old credentials remained")
	}
	if !tt.HasText("Could not remove the previous domain's saved sign-in: locked credential store") {
		t.Fatal("credential removal failure was hidden")
	}
	delete(secrets.deleteErrors, providerCredentialKey("work"))
	settingsClick(t, tt, "GitHub domain input")
	tt.Command("selectAll")
	tt.Type("OTHER.GHE.COM")
	if a.provider("work").Host != "other.ghe.com" || len(secrets.entries[providerCredentialKey("work")]) != 0 || a.provider("work").Login != "" {
		t.Fatal("domain change retained old account identity")
	}
}

func TestProviderLegacyCredentialDeleteFailureIsReported(t *testing.T) {
	a := providerTestApp(t)
	secrets := a.providerSecrets.(*memoryCredentials)
	secrets.deleteErrors = map[string]error{
		providerCredentialKey(store.DefaultProviderID): mygo.ErrSecretNotFound,
		"github-copilot-token":                         errors.New("legacy entry locked"),
	}
	a.signOutProvider(store.DefaultProviderID)
	if !strings.Contains(a.providerState(store.DefaultProviderID).credentialNotice, "legacy entry locked") {
		t.Fatal("legacy credential deletion failure was discarded")
	}
}

func TestProviderAccountLookupFailureIsVisibleAndCannotLabelANewAccount(t *testing.T) {
	a := providerTestApp(t)
	p, r := a.provider("work"), a.providerState("work")
	r.status = "Connected · 2 models available"
	lookupError := errors.New("account lookup denied")
	a.updateProviderAccount("work", provider.AccountInfo{}, lookupError, false)
	if p.Login != "work-user" || !strings.Contains(r.status, "Could not load GitHub username: account lookup denied") || !strings.Contains(r.status, "2 models available") {
		t.Fatal("refresh discarded account lookup error or previously confirmed username")
	}
	r.status = "Connected."
	a.updateProviderAccount("work", provider.AccountInfo{}, lookupError, true)
	if p.Login != "" || !strings.Contains(r.status, "Could not load GitHub username") {
		t.Fatal("new sign-in retained the previous account's username")
	}
	r.status = "Connected · 2 models available"
	a.updateProviderAccount("work", provider.AccountInfo{Login: "new-user", Host: "example.ghe.com"}, nil, false)
	if p.Login != "new-user" || strings.Contains(r.status, "Could not load") {
		t.Fatal("successful account lookup did not replace account identity")
	}
}

func TestProviderSelectionAndVisibilityKeepIdenticalModelIDsSeparate(t *testing.T) {
	a := providerTestApp(t)
	a.state.ActiveID = "chat"
	a.state.Conversations = []store.Conversation{{ID: "chat", ProviderID: store.DefaultProviderID, Model: "same", ReasoningEffort: "high", ThinkingBudgetTokens: 1000}}
	personal, work := a.clientForProvider(store.DefaultProviderID), a.clientForProvider("work")
	a.chooseModel("work", "same")
	if a.selectedProviderID() != "work" || a.selectedClient() != work || a.selectedClient() == personal || a.current().ReasoningEffort != "" || a.current().ThinkingBudgetTokens != 0 {
		t.Fatal("model selection crossed provider identity or retained stale reasoning")
	}
	loaded, err := a.storage.Load()
	if err != nil || loaded.Conversations[0].ProviderID != "work" || loaded.Settings.ActiveProviderID != "work" {
		t.Fatalf("provider binding not persisted: %#v %v", loaded, err)
	}
	p := a.provider("work")
	a.setModelEnabled("work", "same", false)
	if got := a.providerModels("work"); len(got) != 1 || got[0].ID != "work-only" {
		t.Fatalf("disabled shared model remains visible: %#v", got)
	}
	if len(a.providerModels(store.DefaultProviderID)) != 2 {
		t.Fatal("work model visibility leaked into personal provider")
	}
	a.setModelEnabled("work", "same", true)
	a.setModelEnabled("work", "work-only", false)
	if got := a.providerModels("work"); len(got) != 1 || got[0].ID != "same" {
		t.Fatalf("reenabled shared model did not become visible: %#v", got)
	}
	a.toggleModelPin("work", "same")
	if !reflect.DeepEqual(p.PinnedModels, []string{"same"}) || len(a.provider(store.DefaultProviderID).PinnedModels) != 0 {
		t.Fatal("pin crossed provider identity")
	}
}

func TestProviderCancellationAndSignOutAreIsolated(t *testing.T) {
	a := providerTestApp(t)
	personal := a.providerState(store.DefaultProviderID)
	work := a.providerState("work")
	personalCtx, personalCancel := context.WithCancel(context.Background())
	defer personalCancel()
	workCtx, workCancel := context.WithCancel(context.Background())
	work.authBusy, work.authEpoch, work.authCancel = true, 1, workCancel
	personal.authBusy, personal.authEpoch, personal.authCancel = true, 1, personalCancel
	a.cancelProviderLogin("work")
	if workCtx.Err() == nil || personalCtx.Err() != nil || !a.currentProviderLogin(store.DefaultProviderID, 1, personalCtx) || a.currentProviderLogin("work", 1, context.Background()) {
		t.Fatal("cancellation affected wrong provider")
	}
	secrets := a.providerSecrets.(*memoryCredentials)
	secrets.entries[providerCredentialKey("work")] = []byte("work")
	secrets.entries[providerCredentialKey(store.DefaultProviderID)] = []byte("personal")
	a.signOutProvider("work")
	if work.client != nil || personal.client == nil || len(secrets.entries[providerCredentialKey(store.DefaultProviderID)]) == 0 {
		t.Fatal("sign-out affected another account")
	}
}

func TestGroupedModelPickerPinAndAccountSelection(t *testing.T) {
	a := providerTestApp(t)
	a.state.Settings.Model = "same"
	tt := ui.NewTester(func(c *ui.Context) { ui.Row(c).Height(50).Children(func() { a.modelPicker(c) }) }, 720, 600)
	settingsClick(t, tt, "Choose model")
	if !tt.HasText("Personal") || !tt.HasText("Work") {
		t.Fatalf("missing provider groups: %v", tt.Texts())
	}
	settingsClick(t, tt, "Pin Shared model from Work")
	if !reflect.DeepEqual(a.provider("work").PinnedModels, []string{"same"}) {
		t.Fatal("pin was not saved")
	}
	for _, removed := range []string{"Search models", "All", "Pinned"} {
		if _, ok := tt.Find(removed); ok {
			t.Fatalf("removed model picker control remains: %s", removed)
		}
	}
	settingsClick(t, tt, "Choose Shared model from Work")
	if a.selectedProviderID() != "work" || a.selectedModel() != "same" {
		t.Fatal("picker selected the same ID from the wrong provider")
	}
}

func TestProviderFormValidatesHostAndRenamesOnlySelectedInstance(t *testing.T) {
	a := providerTestApp(t)
	a.providerSettingsID = "work"
	a.providerState("work").client = nil
	a.settingsPage = "Providers"
	tt := ui.NewTester(a.settingsView, 900, 900)
	settingsClick(t, tt, "GitHub domain input")
	tt.Command("selectAll")
	tt.Type("https://evil.example/")
	if a.provider("work").Host != "example.ghe.com" {
		t.Fatal("invalid domain changed provider host")
	}
	settingsClick(t, tt, "GitHub domain input")
	tt.Command("selectAll")
	tt.Type("updated.ghe.com")
	settingsClick(t, tt, "Provider name input")
	tt.Command("selectAll")
	tt.Type("Renamed work")
	if tt.HasText("Save provider") {
		t.Fatal("provider form still requires manual saving")
	}
	if a.provider("work").Name != "Renamed work" || a.provider("work").Host != "updated.ghe.com" || a.provider(store.DefaultProviderID).Name != "Personal" {
		t.Fatalf("provider edit did not preserve isolation: work=%#v personal=%#v text=%v", a.provider("work"), a.provider(store.DefaultProviderID), tt.Texts())
	}
	loaded := settingsSavedState(t, a)
	if loaded.Settings.Providers[1].Name != "Renamed work" || loaded.Settings.Providers[1].Host != "updated.ghe.com" {
		t.Fatal("provider changes were not saved automatically")
	}
}

func TestDisabledProviderRetainsAccountAndCannotBeUsed(t *testing.T) {
	a := providerTestApp(t)
	a.state.ActiveID = "chat"
	a.state.Conversations = []store.Conversation{{ID: "chat", ProviderID: "work", Model: "same"}}
	a.state.Settings.ActiveProviderID, a.state.Settings.Model = "work", "same"
	r := a.providerState("work")
	client := r.client
	ctx, cancel := context.WithCancel(context.Background())
	r.authBusy, r.authCancel = true, cancel
	secrets := a.providerSecrets.(*memoryCredentials)
	secrets.entries[providerCredentialKey("work")] = []byte("kept-work-token")
	a.setProviderEnabled("work", false)
	if !a.provider("work").Disabled || r.client != client || len(r.models) != 2 || len(secrets.entries[providerCredentialKey("work")]) == 0 || a.provider("work").Login != "work-user" {
		t.Fatal("disabling deleted the provider's account or configuration")
	}
	if ctx.Err() == nil || a.clientForProvider("work") != nil || a.clientStillConnected(client) || len(a.providerModels("work")) != 0 {
		t.Fatal("disabled provider remains usable or active")
	}
	if a.current().ProviderID != "work" || a.state.Settings.ActiveProviderID != store.DefaultProviderID {
		t.Fatal("disabling rebound an existing conversation or left disabled new-chat defaults")
	}
	if _, ok := a.modelForProvider("work", "same"); ok {
		t.Fatal("disabled model remains available for chat features")
	}
	a.chooseModel("work", "same")
	if a.state.Settings.ActiveProviderID == "work" {
		t.Fatal("model selection reactivated a disabled provider")
	}
	if loaded := settingsSavedState(t, a); !loaded.Settings.Providers[1].Disabled {
		t.Fatal("disabled state was not persisted")
	}
	a.provider("work").ModelsRefreshedAt = time.Now()
	a.setProviderEnabled("work", true)
	if a.clientForProvider("work") != client || a.provider("work").Disabled {
		t.Fatal("reenabling did not restore the existing signed-in provider")
	}
}

func TestProviderDailyRefreshEligibility(t *testing.T) {
	now := time.Now()
	p := store.Provider{ID: "work", Host: "example.ghe.com", ModelsRefreshedAt: now.Add(-25 * time.Hour)}
	r := &providerRuntime{client: github.NewClient("fake"), modelsLoaded: true, models: []provider.Model{{ID: "same"}}}
	if !providerCatalogRefreshDue(p, r, now) {
		t.Fatal("catalog was not refreshed after one day")
	}
	p.Disabled = true
	if providerCatalogRefreshDue(p, r, now) {
		t.Fatal("disabled provider requested daily refresh")
	}
	p.Disabled = false
	p.ModelsRefreshedAt = now.Add(-23 * time.Hour)
	if providerCatalogRefreshDue(p, r, now) {
		t.Fatal("fresh catalog requested unnecessary refresh")
	}
	r.models, r.modelsLoaded = nil, false
	if !providerCatalogRefreshDue(p, r, now) {
		t.Fatal("new process with no in-memory catalog skipped initial load")
	}
	r.modelsAttemptedAt = now.Add(-time.Minute)
	if providerCatalogRefreshDue(p, r, now) {
		t.Fatal("failed startup fetch was not throttled")
	}
	r.modelsAttemptedAt = now.Add(-16 * time.Minute)
	if !providerCatalogRefreshDue(p, r, now) {
		t.Fatal("failed catalog was not eligible for retry")
	}
	r.modelsBusy = true
	if providerCatalogRefreshDue(p, r, now) {
		t.Fatal("concurrent catalog refresh was allowed")
	}
}

func TestProviderListAddRemoveKeepsHistoryAndLastDeletionPersists(t *testing.T) {
	a := providerTestApp(t)
	a.settingsPage = "Providers"
	a.state.ActiveID = "work-chat"
	a.state.Conversations = []store.Conversation{{ID: "work-chat", ProviderID: "work", Model: "same", Messages: []store.Message{{ID: "old", Role: "user", Content: "keep this history"}}}}
	secrets := a.providerSecrets.(*memoryCredentials)
	secrets.entries[providerCredentialKey("work")] = []byte("work-token")
	secrets.entries[providerCredentialKey(store.DefaultProviderID)] = []byte("personal-token")
	secrets.entries["github-copilot-token"] = []byte("legacy-token")
	loginCtx, loginCancel := context.WithCancel(context.Background())
	modelCtx, modelCancel := context.WithCancel(context.Background())
	titleCtx, titleCancel := context.WithCancel(context.Background())
	r := a.providerState("work")
	r.authBusy, r.authCancel, r.modelsBusy, r.modelsCancel = true, loginCancel, true, modelCancel
	a.titleJobs = map[string]*titleJob{"work-chat": {cancel: titleCancel}}
	tt := ui.NewTester(a.settingsView, 900, 900)
	settingsClick(t, tt, "Provider Work")
	settingsClick(t, tt, "Remove provider")
	if a.provider("work") != nil || a.providers["work"] != nil || len(secrets.entries[providerCredentialKey("work")]) != 0 {
		t.Fatal("selected provider configuration or credential survived removal")
	}
	if loginCtx.Err() == nil || modelCtx.Err() == nil || titleCtx.Err() == nil {
		t.Fatal("provider removal did not cancel pending work")
	}
	if a.current().ProviderID != "work" || len(a.current().Messages) != 1 || a.current().Messages[0].Content != "keep this history" {
		t.Fatal("provider removal rebound or deleted conversation history")
	}
	if a.clientForProvider(a.current().ProviderID) != nil {
		t.Fatal("deleted provider remains available to an old conversation")
	}
	settingsClick(t, tt, "Provider Personal")
	settingsClick(t, tt, "Remove provider")
	if len(a.state.Settings.Providers) != 0 || a.providerSettingsID != "" || len(secrets.entries) != 0 {
		t.Fatal("last provider or its legacy credential was retained")
	}
	settingsClick(t, tt, "Remove provider") // Disabled minus is a no-op with no selection.
	loaded := settingsSavedState(t, a)
	if len(loaded.Settings.Providers) != 0 || loaded.Settings.ActiveProviderID != "" || len(loaded.Conversations) != 1 || loaded.Conversations[0].ProviderID != "work" {
		t.Fatal("last provider was recreated on reload or history was lost")
	}
	settingsClick(t, tt, "Add provider")
	settingsClick(t, tt, "Add GitHub Copilot")
	if len(a.state.Settings.Providers) != 1 || a.providerSettingsID == "" || a.state.Settings.ActiveProviderID != a.providerSettingsID {
		t.Fatal("plus did not create and select a new provider after deleting all providers")
	}
}

func TestProviderListRemovalFailureKeepsConfigurationAndCanRetry(t *testing.T) {
	a := providerTestApp(t)
	a.settingsPage, a.providerSettingsID = "Providers", "work"
	secrets := a.providerSecrets.(*memoryCredentials)
	secrets.entries[providerCredentialKey("work")] = []byte("work-token")
	secrets.deleteErrors = map[string]error{providerCredentialKey("work"): errors.New("credential store locked")}
	a.save()
	client := a.clientForProvider("work")
	tt := ui.NewTester(a.settingsView, 900, 900)
	settingsClick(t, tt, "Remove provider")
	if a.provider("work") == nil || a.clientForProvider("work") != client || len(secrets.entries[providerCredentialKey("work")]) == 0 || len(settingsSavedState(t, a).Settings.Providers) != 2 {
		t.Fatal("failed credential removal discarded provider state")
	}
	if !tt.HasText("Could not delete provider") || !tt.HasText("Try again.") {
		t.Fatal("provider deletion failure was not shown with a retry action")
	}
	delete(secrets.deleteErrors, providerCredentialKey("work"))
	settingsClick(t, tt, "Remove provider")
	if a.provider("work") != nil {
		t.Fatal("provider deletion could not be retried")
	}
}
