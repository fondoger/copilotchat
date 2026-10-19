package apikeyhelper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo"
	"github.com/fondoger/copilotchat/internal/credentials"
	"github.com/fondoger/copilotchat/internal/provider/microsoft/entra"
	"github.com/fondoger/copilotchat/internal/provider/microsoft/wam"
	"github.com/fondoger/copilotchat/internal/store"
)

type memoryStore map[string][]byte

func (m memoryStore) Get(k string) ([]byte, error) {
	if v, ok := m[k]; ok {
		return v, nil
	}
	return nil, mygo.ErrSecretNotFound
}
func (m memoryStore) Set(k string, v []byte) error { m[k] = append([]byte(nil), v...); return nil }
func (m memoryStore) Delete(k string) error        { delete(m, k); return nil }

func saveProviders(t *testing.T, dir string, providers ...store.Provider) {
	t.Helper()
	value := map[string]any{"settings": map[string]any{"providers": providers}, "conversations": []any{map[string]any{"content": "private fixture never needed by helper"}}}
	data, _ := json.Marshal(value)
	if err := os.WriteFile(filepath.Join(dir, "state.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubHelperPinsProviderAndNeverFallsBack(t *testing.T) {
	dir := t.TempDir()
	a := store.DefaultProvider()
	a.Login = "fixture"
	b := a
	b.ID = "second"
	saveProviders(t, dir, a, b)
	vault := memoryStore{credentials.GitHubKey(a.ID): []byte("token-a"), credentials.GitHubKey(b.ID): []byte("token-b")}
	before, _ := os.ReadFile(filepath.Join(dir, "state.json"))
	for _, p := range []store.Provider{a, b} {
		got, err := Token(context.Background(), dir, p.ID, vault)
		if err != nil || got != string(vault[credentials.GitHubKey(p.ID)]) {
			t.Fatal("wrong account credential", err)
		}
	}
	delete(vault, credentials.GitHubKey(b.ID))
	if got, err := Token(context.Background(), dir, b.ID, vault); err == nil || got != "" {
		t.Fatal("missing account fell back")
	}
	b.Disabled = true
	saveProviders(t, dir, a, b)
	if got, err := Token(context.Background(), dir, b.ID, vault); err == nil || got != "" {
		t.Fatal("disabled account allowed")
	}
	saveProviders(t, dir, a)
	if _, err := Token(context.Background(), dir, b.ID, vault); err == nil {
		t.Fatal("deleted account allowed")
	}
	saveProviders(t, dir, a, b)
	b.Disabled = false
	saveProviders(t, dir, a, b)
	after, _ := os.ReadFile(filepath.Join(dir, "state.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("helper changed state")
	}
}

type fakeSource struct {
	fetch   func() (string, error)
	revoked bool
}

func (f *fakeSource) Token(context.Context) (string, error) { return f.fetch() }
func (f *fakeSource) Revoke()                               { f.revoked = true }

func TestMicrosoftHelperUsesSavedReferenceAndRechecksRevocation(t *testing.T) {
	for _, action := range []string{"valid", "disabled", "deleted", "signed-out", "sign-out-vault-failure", "changed-account"} {
		t.Run(action, func(t *testing.T) {
			dir := t.TempDir()
			p := store.Provider{ID: "work-id", Kind: store.ProviderKindM365, Host: store.M365ProviderHost, Login: "fixture"}
			saveProviders(t, dir, p)
			entry := credentials.MicrosoftSignIn{Version: 1, Mode: "windows-wam", WAM: &wam.Reference{ID: "selected-wam-account", Account: entra.Account{ID: "12345678-1234-1234-1234-123456789abc", TenantID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}}}
			vault := memoryStore{}
			if err := credentials.SaveMicrosoft(vault, p.ID, entry); err != nil {
				t.Fatal(err)
			}
			source := &fakeSource{fetch: func() (string, error) {
				switch action {
				case "disabled":
					p.Disabled = true
					saveProviders(t, dir, p)
				case "deleted":
					saveProviders(t, dir)
				case "signed-out":
					_ = credentials.DeleteMicrosoft(vault, p.ID)
				case "sign-out-vault-failure":
					p.Login = ""
					saveProviders(t, dir, p)
				case "changed-account":
					other := entry
					ref := *entry.WAM
					ref.ID = "different-account"
					other.WAM = &ref
					_ = credentials.SaveMicrosoft(vault, p.ID, other)
				}
				return "fresh-token", nil
			}}
			got, err := token(context.Background(), dir, p.ID, vault, func(root, id string, saved credentials.MicrosoftSignIn) (credentials.TokenSource, entra.Account, error) {
				if root != dir || id != p.ID || saved.WAM == nil || saved.WAM.ID != entry.WAM.ID {
					t.Fatal("identity changed before renewal")
				}
				return source, saved.WAM.Account, nil
			})
			if action == "valid" {
				if err != nil || got != "fresh-token" {
					t.Fatal(err)
				}
			} else if err == nil || got != "" {
				t.Fatal("credential returned after revocation")
			}
			if !source.revoked {
				t.Fatal("source not released")
			}
		})
	}
}

func TestCommandOutputAndFailureContract(t *testing.T) {
	for _, check := range []bool{false, true} {
		var out, diagnostic bytes.Buffer
		args := []string{"--provider", "p", "--data-dir", t.TempDir()}
		if check {
			args = append(args, "--check")
		}
		code := run(context.Background(), args, &out, &diagnostic, func(_ context.Context, dir, id string) (string, error) {
			if id != "p" || dir == "" {
				t.Fatal("missing binding")
			}
			return "token-fixture", nil
		})
		if code != 0 {
			t.Fatal(diagnostic.String())
		}
		if check {
			if out.Len() != 0 || bytes.Contains(diagnostic.Bytes(), []byte("token-fixture")) {
				t.Fatal("check leaked token")
			}
		} else if out.String() != "token-fixture\n" || diagnostic.Len() != 0 {
			t.Fatal("stdout contract")
		}
	}
	for _, value := range []string{"", "token\ninjected"} {
		var out, diagnostic bytes.Buffer
		if run(context.Background(), []string{"--provider", "p"}, &out, &diagnostic, func(context.Context, string, string) (string, error) { return value, nil }) == 0 || out.Len() != 0 {
			t.Fatal("invalid token was emitted")
		}
	}
	var out, diagnostic bytes.Buffer
	if run(context.Background(), []string{"--provider", "p"}, &out, &diagnostic, func(context.Context, string, string) (string, error) {
		return "secret", errors.New("reconnect required")
	}) != 1 || out.Len() != 0 {
		t.Fatal("failure emitted credential")
	}
	if run(context.Background(), nil, &out, &diagnostic, func(context.Context, string, string) (string, error) { t.Fatal("unbound fetch"); return "", nil }) != 2 {
		t.Fatal("missing ID accepted")
	}
}
