package desktop

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider/microsoft/cli"
	"github.com/fondoger/copilotchat/internal/provider/microsoft/entra"
	"github.com/fondoger/copilotchat/internal/provider/microsoft/wam"
	"github.com/fondoger/copilotchat/internal/store"
)

type boundedCredentials struct {
	memoryCredentials
	failKey string
}

func TestM365NativeWAMReferenceRestoresWithoutCLIAndStaysOutOfState(t *testing.T) {
	a := providerTestApp(t)
	id, err := a.addProviderKind(store.ProviderKindM365, "Native work", store.M365ProviderHost)
	if err != nil {
		t.Fatal(err)
	}
	entry := m365SavedSignIn{Version: 1, Mode: "windows-wam", WAM: &wam.Reference{
		ID:      "opaque-windows-account-fixture",
		Account: entra.Account{ID: "12345678-1234-1234-1234-123456789abc", TenantID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", Username: "work@example.test"},
	}}
	if err := saveM365SignIn(a.credentials(), id, entry); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "") // Restoring a native account must not launch/locate the CLI.
	a.loadM365Provider(*a.provider(id), a.providerState(id))
	if a.clientForProvider(id) == nil || a.providerState(id).m365Auth == nil {
		t.Fatal("native WAM account did not restore independently")
	}
	loaded, err := loadM365SignIn(a.credentials(), id)
	if err != nil || loaded.Mode != "windows-wam" || loaded.WAM == nil || loaded.WAM.ID != entry.WAM.ID || loaded.CLI != nil {
		t.Fatal("native account did not round trip through the credential vault", err)
	}
	a.saveSettings()
	data, _ := json.Marshal(settingsSavedState(t, a))
	if strings.Contains(string(data), entry.WAM.ID) || strings.Contains(string(data), entry.WAM.Account.ID) {
		t.Fatal("native WAM account reference leaked to state.json")
	}
	a.signOutProvider(id)
	if a.clientForProvider(id) != nil || a.providerState(id).m365Auth != nil {
		t.Fatal("native WAM source survived sign-out")
	}
	if _, err := loadM365SignIn(a.credentials(), id); err == nil {
		t.Fatal("native sign-out retained its saved account reference")
	}
}

func TestM365NativeWAMRejectsMissingOrInvalidReference(t *testing.T) {
	a := providerTestApp(t)
	for _, ref := range []*wam.Reference{nil, {}, {ID: "opaque-id"}} {
		client, source, err := a.m365Client("native", m365SavedSignIn{Version: 1, Mode: "windows-wam", WAM: ref})
		if err == nil || client != nil || source != nil {
			t.Fatal("invalid native account reference was accepted")
		}
	}
}

func (s *boundedCredentials) Set(key string, value []byte) error {
	if key == s.failKey {
		return errors.New("credential commit failed")
	}
	if len(value) > 2560 {
		return errors.New("Windows credential too large")
	}
	return s.memoryCredentials.Set(key, value)
}

func TestM365CredentialChunksCommitAtomicallyAndStayOutOfState(t *testing.T) {
	secrets := &boundedCredentials{memoryCredentials: memoryCredentials{entries: map[string][]byte{}}}
	entry := m365SavedSignIn{Version: 1, Mode: "copilot-cli", CLI: &cli.Reference{Account: entra.Account{ID: "12345678-1234-1234-1234-123456789abc", TenantID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", Username: "work@example.test", Name: strings.Repeat("account-fixture", 900)}}}
	if err := saveM365SignIn(secrets, "work", entry); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadM365SignIn(secrets, "work")
	if err != nil || loaded.CLI.Account.Name != entry.CLI.Account.Name {
		t.Fatal("large credential did not round trip", err)
	}
	oldCount := len(secrets.entries)
	secrets.failKey = m365CredentialKey("work")
	newEntry := entry
	newReference := *entry.CLI
	newReference.Account.Name = "updated-fixture"
	newEntry.CLI = &newReference
	if saveM365SignIn(secrets, "work", newEntry) == nil {
		t.Fatal("commit failure not returned")
	}
	loaded, err = loadM365SignIn(secrets, "work")
	if err != nil || loaded.CLI.Account.Name != entry.CLI.Account.Name || len(secrets.entries) != oldCount {
		t.Fatal("failed commit lost old credential or left staged chunks")
	}
	secrets.failKey = ""
	if err := saveM365SignIn(secrets, "work", newEntry); err != nil {
		t.Fatal(err)
	}
	loaded, err = loadM365SignIn(secrets, "work")
	if err != nil || loaded.CLI.Account.Name != "updated-fixture" {
		t.Fatal("rotation did not commit")
	}
	a := providerTestApp(t)
	a.providerSecrets = secrets
	id, err := a.addProviderKind(store.ProviderKindM365, "Company", store.M365ProviderHost)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveM365SignIn(secrets, id, newEntry); err != nil {
		t.Fatal(err)
	}
	a.loadProviders()
	if a.clientForProvider(id) == nil || a.clientForProvider(id).Host() != store.M365ProviderHost {
		t.Fatal("M365 sign-in was not restored")
	}
	stateJSON, _ := json.Marshal(settingsSavedState(t, a))
	if strings.Contains(string(stateJSON), "account-fixture") || strings.Contains(string(stateJSON), "updated-fixture") {
		t.Fatal("credential leaked to state")
	}
	a.signOutProvider(id)
	if a.clientForProvider(id) != nil {
		t.Fatal("signed-out M365 client retained")
	}
	for key := range secrets.entries {
		if strings.HasPrefix(key, m365CredentialKey(id)) {
			t.Fatal("M365 sign-out left credentials")
		}
	}
	if _, err := loadM365SignIn(secrets, "work"); err != nil {
		t.Fatal("sign-out removed a different account")
	}
}

func TestM365CLIReferenceAndGitHubCredentialStaySeparate(t *testing.T) {
	a := providerTestApp(t)
	id, err := a.addProviderKind(store.ProviderKindM365, "Company", store.M365ProviderHost)
	if err != nil {
		t.Fatal(err)
	}
	secrets := a.providerSecrets.(*memoryCredentials)
	secrets.entries[providerCredentialKey(store.DefaultProviderID)] = []byte("github-fixture")
	entry := m365SavedSignIn{Version: 1, Mode: "copilot-cli", CLI: &cli.Reference{Account: entra.Account{ID: "12345678-1234-1234-1234-123456789abc", TenantID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", Username: "work@example.test"}}}
	if err := saveM365SignIn(secrets, id, entry); err != nil {
		t.Fatal(err)
	}
	a.loadProviders()
	if a.clientForProvider(id) == nil || a.clientForProvider(store.DefaultProviderID) == nil {
		t.Fatal("mixed provider sign-ins did not restore")
	}
	a.setProviderEnabled(id, false)
	if a.clientForProvider(id) != nil || a.providerState(id).client == nil {
		t.Fatal("disable did not retain M365 sign-in")
	}
	if err := a.removeProvider(id); err != nil {
		t.Fatal(err)
	}
	if string(secrets.entries[providerCredentialKey(store.DefaultProviderID)]) != "github-fixture" {
		t.Fatal("removing M365 changed GitHub credential")
	}
}

func TestM365CorruptManifestCanReconnectAndRemove(t *testing.T) {
	a := providerTestApp(t)
	id, err := a.addProviderKind(store.ProviderKindM365, "Company", store.M365ProviderHost)
	if err != nil {
		t.Fatal(err)
	}
	secrets := a.providerSecrets.(*memoryCredentials)
	secrets.entries[m365CredentialKey(id)] = []byte(`{"generation":"../../unsafe","chunks":100000}`)
	entry := m365SavedSignIn{Version: 1, Mode: "copilot-cli", CLI: &cli.Reference{Account: entra.Account{ID: "12345678-1234-1234-1234-123456789abc", TenantID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", Username: "work@example.test"}}}
	if err := saveM365SignIn(secrets, id, entry); err != nil {
		t.Fatal("corrupt manifest blocked reconnect", err)
	}
	if _, err := loadM365SignIn(secrets, id); err != nil {
		t.Fatal(err)
	}
	if err := a.clearM365Credentials(id); err != nil {
		t.Fatal(err)
	}
	secrets.entries[m365CredentialKey(id)] = []byte("corrupt fixture")
	if err := a.removeProvider(id); err != nil {
		t.Fatal("corrupt manifest blocked provider removal", err)
	}
}

func TestM365RotationRetainsCleanupIndexWhenOldSecretDeletionFails(t *testing.T) {
	a := providerTestApp(t)
	id, err := a.addProviderKind(store.ProviderKindM365, "Company", store.M365ProviderHost)
	if err != nil {
		t.Fatal(err)
	}
	secrets := a.providerSecrets.(*memoryCredentials)
	entry := m365SavedSignIn{Version: 1, Mode: "copilot-cli", CLI: &cli.Reference{Account: entra.Account{Name: "old-fixture"}}}
	if err := saveM365SignIn(secrets, id, entry); err != nil {
		t.Fatal(err)
	}
	old, err := readSecretManifest(secrets, m365CredentialKey(id))
	if err != nil {
		t.Fatal(err)
	}
	oldKey := m365ChunkKey(m365CredentialKey(id), old, 0)
	secrets.deleteErrors = map[string]error{oldKey: errors.New("locked")}
	entry.CLI.Account.Name = "updated-fixture"
	if saveM365SignIn(secrets, id, entry) == nil {
		t.Fatal("cleanup error hidden")
	}
	manifest, err := readSecretManifest(secrets, m365CredentialKey(id))
	if err != nil || len(manifest.Retired) != 1 {
		t.Fatal("lost old secret cleanup index")
	}
	loaded, err := loadM365SignIn(secrets, id)
	if err != nil || loaded.CLI.Account.Name != "updated-fixture" {
		t.Fatal("new credential lost")
	}
	delete(secrets.deleteErrors, oldKey)
	if err := a.clearM365Credentials(id); err != nil {
		t.Fatal(err)
	}
	for key := range secrets.entries {
		if strings.HasPrefix(key, m365CredentialKey(id)) {
			t.Fatal("sign-out left older credentials")
		}
	}
}

func TestM365CleanupManifestRejectsActiveOrDuplicateGenerations(t *testing.T) {
	secrets := &memoryCredentials{entries: map[string][]byte{}}
	manifest := secretManifest{Generation: strings.Repeat("a", 24), Chunks: 1, SHA256: strings.Repeat("0", 64)}
	for _, retired := range [][]secretGeneration{
		{{Generation: manifest.Generation, Chunks: 1}},
		{{Generation: strings.Repeat("b", 24), Chunks: 1}, {Generation: strings.Repeat("b", 24), Chunks: 1}},
	} {
		manifest.Retired = retired
		data, _ := json.Marshal(manifest)
		secrets.entries["fixture"] = data
		if _, err := readSecretManifest(secrets, "fixture"); !errors.Is(err, errInvalidMicrosoftManifest) {
			t.Fatal("unsafe cleanup index accepted")
		}
	}
}

func TestM365FailedRemovalDoesNotKeepRevokedConnection(t *testing.T) {
	a := providerTestApp(t)
	id, err := a.addProviderKind(store.ProviderKindM365, "Company", store.M365ProviderHost)
	if err != nil {
		t.Fatal(err)
	}
	secrets := a.providerSecrets.(*memoryCredentials)
	entry := m365SavedSignIn{Version: 1, Mode: "copilot-cli", CLI: &cli.Reference{Account: entra.Account{ID: "12345678-1234-1234-1234-123456789abc", TenantID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", Username: "work@example.test"}}}
	if err := saveM365SignIn(secrets, id, entry); err != nil {
		t.Fatal(err)
	}
	a.loadM365Provider(*a.provider(id), a.providerState(id))
	a.provider(id).Login = entry.CLI.Account.Username
	if a.clientForProvider(id) == nil {
		t.Fatal("fixture did not connect")
	}
	manifest, err := readSecretManifest(secrets, m365CredentialKey(id))
	if err != nil {
		t.Fatal(err)
	}
	key := m365ChunkKey(m365CredentialKey(id), manifest, 0)
	secrets.deleteErrors = map[string]error{key: errors.New("vault is locked")}
	if a.removeProvider(id) == nil {
		t.Fatal("credential deletion failure was hidden")
	}
	r := a.providerState(id)
	if a.provider(id) == nil || r.client != nil || r.m365Auth != nil || a.provider(id).Login != "" || r.status != "Signed out for this session." {
		t.Fatal("failed deletion retained a revoked connection or removed the provider")
	}
	if a.clientForProvider("work") == nil {
		t.Fatal("unrelated provider was disconnected")
	}
	delete(secrets.deleteErrors, key)
	if err := a.removeProvider(id); err != nil || a.provider(id) != nil {
		t.Fatal("could not retry provider deletion", err)
	}
}
