package wam

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fondoger/copilotchat/internal/provider/microsoft/entra"
)

func fixtureReference() Reference {
	return Reference{ID: "opaque-wam-account", Account: entra.Account{
		ID: "12345678-1234-1234-1234-123456789abc", TenantID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", Username: "person@example.test",
	}}
}

func fixtureToken(overrides map[string]any) string {
	account := fixtureReference().Account
	claims := map[string]any{"aud": entra.Resource, "iss": "https://sts.windows.net/" + account.TenantID + "/", "tid": account.TenantID, "oid": account.ID, "upn": account.Username, "exp": time.Now().Add(time.Hour).Unix()}
	for key, value := range overrides {
		claims[key] = value
	}
	payload, _ := json.Marshal(claims)
	return "fixture." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func testSource(t *testing.T, fetch acquireFunc) *Source {
	t.Helper()
	s, err := NewSource(fixtureReference())
	if err != nil {
		t.Fatal(err)
	}
	s.fetch = fetch
	t.Cleanup(s.Revoke)
	return s
}

func TestSignInReturnsOnlyValidatedAccountReference(t *testing.T) {
	token := fixtureToken(nil)
	ref, err := signIn(context.Background(), 123, func(_ context.Context, hwnd uintptr, id string) (string, string, error) {
		if hwnd != 123 || id != "" {
			t.Fatal("interactive sign-in did not request the account picker")
		}
		return token, fixtureReference().ID, nil
	})
	if err != nil || ref != fixtureReference() {
		t.Fatal("sign-in did not bind the validated account", err)
	}
	encoded, _ := json.Marshal(ref)
	if strings.Contains(string(encoded), token) || strings.Contains(string(encoded), "token") {
		t.Fatal("saved account reference contains a token")
	}
	for _, tc := range []struct{ id, token string }{{"", token}, {ref.ID, fixtureToken(map[string]any{"aud": "wrong-resource"})}} {
		if _, err := signIn(context.Background(), 123, func(context.Context, uintptr, string) (string, string, error) { return tc.token, tc.id, nil }); err == nil {
			t.Fatal("invalid sign-in result accepted")
		}
	}
}

func TestTokenRejectsWrongAccountAudienceAndExpiredCredentials(t *testing.T) {
	ref := fixtureReference()
	for _, tc := range []struct {
		name string
		id   string
		edit map[string]any
	}{
		{"wam-account", "other-account", nil},
		{"object", ref.ID, map[string]any{"oid": "87654321-1234-1234-1234-123456789abc"}},
		{"tenant", ref.ID, map[string]any{"tid": "bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee", "iss": "https://sts.windows.net/bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee/"}},
		{"audience", ref.ID, map[string]any{"aud": "wrong-resource"}},
		{"expired", ref.ID, map[string]any{"exp": time.Now().Add(-time.Minute).Unix()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testSource(t, func(_ context.Context, hwnd uintptr, id string) (string, string, error) {
				if hwnd != 0 || id != ref.ID {
					t.Fatal("renewal did not select the saved account silently")
				}
				return fixtureToken(tc.edit), tc.id, nil
			})
			if token, err := s.Token(context.Background()); token != "" || err == nil || s.token != "" {
				t.Fatal("invalid credential was returned or cached")
			}
		})
	}
}

func TestTokenCacheCoalescesRenewalAndRefreshesBeforeExpiry(t *testing.T) {
	var calls atomic.Int32
	s := testSource(t, func(context.Context, uintptr, string) (string, string, error) {
		calls.Add(1)
		return fixtureToken(nil), fixtureReference().ID, nil
	})
	var callers sync.WaitGroup
	for range 8 {
		callers.Go(func() {
			if token, err := s.Token(context.Background()); token == "" || err != nil {
				t.Error("silent token acquisition failed", err)
			}
		})
	}
	callers.Wait()
	if calls.Load() != 1 {
		t.Fatal("concurrent callers did not reuse the same cached credential")
	}
	s.mu.Lock()
	s.expires = time.Now().Add(4 * time.Minute)
	s.mu.Unlock()
	if _, err := s.Token(context.Background()); err != nil || calls.Load() != 2 {
		t.Fatal("token was not refreshed before expiry", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if token, err := s.Token(ctx); token != "" || !errors.Is(err, context.Canceled) || calls.Load() != 2 {
		t.Fatal("canceled caller received cached token or triggered renewal")
	}
	s.Revoke()
	if s.token != "" || !s.expires.IsZero() {
		t.Fatal("revocation retained the populated token cache")
	}
}

func TestRevokeCancelsOutstandingWorkWithoutWaitingForNativeCall(t *testing.T) {
	started, release, finished := make(chan context.Context, 1), make(chan struct{}), make(chan struct{})
	s := testSource(t, func(ctx context.Context, _ uintptr, _ string) (string, string, error) {
		started <- ctx
		<-release // Simulate a native call that is slow to observe cancellation.
		return fixtureToken(nil), fixtureReference().ID, nil
	})
	go func() {
		defer close(finished)
		if token, err := s.Token(context.Background()); token != "" || err == nil {
			t.Error("revoked source returned a native call's token")
		}
	}()
	nativeCtx := <-started
	revoked := make(chan struct{})
	go func() { s.Revoke(); close(revoked) }()
	select {
	case <-revoked:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("Revoke waited for the native call")
	}
	select {
	case <-nativeCtx.Done():
	case <-time.After(time.Second):
		close(release)
		t.Fatal("Revoke did not cancel native work")
	}
	if token, err := s.Token(context.Background()); token != "" || err == nil {
		t.Fatal("revoked source allowed another request")
	}
	close(release)
	<-finished
	if s.token != "" || !s.expires.IsZero() {
		t.Fatal("revoked source retained a cached credential")
	}
}

func TestCanceledCallerCannotReceiveNativeCredential(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := testSource(t, func(context.Context, uintptr, string) (string, string, error) {
		cancel()
		return fixtureToken(nil), fixtureReference().ID, nil
	})
	if token, err := s.Token(ctx); token != "" || !errors.Is(err, context.Canceled) || s.token != "" {
		t.Fatal("canceled acquisition returned or cached a credential")
	}
}

func TestNewSourceRejectsIncompleteIdentity(t *testing.T) {
	for _, ref := range []Reference{{}, {ID: "opaque"}, {ID: "opaque", Account: entra.Account{ID: "not-a-guid", TenantID: fixtureReference().Account.TenantID}}} {
		if source, err := NewSource(ref); err == nil {
			source.Revoke()
			t.Fatal("incomplete account binding accepted")
		}
	}
}
